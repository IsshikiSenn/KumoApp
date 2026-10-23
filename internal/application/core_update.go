package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/AstraFoundry/KumoApp/internal/domain/core"
	"github.com/AstraFoundry/KumoApp/internal/infrastructure/mihomo"
)

// What happened to TUN permission when the core was replaced.
const (
	// TUNKept: the core still has permission, or never needed any.
	TUNKept = "kept"
	// TUNRegranted: the permission was lost with the old file and given again.
	TUNRegranted = "regranted"
	// TUNNeedsGrant: the permission was lost and could not be given again.
	TUNNeedsGrant = "needs-grant"
)

// CoreUpdate is the outcome of installing the latest Mihomo.
type CoreUpdate struct {
	Path string `json:"path"`
	// Version is the release now installed.
	Version string `json:"version"`
	// Previous is the release that was installed before, if Kumo knew it.
	Previous string `json:"previous,omitempty"`
	// Updated is false when the installed release was already the latest and
	// nothing was downloaded.
	Updated bool `json:"updated"`
	// Restarted is true when the core was running and was started again.
	Restarted bool `json:"restarted"`
	// TUN is empty when TUN permission was not involved.
	TUN string `json:"tun,omitempty"`
}

// InstallCore installs the latest Mihomo as Kumo's own core, unless it is
// already installed.
//
// A running managed core is stopped for the swap and started again, since a
// running executable cannot be replaced everywhere and a replaced one would
// keep running the old release. The core is stopped only once the download
// is done, so a running core's proxy carries the download. TUN permission
// belongs to the file, so it is given again when the old file had it. The
// chosen core stays as it is unless it is the managed core or there was none.
func (s *SettingsService) InstallCore(ctx context.Context) (CoreUpdate, error) {
	s.installMu.Lock()
	defer s.installMu.Unlock()

	managed := s.deps.Paths.ManagedCore()
	record := s.deps.Paths.ManagedCoreRecord()
	previous := mihomo.ReadManaged(record).Version
	_, statErr := os.Stat(managed)
	existed := statErr == nil

	rt, err := s.runtime()
	if err != nil {
		return CoreUpdate{}, err
	}
	installer, tag, err := s.reachRelease(ctx, rt)
	if err != nil {
		return CoreUpdate{}, fmt.Errorf("reading the latest Mihomo version: %w", err)
	}
	update := CoreUpdate{Path: managed, Version: tag, Previous: previous}
	if existed && previous == tag {
		return update, s.chooseManaged(managed, existed)
	}
	binary, err := installer.Download(ctx, tag)
	if err != nil {
		return CoreUpdate{}, err
	}

	// The download can take a while; the core may have stopped or started.
	rt, err = s.runtime()
	if err != nil {
		return CoreUpdate{}, err
	}
	running := rt.IsActive() && samePath(rt.CorePath, managed)
	before := tunPermission(managed)
	if running {
		if _, err := s.lifecycle.Stop(ctx); err != nil {
			return CoreUpdate{}, err
		}
	}

	if err := mihomo.Place(managed, binary); err != nil {
		if running {
			_, _ = s.lifecycle.Start(ctx, StartOptions{})
		}
		return CoreUpdate{}, err
	}
	update.Updated = true
	var recordErr error
	if err := mihomo.WriteManaged(record, mihomo.Managed{Version: tag, InstalledAt: s.deps.Now()}); err != nil {
		recordErr = fmt.Errorf("mihomo %s is installed, but its version was not recorded: %w", tag, err)
	}
	chooseErr := s.chooseManaged(managed, existed)
	update.TUN = s.regrantTUN(ctx, before, managed)

	if running {
		if _, err := s.lifecycle.Start(ctx, StartOptions{}); err != nil {
			return update, errors.Join(fmt.Errorf("mihomo %s is installed, but it did not start: %w", tag, err), recordErr, chooseErr)
		}
		update.Restarted = true
	}
	return update, errors.Join(recordErr, chooseErr)
}

// runtime is the runtime record after reconciling it with the processes.
func (s *SettingsService) runtime() (core.Runtime, error) {
	if _, err := s.lifecycle.Status(); err != nil {
		return core.Runtime{}, err
	}
	return s.deps.Runtime.Load()
}

// proxyAttempt bounds a try through the core's proxy, so a proxy that
// accepts connections but goes nowhere does not hold up the direct try.
const proxyAttempt = 20 * time.Second

// reachRelease finds the latest release tag and the installer that got it.
// A running core carries the request through its own proxy first, since
// GitHub may only be reachable that way; it falls back to a direct request.
func (s *SettingsService) reachRelease(ctx context.Context, rt core.Runtime) (mihomo.Installer, string, error) {
	if rt.State == core.StateRunning && rt.Readiness == core.ReadinessControllerReady {
		installer := s.deps.Installer.ViaProxy(rt.Settings.MixedPort)
		tryCtx, cancel := context.WithTimeout(ctx, proxyAttempt)
		tag, err := installer.Latest(tryCtx)
		cancel()
		if err == nil {
			return installer, tag, nil
		}
	}
	tag, err := s.deps.Installer.Latest(ctx)
	return s.deps.Installer, tag, err
}

// chooseManaged selects the managed core after an install, unless the user
// chose another one. It is selected when it is new, when it is already the
// choice, and when nothing was chosen.
func (s *SettingsService) chooseManaged(managed string, existed bool) error {
	rt, err := s.deps.Runtime.Load()
	if err != nil {
		return err
	}
	if existed && rt.CorePath != "" && !samePath(rt.CorePath, managed) {
		return nil
	}
	return s.SetCorePath(managed)
}

// regrantTUN gives TUN permission to the new file when the old one had it by
// setuid or capability. Those belong to the file and are gone after a swap;
// root and administrator belong to Kumo and stay.
func (s *SettingsService) regrantTUN(ctx context.Context, before TUNPermission, path string) string {
	if !before.Granted {
		return ""
	}
	if before.Method != "setuid" && before.Method != "capability" {
		return TUNKept
	}
	if err := s.grantTUN(ctx, path); err != nil {
		return TUNNeedsGrant
	}
	return TUNRegranted
}

func (s *SettingsService) grantTUN(ctx context.Context, path string) error {
	if s.deps.GrantTUN != nil {
		return s.deps.GrantTUN(ctx, path)
	}
	return grantTUN(ctx, path)
}

// markManaged flags the managed core among the candidates and fills in its
// version, the only one whose release Kumo knows.
func (s *SettingsService) markManaged(candidates []mihomo.Candidate) {
	version := mihomo.ReadManaged(s.deps.Paths.ManagedCoreRecord()).Version
	managed := s.deps.Paths.ManagedCore()
	for i := range candidates {
		if samePath(candidates[i].Path, managed) {
			candidates[i].Managed = true
			candidates[i].Version = version
		}
	}
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if absA, err := filepath.Abs(a); err == nil {
		a = absA
	}
	if absB, err := filepath.Abs(b); err == nil {
		b = absB
	}
	if a == b {
		return true
	}
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}
