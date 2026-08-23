//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// expectedTools is the tool set focal serve exposes under the default
// configuration: one per enabled operation, kernel excluded because it is off
// by default, sorted. The names are the wire names an agent calls.
var expectedTools = []string{
	"inspect",
	"inspect_cpu",
	"inspect_logs",
	"inspect_memory",
	"inspect_network",
	"inspect_processes",
	"inspect_service",
	"inspect_storage",
	"inspect_system",
}

// TestMCPToolsAndCall drives the Remote MCP path with the protocol's own
// go-sdk client: it lists the tools focal serves and calls two of them against
// the real container, one that succeeds and one that must be refused.
func TestMCPToolsAndCall(t *testing.T) {
	h := requireHarness(t)
	url := h.startServe(t, "--upstream-token", upstreamToken)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session := connectMCP(t, ctx, url, upstreamToken)
	defer session.Close()

	t.Run("tools/list", func(t *testing.T) {
		got := listToolNames(t, ctx, session)
		if !slices.Equal(got, expectedTools) {
			t.Errorf("tools = %v, want %v", got, expectedTools)
		}
		// A second list must come back in the same order: an agent that
		// caches or diffs the tool list depends on it being stable.
		if again := listToolNames(t, ctx, session); !slices.Equal(again, got) {
			t.Errorf("tools/list not deterministic: %v then %v", got, again)
		}
	})

	t.Run("every tool is read-only", func(t *testing.T) {
		res, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		for _, tool := range res.Tools {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Errorf("tool %q is not marked readOnlyHint", tool.Name)
			}
		}
	})

	t.Run("inspect_system against the container", func(t *testing.T) {
		res, err := session.CallTool(ctx, &sdk.CallToolParams{
			Name:      "inspect_system",
			Arguments: map[string]any{"host": targetHost},
		})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if res.IsError {
			t.Fatalf("inspect_system reported an error: %s", toolText(t, res))
		}
		text := toolText(t, res)
		env := decodeEnvelope(t, text)
		if env.Status != "ok" {
			t.Errorf("status = %q, want ok", env.Status)
		}
		// system runs uname and friends, whose output lands in the
		// envelope's parts. Searching the raw result keeps the check
		// independent of exactly which part carried it.
		if !strings.Contains(strings.ToLower(text), "linux") {
			t.Errorf("system output does not look like a Linux host:\n%s", text)
		}
	})

	t.Run("inspect_service rejects injection", func(t *testing.T) {
		res, err := session.CallTool(ctx, &sdk.CallToolParams{
			Name:      "inspect_service",
			Arguments: map[string]any{"host": targetHost, "service": "nginx;id"},
		})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if !res.IsError {
			t.Fatalf("injection was not refused: %s", toolText(t, res))
		}
		e := decodeError(t, toolText(t, res))
		if e.Kind != "validation" {
			t.Errorf("kind = %q, want validation", e.Kind)
		}
	})
}

// TestMCPAuth confirms the shared-secret check in front of the MCP handler: a
// request without the token, or with the wrong one, is refused with 401 before
// it reaches any tool.
func TestMCPAuth(t *testing.T) {
	h := requireHarness(t)
	url := h.startServe(t, "--upstream-token", upstreamToken)

	for name, header := range map[string]string{
		"no token":    "",
		"wrong token": "Bearer wrong-token-value",
	} {
		t.Run(name, func(t *testing.T) {
			body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
			req, err := http.NewRequest(http.MethodPost, url, body)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
		})
	}
}

// TestServeRejectsUnauthenticatedNonLoopback confirms focal refuses to bind an
// address reachable from beyond the machine when nothing authenticates the
// caller — no token, no --allow-unauthenticated-listen. The refusal is at
// startup, before any socket exists.
func TestServeRejectsUnauthenticatedNonLoopback(t *testing.T) {
	h := requireHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stdout, stderr, code := h.run(ctx, nil, "serve", "--listen", "0.0.0.0:0")
	if code == 0 {
		t.Fatalf("serve unexpectedly started\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	e := decodeError(t, stderr)
	if e.Kind != "validation" {
		t.Errorf("kind = %q, want validation\n%s", e.Kind, stderr)
	}
	if e.Code != "unauthenticated_listen_address" {
		t.Errorf("code = %q, want unauthenticated_listen_address\n%s", e.Code, stderr)
	}
}

// startServe launches `focal serve` on a free loopback port with the given
// extra flags, waits until it reports it is listening, and registers its
// shutdown. It returns the base URL of the MCP endpoint.
func (h *harness) startServe(t *testing.T, extraArgs ...string) string {
	t.Helper()
	port := freePort(t)
	listen := fmt.Sprintf("127.0.0.1:%d", port)
	args := append([]string{"serve", "--config", h.configFile, "--listen", listen}, extraArgs...)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, h.focalBin, args...)
	cmd.Env = h.env

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start serve: %v", err)
	}

	// Drain stderr, signalling the moment focal says it is listening and
	// keeping the whole log for a diagnostic if it never does.
	var (
		mu       sync.Mutex
		logLines []string
	)
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stderr)
		var once sync.Once
		for scanner.Scan() {
			line := scanner.Text()
			mu.Lock()
			logLines = append(logLines, line)
			mu.Unlock()
			if strings.Contains(line, "listening on") {
				once.Do(func() { close(ready) })
			}
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		mu.Lock()
		log := strings.Join(logLines, "\n")
		mu.Unlock()
		t.Fatalf("focal serve never reported listening:\n%s", log)
	}
	return "http://" + listen + "/"
}

// connectMCP opens an MCP session to url authenticating with token, using the
// protocol's own go-sdk client so the transport handles the SSE framing,
// protocol-version negotiation and headers.
func connectMCP(t *testing.T, ctx context.Context, url, token string) *sdk.ClientSession {
	t.Helper()
	transport := &sdk.StreamableClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &bearerRoundTripper{token: token, base: http.DefaultTransport},
		},
		// The server is stateless (2026-07-28); it sends nothing on a
		// standalone stream, so the client need not open one.
		DisableStandaloneSSE: true,
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "focal-e2e", Version: "0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect MCP: %v", err)
	}
	return session
}

// listToolNames returns the sorted names of the tools the session reports.
func listToolNames(t *testing.T, ctx context.Context, session *sdk.ClientSession) []string {
	t.Helper()
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// toolText returns the single text content of a tool result, which is where
// focal puts the envelope or the error JSON.
func toolText(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("tool result has %d content items, want 1", len(res.Content))
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("tool content is %T, want *TextContent", res.Content[0])
	}
	return text.Text
}

// bearerRoundTripper attaches the upstream token to every request, which is how
// the client crosses the shared-secret check in front of the MCP handler.
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if b.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(req)
}

// freePort asks the kernel for an unused loopback TCP port and returns it. A
// short race between closing the listener and focal binding is unavoidable and
// harmless on a loopback interface running one test binary.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
