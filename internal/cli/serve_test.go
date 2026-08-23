package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
)

// serveApp builds an app whose executor is a Recorder, and reports what the
// serve command wrote to stderr and which connection options each executor was
// built from.
type serveApp struct {
	app      *app
	stderr   *bytes.Buffer
	recorder *sshtest.Recorder
	opts     []ssh.Options
}

func newServeApp(t *testing.T) *serveApp {
	t.Helper()
	s := &serveApp{stderr: &bytes.Buffer{}, recorder: &sshtest.Recorder{}}
	s.app = &app{
		stdout: &bytes.Buffer{},
		stderr: s.stderr,
		newExecutor: func(opts ssh.Options) (ssh.Executor, *result.Error) {
			s.opts = append(s.opts, opts)
			return s.recorder, nil
		},
	}
	return s
}

func TestServeListenDefaultsToLoopback(t *testing.T) {
	cmd := newServeCommand(newServeApp(t).app)

	flag := cmd.Flags().Lookup("listen")
	if flag == nil {
		t.Fatal("serve has no --listen flag")
	}
	if flag.DefValue != "127.0.0.1:8080" {
		t.Errorf("--listen default = %q, want 127.0.0.1:8080", flag.DefValue)
	}
	if cmd.Flags().Lookup("allow-unauthenticated-listen") == nil {
		t.Error("serve has no --allow-unauthenticated-listen flag")
	}
}

// TestListenAddressPolicy is focal serve's safe-side default stated as a
// table: focal has no authentication of its own, so it will only bind where
// nothing but this machine can reach it unless the operator says otherwise.
// The judgement is made on the parsed address, never on how it was spelled —
// [::1] and ::ffff:127.0.0.1 are the same loopback 127.0.0.1 is.
func TestListenAddressPolicy(t *testing.T) {
	tests := []struct {
		addr     string
		loopback bool
	}{
		{addr: "127.0.0.1:8080", loopback: true},
		{addr: "127.0.0.2:8080", loopback: true},
		{addr: "[::1]:8080", loopback: true},
		{addr: "[::ffff:127.0.0.1]:8080", loopback: true},
		{addr: "localhost:8080", loopback: true},
		{addr: "0.0.0.0:8080", loopback: false},
		{addr: "[::]:8080", loopback: false},
		{addr: ":8080", loopback: false},
		{addr: "192.0.2.1:8080", loopback: false},
		// A name focal cannot resolve is not a name focal will trust.
		{addr: "nowhere.invalid:8080", loopback: false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got, err := listensOnLoopback(tt.addr)
			if err != nil {
				t.Fatalf("listensOnLoopback: %v", err)
			}
			if got != tt.loopback {
				t.Errorf("listensOnLoopback(%q) = %v, want %v", tt.addr, got, tt.loopback)
			}
		})
	}
}

func TestListenAddressMustBeHostPort(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "", "::1:8080"} {
		if _, err := listensOnLoopback(addr); err == nil {
			t.Errorf("listensOnLoopback(%q) was accepted", addr)
		} else if err.Code != "invalid_listen_address" {
			t.Errorf("listensOnLoopback(%q): code = %q, want invalid_listen_address", addr, err.Code)
		}
	}
}

func TestListenRefusesANonLoopbackAddressWithoutTheFlag(t *testing.T) {
	s := newServeApp(t)

	ln, err := s.app.listen(serveOptions{listen: "0.0.0.0:0"})
	if err == nil {
		_ = ln.Close()
		t.Fatal("focal serve bound a non-loopback address without being told to")
	}
	if err.code != exitUsage {
		t.Errorf("exit code = %d, want %d", err.code, exitUsage)
	}
	if err.err.Code != "unauthenticated_listen_address" {
		t.Errorf("code = %q, want unauthenticated_listen_address", err.err.Code)
	}
	if !slices.Contains(err.err.Allowed, "--allow-unauthenticated-listen") {
		t.Errorf("allowed = %v, want it to name the flag that permits this", err.err.Allowed)
	}
}

