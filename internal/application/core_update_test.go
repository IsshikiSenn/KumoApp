package application

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/AstraFoundry/KumoApp/internal/domain/core"
	"github.com/AstraFoundry/KumoApp/internal/infrastructure/mihomo"
	"github.com/AstraFoundry/KumoApp/internal/infrastructure/paths"
	"github.com/AstraFoundry/KumoApp/internal/infrastructure/process"
	"github.com/AstraFoundry/KumoApp/internal/infrastructure/sysproxy"
)

// release is a stand-in for Mihomo's GitHub releases.
type release struct {
	mu        sync.Mutex
	tag       string
	archive   []byte
	downloads int
	// onDownload runs when a build is being served.
	onDownload func()
	srv        *httptest.Server
}

func newRelease(t *testing.T, tag string, binary []byte) *release {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake release serves .gz builds")
	}
	var packed bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&packed, gzip.BestSpeed)
	_, _ = gz.Write(binary)
	_ = gz.Close()
	r := &release{tag: tag, archive: packed.Bytes()}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *release) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch req.URL.Path {
	case "/latest/download/version.txt":
		fmt.Fprintln(w, r.tag)
	case "/download/" + r.tag + "/" + mihomo.AssetName(runtime.GOOS, runtime.GOARCH, r.tag):
		r.downloads++
		if r.onDownload != nil {
			r.onDownload()
		}
		_, _ = w.Write(r.archive)
	default:
		http.NotFound(w, req)
	}
}

func (r *release) publish(tag string) {
	r.mu.Lock()
	r.tag = tag
	r.mu.Unlock()
}

func (r *release) downloaded() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.downloads
}

func (r *release) installer() mihomo.Installer { return mihomo.Installer{Base: r.srv.URL} }

type installKit struct {
	k      *Kumo
	rel    *release
	paths  paths.Paths
	grants []string
	grant  error
}

// newInstallKit is a Kumo whose Mihomo comes from a fake release and whose
// TUN grants are recorded instead of asked of the system.
func newInstallKit(t *testing.T) *installKit {
	t.Helper()
	kit := &installKit{paths: paths.Paths{Root: t.TempDir()}}
	kit.rel = newRelease(t, "v1.0.0", []byte("#!/bin/sh\nexit 0\n"))
	deps := DefaultDependencies(kit.paths, core.OwnerApp)
	deps.SystemProxy = sysproxy.NewMemory()
	deps.Installer = kit.rel.installer()
	deps.GrantTUN = func(_ context.Context, path string) error {
		kit.grants = append(kit.grants, path)
		return kit.grant
	}
	kit.k = New(deps)
	if err := kit.paths.Prepare(); err != nil {
		t.Fatal(err)
	}
	return kit
}

func (kit *installKit) install(t *testing.T) CoreUpdate {
	t.Helper()
	update, err := kit.k.Settings.InstallCore(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	return update
}

func TestInstallCoreFirstInstall(t *testing.T) {
	kit := newInstallKit(t)
	update := kit.install(t)
	if !update.Updated || update.Version != "v1.0.0" || update.Previous != "" || update.Restarted || update.TUN != "" {
		t.Fatalf("unexpected outcome: %+v", update)
	}
	managed := kit.paths.ManagedCore()
	if update.Path != managed {
		t.Fatalf("path %q, want %q", update.Path, managed)
	}
	if info, err := os.Stat(managed); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("managed core not an executable: %v", err)
	}
	if got := mihomo.ReadManaged(kit.paths.ManagedCoreRecord()).Version; got != "v1.0.0" {
		t.Fatalf("recorded version %q", got)
	}
	settings, err := kit.k.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(settings.CorePath, managed) {
		t.Fatalf("managed core not chosen: %q", settings.CorePath)
	}
	var versions []string
	for _, c := range settings.Candidates {
		if c.Managed {
			if !samePath(c.Path, managed) {
				t.Fatalf("%q flagged as the managed core", c.Path)
			}
			versions = append(versions, c.Version)
		}
	}
	if len(versions) != 1 || versions[0] != "v1.0.0" {
		t.Fatalf("candidate versions %v", versions)
	}
}

func TestInstallCoreAlreadyLatestDownloadsNothing(t *testing.T) {
	kit := newInstallKit(t)
	kit.install(t)
	update := kit.install(t)
	if update.Updated || update.Version != "v1.0.0" || update.Previous != "v1.0.0" {
		t.Fatalf("unexpected outcome: %+v", update)
	}
	if n := kit.rel.downloaded(); n != 1 {
		t.Fatalf("downloaded %d times, want 1", n)
	}
}

