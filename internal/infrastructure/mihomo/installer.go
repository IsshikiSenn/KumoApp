package mihomo

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ReleaseBase is where MetaCubeX publishes Mihomo builds.
const ReleaseBase = "https://github.com/MetaCubeX/mihomo/releases"

// Installer fetches Mihomo builds. The zero value talks to GitHub.
type Installer struct {
	// Base is the releases URL; empty means ReleaseBase.
	Base string
	// Client makes the requests; nil means a client with a five minute timeout.
	Client *http.Client
}

// ViaProxy is an Installer that reaches the releases through the HTTP
// proxy a running core listens on, for networks where GitHub is only
// reachable that way.
func (i Installer) ViaProxy(port int) Installer {
	proxy := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	i.Client = &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	return i
}

// Latest reads the newest release tag, such as v1.19.33. It reads the
// release's version.txt rather than GitHub's API, which rate limits
// anonymous callers.
func (i Installer) Latest(ctx context.Context) (string, error) {
	body, err := i.get(ctx, i.base()+"/latest/download/version.txt")
	if err != nil {
		return "", err
	}
	tag := strings.TrimSpace(string(body))
	if tag == "" {
		return "", fmt.Errorf("the release has no version")
	}
	return tag, nil
}

// Download fetches the build of tag for this machine and returns the
// executable.
func (i Installer) Download(ctx context.Context, tag string) ([]byte, error) {
	asset := AssetName(runtime.GOOS, runtime.GOARCH, tag)
	archive, err := i.get(ctx, i.base()+"/download/"+tag+"/"+asset)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", asset, err)
	}
	return extract(asset, archive)
}

func (i Installer) base() string {
	if i.Base != "" {
		return strings.TrimRight(i.Base, "/")
	}
	return ReleaseBase
}

func (i Installer) get(ctx context.Context, url string) ([]byte, error) {
	client := i.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return get(ctx, client, url)
}

// Place writes the executable to dest, replacing what is there. The new
// file is a different file, so permissions given to the old one (such as
// TUN's setuid bit) do not carry over.
func Place(dest string, binary []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".download"
	if err := os.WriteFile(tmp, binary, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// AssetName is the release file for a platform, such as
// mihomo-darwin-arm64-v1.19.32.gz or mihomo-windows-amd64-v1.19.32.zip.
// amd64 builds use the "compatible" variant, which runs on every x86-64 CPU.
func AssetName(goos, goarch, tag string) string {
	arch := goarch
	if goarch == "amd64" {
		arch = "amd64-compatible"
	}
	ext := ".gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("mihomo-%s-%s-%s%s", goos, arch, tag, ext)
}

func get(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

func extract(name string, archive []byte) ([]byte, error) {
	if strings.HasSuffix(name, ".zip") {
		r, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range r.File {
			if strings.HasSuffix(strings.ToLower(f.Name), ".exe") {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("%s holds no executable", name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return io.ReadAll(gz)
}