func TestListenBindsLoopbackQuietly(t *testing.T) {
	s := newServeApp(t)

	ln, err := s.app.listen(serveOptions{listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("listen: %v", err.err)
	}
	defer ln.Close()

	if got := s.stderr.String(); got != "" {
		t.Errorf("stderr = %q, want nothing for a loopback bind", got)
	}
}

func TestListenWarnsWhenBindingBeyondLoopback(t *testing.T) {
	s := newServeApp(t)

	ln, err := s.app.listen(serveOptions{listen: "0.0.0.0:0", allowUnauthenticated: true})
	if err != nil {
		t.Fatalf("listen: %v", err.err)
	}
	defer ln.Close()

	got := s.stderr.String()
	if !strings.Contains(got, "warning") {
		t.Errorf("stderr = %q, want a warning", got)
	}
	if !strings.Contains(got, "authentication") {
		t.Errorf("stderr = %q, want it to say focal has no authentication of its own", got)
	}
}

// serveHost starts the whole of focal serve's HTTP surface — the real MCP
// handler over the real runner — with a Recorder in place of ssh(1).
func serveHost(t *testing.T, s *serveApp, o serveOptions) string {
	t.Helper()
	handler, err := s.app.serveHandler(o)
	if err != nil {
		t.Fatalf("serveHandler: %v", err.err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

// callTool posts one tools/call the way a 2026-07-28 client does and returns
// the decoded result.
func callTool(t *testing.T, url, tool string, args map[string]any) (content string, isError bool) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "focal-test", "version": "0"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
			"name":      tool,
			"arguments": args,
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", tool)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var payload []byte
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
			payload = []byte(data)
			break
		}
		if strings.HasPrefix(scanner.Text(), "{") {
			payload = scanner.Bytes()
			break
		}
	}
	if len(payload) == 0 {
		t.Fatalf("no JSON-RPC message in the response (status %d)", resp.StatusCode)
	}
	var out struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("decode %q: %v", payload, err)
	}
	if out.Error != nil {
		t.Fatalf("tools/call %s: JSON-RPC error %s", tool, out.Error)
	}
	if len(out.Result.Content) == 0 {
		t.Fatalf("tools/call %s: no content", tool)
	}
	return out.Result.Content[0].Text, out.Result.IsError
}

func TestServeRunsTheOperationOverSSH(t *testing.T) {
	withConfig(t, "")
	s := newServeApp(t)
	url := serveHost(t, s, serveOptions{listen: "127.0.0.1:0"})

	content, isError := callTool(t, url, "inspect_logs", map[string]any{
		"host": "web", "service": "nginx", "since": "30m", "lines": 200,
	})
	if isError {
		t.Fatalf("call failed: %s", content)
	}

	calls := s.recorder.Calls()
	if len(calls) != 1 {
		t.Fatalf("recorder saw %d calls, want 1", len(calls))
	}
	if got := calls[0].Target.String(); got != "web" {
		t.Errorf("target = %q, want web", got)
	}
	if got := calls[0].Command.Program(); got != "journalctl" {
		t.Errorf("program = %q, want journalctl", got)
	}
	want := []string{"-u", "nginx.service", "--since=-30m", "-n", "200", "--no-pager", "--output=short-iso"}
	if got := calls[0].Command.Args(); !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestServeRefusesAnInjectedServiceName(t *testing.T) {
	withConfig(t, "")
	s := newServeApp(t)
	url := serveHost(t, s, serveOptions{listen: "127.0.0.1:0"})

	content, isError := callTool(t, url, "inspect_logs", map[string]any{"host": "web", "service": "nginx;id"})
	if !isError {
		t.Fatalf("an injected unit name was accepted: %s", content)
	}
	if len(s.recorder.Calls()) != 0 {
		t.Errorf("it still reached the executor: %+v", s.recorder.Calls())
	}
	var got result.Error
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("error content is not focal's schema: %v (%q)", err, content)
	}
	if got.Kind != result.KindValidation {
		t.Errorf("kind = %q, want %q", got.Kind, result.KindValidation)
	}
}