func TestInstallCoreWithoutRecordInstallsAgain(t *testing.T) {
	kit := newInstallKit(t)
	kit.install(t)
	if err := os.Remove(kit.paths.ManagedCoreRecord()); err != nil {
		t.Fatal(err)
	}
	update := kit.install(t)
	if !update.Updated || update.Previous != "" {
		t.Fatalf("unexpected outcome: %+v", update)
	}
	if n := kit.rel.downloaded(); n != 2 {
		t.Fatalf("downloaded %d times, want 2", n)
	}
	if got := mihomo.ReadManaged(kit.paths.ManagedCoreRecord()).Version; got != "v1.0.0" {
		t.Fatalf("recorded version %q", got)
	}
}

func TestInstallCoreNewReleaseReplacesAndRecords(t *testing.T) {
	kit := newInstallKit(t)
	kit.install(t)
	kit.rel.publish("v1.0.1")
	update := kit.install(t)
	if !update.Updated || update.Version != "v1.0.1" || update.Previous != "v1.0.0" {
		t.Fatalf("unexpected outcome: %+v", update)
	}
	if got := mihomo.ReadManaged(kit.paths.ManagedCoreRecord()).Version; got != "v1.0.1" {
		t.Fatalf("recorded version %q", got)
	}
}

func TestInstallCoreKeepsAnotherChosenCore(t *testing.T) {
	kit := newInstallKit(t)
	kit.install(t)
	other := filepath.Join(t.TempDir(), "mihomo-mine")
	if err := os.WriteFile(other, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := kit.k.Settings.SetCorePath(other); err != nil {
		t.Fatal(err)
	}
	kit.rel.publish("v1.0.1")
	if update := kit.install(t); !update.Updated {
		t.Fatalf("not updated: %+v", update)
	}
	if update := kit.install(t); update.Updated {
		t.Fatalf("updated twice: %+v", update)
	}
	settings, err := kit.k.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	if settings.CorePath != other {
		t.Fatalf("the chosen core changed to %q", settings.CorePath)
	}
}

func TestInstallCoreOfflineChangesNothing(t *testing.T) {
	kit := newInstallKit(t)
	kit.install(t)
	deps := kit.k.deps
	deps.Installer = mihomo.Installer{Base: "http://127.0.0.1:1"}
	offline := New(deps)
	if _, err := offline.Settings.InstallCore(context.Background()); err == nil {
		t.Fatal("installed without a network")
	}
	if got := mihomo.ReadManaged(kit.paths.ManagedCoreRecord()).Version; got != "v1.0.0" {
		t.Fatalf("recorded version %q", got)
	}
}

func runningOn(port int) core.Runtime {
	rt := core.Runtime{State: core.StateRunning, Readiness: core.ReadinessControllerReady}
	rt.Settings.MixedPort = port
	return rt
}

// A running core carries the check for the latest release through its proxy,
// for networks where GitHub is only reachable that way.
func TestReachReleaseUsesTheRunningCoresProxy(t *testing.T) {
	kit := newInstallKit(t)
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Host)
		fmt.Fprintln(w, "v7.7.7")
	}))
	defer proxy.Close()
	u, _ := url.Parse(proxy.URL)
	port, _ := strconv.Atoi(u.Port())

	deps := kit.k.deps
	deps.Installer = mihomo.Installer{Base: "http://github.invalid/releases"}
	settings := New(deps).Settings
	_, tag, err := settings.reachRelease(context.Background(), runningOn(port))
	if err != nil || tag != "v7.7.7" || len(seen) != 1 || seen[0] != "github.invalid" {
		t.Fatalf("tag %q, err %v, the proxy saw %v", tag, err, seen)
	}

	// A stopped core has no proxy to use.
	if _, _, err := settings.reachRelease(context.Background(), core.Runtime{}); err == nil {
		t.Fatal("reached an unresolvable host directly")
	}
}

