//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// proxiedHost is the Host an authenticating proxy in front of focal forwards
// unchanged: the external name the client addressed, not focal's own address.
const proxiedHost = "focal.example.com"

// hostProbe is what one request with a given Host came back as.
type hostProbe struct {
	status int
	body   string
}

// probeHost sends one tools/list to focal through client, addressed to url but
// carrying host as its Host header, token as its bearer (none when empty) and
// any extra headers, and returns the status and body focal answered with.
func probeHost(t *testing.T, client *http.Client, url, host, token string, extra map[string]string) hostProbe {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return hostProbe{status: resp.StatusCode, body: string(raw)}
}

// unixClient is an HTTP client whose every connection is the Unix domain socket
// at path, whatever host the request URL names.
func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
}

// e2eSocketPath returns an unused socket path in a directory of focal's own,
// short enough for sun_path wherever TMPDIR points.
func e2eSocketPath(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if len(base)+len("/focal1234567890/focal.sock") > 103 {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "focal")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "focal.sock")
}

// TestServeHostBehindProxy is what focal serve answers a request that reached
// it through an authenticating proxy, which forwards the external name it was
// addressed by in Host. With a token, focal leaves Host unchecked and answers
// on the token alone; the cross-origin check stays in place either way.
func TestServeHostBehindProxy(t *testing.T) {
	h := requireHarness(t)

	t.Run("loopback TCP with a token", func(t *testing.T) {
		url := h.startServe(t, upstreamToken)

		t.Run("external Host and the token", func(t *testing.T) {
			got := probeHost(t, http.DefaultClient, url, proxiedHost, upstreamToken, nil)
			if got.status != http.StatusOK || !strings.Contains(got.body, `"tools"`) {
				t.Errorf("status = %d, want 200 with the tool list\n%s", got.status, got.body)
			}
		})
		t.Run("external Host and no token", func(t *testing.T) {
			got := probeHost(t, http.DefaultClient, url, proxiedHost, "", nil)
			if got.status != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401\n%s", got.status, got.body)
			}
		})
		t.Run("cross-site request with the token", func(t *testing.T) {
			got := probeHost(t, http.DefaultClient, url, proxiedHost, upstreamToken, map[string]string{
				"Origin":         "https://evil.example",
				"Sec-Fetch-Site": "cross-site",
			})
			if got.status != http.StatusForbidden {
				t.Errorf("status = %d, want 403\n%s", got.status, got.body)
			}
		})
	})

	t.Run("Unix socket with a token", func(t *testing.T) {
		path := e2eSocketPath(t)
		h.startServeOn(t, "unix:"+path, upstreamToken)
		got := probeHost(t, unixClient(path), fmt.Sprintf("http://%s/", proxiedHost), proxiedHost, upstreamToken, nil)
		if got.status != http.StatusOK {
			t.Errorf("Host %s -> %d, want 200\n%s", proxiedHost, got.status, got.body)
		}
	})

	t.Run("Unix socket without a token", func(t *testing.T) {
		path := e2eSocketPath(t)
		h.startServeOn(t, "unix:"+path, "")
		got := probeHost(t, unixClient(path), fmt.Sprintf("http://%s/", proxiedHost), proxiedHost, "", nil)
		if got.status != http.StatusOK {
			t.Errorf("Host %s -> %d, want 200\n%s", proxiedHost, got.status, got.body)
		}
	})
}