// TestIdentityResolutionOrder fixes the layering: what the tool call named
// wins, the alias registered as "default" fills in for a call that named
// nothing, and with neither the choice falls to the configuration file and
// then to ssh(1) itself.
func TestIdentityResolutionOrder(t *testing.T) {
	dir := t.TempDir()
	def := keyFile(t, dir, "id_ed25519", 0o600)
	customer := keyFile(t, dir, "customer-a.pem", 0o600)
	configured := keyFile(t, dir, "configured.pem", 0o600)

	tests := []struct {
		name       string
		registered []string
		configured string
		identity   string
		want       string
	}{
		{"call names an alias", []string{"default=" + def, "customer-a=" + customer}, "", "customer-a", customer},
		{"call names none", []string{"default=" + def, "customer-a=" + customer}, "", "", def},
		{"call beats the config file", []string{"customer-a=" + customer}, configured, "customer-a", customer},
		{"default beats the config file", []string{"default=" + def}, configured, "", def},
		{"config file fills in", []string{"customer-a=" + customer}, configured, "", configured},
		{"nothing at all is left to ssh", []string{"customer-a=" + customer}, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := ""
			if tt.configured != "" {
				body = "execution:\n  identity_file: " + tt.configured + "\n"
			}
			withConfig(t, body)
			s := newServeApp(t)
			url := serveHost(t, s, serveOptions{listen: "127.0.0.1:0", identities: tt.registered})

			args := map[string]any{"host": "web"}
			if tt.identity != "" {
				args["identity"] = tt.identity
			}
			if content, isError := callTool(t, url, "inspect_cpu", args); isError {
				t.Fatalf("call failed: %s", content)
			}
			if len(s.opts) != 1 {
				t.Fatalf("built %d executors, want 1", len(s.opts))
			}
			if got := s.opts[0].IdentityFile; got != tt.want {
				t.Errorf("identity file = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServeRefusesAnUnregisteredIdentityAlias(t *testing.T) {
	dir := t.TempDir()
	def := keyFile(t, dir, "id_ed25519", 0o600)
	withConfig(t, "")
	s := newServeApp(t)
	url := serveHost(t, s, serveOptions{listen: "127.0.0.1:0", identities: []string{"default=" + def}})

	for _, identity := range []string{"customer-a", def, "~/.ssh/id_ed25519"} {
		content, isError := callTool(t, url, "inspect_cpu", map[string]any{"host": "web", "identity": identity})
		if !isError {
			t.Errorf("identity %q was accepted: %s", identity, content)
			continue
		}
		if strings.Contains(content, dir) {
			t.Errorf("identity %q: the refusal leaked a key path: %s", identity, content)
		}
		var got result.Error
		if err := json.Unmarshal([]byte(content), &got); err != nil {
			t.Fatalf("error content is not focal's schema: %v (%q)", err, content)
		}
		if got.Code != "unknown_identity" {
			t.Errorf("identity %q: code = %q, want unknown_identity", identity, got.Code)
		}
	}
	if len(s.recorder.Calls()) != 0 {
		t.Errorf("an unresolved identity still reached the executor: %+v", s.recorder.Calls())
	}
}

func TestServeDefaultUserAppliesToEveryCall(t *testing.T) {
	withConfig(t, "")
	s := newServeApp(t)
	url := serveHost(t, s, serveOptions{listen: "127.0.0.1:0", user: "ec2-user"})

	if content, isError := callTool(t, url, "inspect_cpu", map[string]any{"host": "web"}); isError {
		t.Fatalf("call failed: %s", content)
	}
	calls := s.recorder.Calls()
	if len(calls) == 0 {
		t.Fatal("nothing reached the executor")
	}
	for _, call := range calls {
		if got := call.Target.String(); got != "ec2-user@web" {
			t.Errorf("target = %q, want ec2-user@web", got)
		}
	}
}

func TestServeRejectsAMisconfiguredIdentityAtStartup(t *testing.T) {
	dir := t.TempDir()
	s := newServeApp(t)

	_, err := s.app.serveHandler(serveOptions{
		listen:     "127.0.0.1:0",
		identities: []string{"default=" + keyFile(t, dir, "id", 0o644)},
	})
	if err == nil {
		t.Fatal("a world-readable key was registered")
	}
	if err.err.Code != "insecure_identity_file_permissions" {
		t.Errorf("code = %q, want insecure_identity_file_permissions", err.err.Code)
	}
}

// TestServeOnStopsWithItsContext keeps focal serve from outliving the process
// that started it: cancelling the context shuts the server down and returns.
func TestServeOnStopsWithItsContext(t *testing.T) {
	withConfig(t, "")
	s := newServeApp(t)
	handler, herr := s.app.serveHandler(serveOptions{listen: "127.0.0.1:0"})
	if herr != nil {
		t.Fatalf("serveHandler: %v", herr.err)
	}
	ln, lerr := s.app.listen(serveOptions{listen: "127.0.0.1:0"})
	if lerr != nil {
		t.Fatalf("listen: %v", lerr.err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *cliError, 1)
	go func() { done <- s.app.serveOn(ctx, ln, handler) }()

	// The listener is bound, so a connection proves the server is up.
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	cancel()
	if err := <-done; err != nil {
		t.Errorf("serveOn returned %v after a clean shutdown", err.err)
	}
}
