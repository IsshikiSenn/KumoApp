package mihomo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestViaProxyReachesReleasesThroughTheProxy(t *testing.T) {
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.String())
		fmt.Fprintln(w, "v1.2.3")
	}))
	defer proxy.Close()
	u, _ := url.Parse(proxy.URL)
	port, _ := strconv.Atoi(u.Port())

	// The host does not resolve, so only the proxy can answer.
	installer := Installer{Base: "http://github.invalid/releases"}.ViaProxy(port)
	tag, err := installer.Latest(context.Background())
	if err != nil || tag != "v1.2.3" {
		t.Fatalf("got %q, %v", tag, err)
	}
	if len(seen) != 1 || seen[0] != "http://github.invalid/releases/latest/download/version.txt" {
		t.Fatalf("the proxy saw %v", seen)
	}
}