func TestReachReleaseFallsBackToDirectWhenTheProxyIsDown(t *testing.T) {
	kit := newInstallKit(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	u, _ := url.Parse(dead.URL)
	port, _ := strconv.Atoi(u.Port())
	dead.Close()

	installer, tag, err := kit.k.Settings.reachRelease(context.Background(), runningOn(port))
	if err != nil || tag != "v1.0.0" || installer.Client != nil {
		t.Fatalf("tag %q, err %v, direct %v", tag, err, installer.Client == nil)
	}
}

func TestRegrantTUN(t *testing.T) {
	cases := []struct {
		name      string
		before    TUNPermission
		grant     error
		want      string
		wantGrant bool
	}{
		{name: "never granted", before: TUNPermission{}, want: ""},
		{name: "root stays", before: TUNPermission{Granted: true, Method: "root"}, want: TUNKept},
		{name: "administrator stays", before: TUNPermission{Granted: true, Method: "administrator"}, want: TUNKept},
		{name: "setuid is given again", before: TUNPermission{Granted: true, Method: "setuid"}, want: TUNRegranted, wantGrant: true},
		{name: "capability is given again", before: TUNPermission{Granted: true, Method: "capability"}, want: TUNRegranted, wantGrant: true},
		{name: "refused", before: TUNPermission{Granted: true, Method: "setuid"}, grant: fmt.Errorf("no"), want: TUNNeedsGrant, wantGrant: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kit := newInstallKit(t)
			kit.grant = c.grant
			got := kit.k.Settings.regrantTUN(context.Background(), c.before, "/x/mihomo")
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			if (len(kit.grants) == 1) != c.wantGrant || len(kit.grants) > 1 {
				t.Fatalf("grants: %v", kit.grants)
			}
		})
	}
}

// A running managed core is stopped for the swap and started again, on the
// new release.
func TestSimInstallCoreRestartsRunningCore(t *testing.T) {
	binary := coreBinary(t)
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	s := newSim(t)
	rel := newRelease(t, "v9.9.1", data)
	s.installer = rel.installer()
	s.kumo = s.instance(core.OwnerApp)
	ctx := context.Background()

	first, err := s.kumo.Settings.InstallCore(ctx)
	if err != nil || !first.Updated || first.Restarted {
		t.Fatalf("first install: %+v %v", first, err)
	}
	before := s.start(StartOptions{})
	if !samePath(before.CorePath, first.Path) {
		t.Fatalf("running %q, want the managed core %q", before.CorePath, first.Path)
	}

	// The core is the way to GitHub for some users, so it must still run
	// while the build is downloaded.
	var runningDuringDownload bool
	rel.onDownload = func() {
		st, _ := s.kumo.Lifecycle.Status()
		runningDuringDownload = st.Running() && st.PID == before.PID
	}
	rel.publish("v9.9.2")
	update, err := s.kumo.Settings.InstallCore(ctx)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !update.Updated || !update.Restarted || update.Previous != "v9.9.1" || update.Version != "v9.9.2" {
		t.Fatalf("unexpected outcome: %+v", update)
	}
	if !runningDuringDownload {
		t.Fatal("the core was not running while the new release downloaded")
	}
	after, err := s.kumo.Lifecycle.Status()
	if err != nil || !after.Running() || after.PID == before.PID {
		t.Fatalf("core not restarted: %+v %v", after, err)
	}
	if process.Alive(before.PID) {
		t.Fatal("the old core is still running")
	}

	again, err := s.kumo.Settings.InstallCore(ctx)
	if err != nil || again.Updated || again.Restarted {
		t.Fatalf("a current core was touched: %+v %v", again, err)
	}
	if still, _ := s.kumo.Lifecycle.Status(); still.PID != after.PID {
		t.Fatal("a current core was restarted")
	}
}

// Updating Kumo's own core leaves a running core of another file alone.
func TestSimInstallCoreLeavesOtherRunningCoreAlone(t *testing.T) {
	binary := coreBinary(t)
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	s := newSim(t)
	rel := newRelease(t, "v9.9.1", data)
	s.installer = rel.installer()
	s.kumo = s.instance(core.OwnerApp)
	ctx := context.Background()
	if _, err := s.kumo.Settings.InstallCore(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.kumo.Settings.SetCorePath(binary); err != nil {
		t.Fatal(err)
	}
	running := s.start(StartOptions{})

	rel.publish("v9.9.2")
	update, err := s.kumo.Settings.InstallCore(ctx)
	if err != nil || !update.Updated || update.Restarted {
		t.Fatalf("unexpected outcome: %+v %v", update, err)
	}
	after, _ := s.kumo.Lifecycle.Status()
	if after.PID != running.PID || !after.Running() || after.CorePath != binary {
		t.Fatalf("the running core was disturbed: %+v", after)
	}
}
