package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mcpRun is one focal mcp invocation driven in-process over pipes: the test
// writes the client's side of stdin and reads stdout line by line, and code
// carries the exit code once the invocation returns.
type mcpRun struct {
	in    *io.PipeWriter
	lines <-chan string
	code  chan int
}

// startMCP runs `focal <args>` through the entry every command takes, with
// pipes for stdin and stdout and the serve test app's Recorder in place of
// ssh(1).
func startMCP(t *testing.T, ctx context.Context, s *serveApp, args ...string) *mcpRun {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s.app.stdin = inR
	s.app.stdout = outW

	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(outR)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	r := &mcpRun{in: inW, lines: lines, code: make(chan int, 1)}
	go func() {
		r.code <- s.app.runContext(ctx, args)
		// What the process exiting would do to its stdout.
		_ = outW.Close()
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outR.Close()
	})
	return r
}

func (r *mcpRun) send(t *testing.T, msg map[string]any) {
	t.Helper()
	line, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// A pipe write waits for a reader; if focal is not reading, the test
	// says so instead of hanging.
	written := make(chan error, 1)
	go func() {
		_, err := r.in.Write(append(line, '\n'))
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("write stdin: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("focal mcp did not read its stdin within 5s")
	}
}

func (r *mcpRun) next(t *testing.T) string {
	t.Helper()
	select {
	case line, ok := <-r.lines:
		if !ok {
			t.Fatal("stdout ended before the expected message")
		}
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("no message on stdout within 5s")
		return ""
	}
}

func (r *mcpRun) exitCode(t *testing.T) int {
	t.Helper()
	select {
	case code := <-r.code:
		return code
	case <-time.After(5 * time.Second):
		t.Fatal("focal mcp did not exit within 5s")
		return -1
	}
}

// metaRequest is one 2026-07-28 request: its own _meta in place of an
// initialize handshake.
func metaRequest(id int, method string, params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "focal-test", "version": "0"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
}

// runMCPWithInput runs `focal <args>` to completion with stdin holding input
// and nothing more, and reports what it wrote.
func runMCPWithInput(t *testing.T, s *serveApp, input string, args ...string) (code int, stdout string) {
	t.Helper()
	var out bytes.Buffer
	s.app.stdin = strings.NewReader(input)
	s.app.stdout = &out
	done := make(chan int, 1)
	go func() { done <- s.app.runContext(context.Background(), args) }()
	select {
	case code = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("focal %s did not exit within 10s", strings.Join(args, " "))
	}
	return code, out.String()
}

// TestMCPIsReachableAsASubcommand pins the wiring: `focal mcp` is the stdio
// subcommand, not HOST=mcp with a missing operation.
func TestMCPIsReachableAsASubcommand(t *testing.T) {
	res := runCLI(t, "mcp", "--help")
	if res.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%s", res.code, exitOK, res.stderr)
	}
	if !strings.Contains(res.stdout, "focal mcp serves") {
		t.Fatalf("stdout = %q, want the mcp command's help text", res.stdout)
	}
}

// TestMCPTakesTheSessionFlagsAndNoListenerFlags fixes what focal mcp can be
// told: which keys to offer, who to log in as and which configuration to read —
// the same three flags focal serve has for them — and nothing about where to
// listen, because it does not listen.
func TestMCPTakesTheSessionFlagsAndNoListenerFlags(t *testing.T) {
	cmd := newMCPCommand(newServeApp(t).app)
	for _, name := range []string{"identity", "user", "config"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("focal mcp has no --%s flag", name)
		}
	}
	for _, name := range []string{"listen", "allow-unauthenticated-listen", "upstream-token"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Errorf("focal mcp has a --%s flag, which means nothing without a listener", name)
		}
	}
}

// TestMCPRefusalsBeforeServingWriteNothingToStdout is the half of "stdout is
// JSON-RPC only" that holds before the session starts: a refused start writes
// focal's error JSON to stderr, and stdout stays empty, so a client never reads
// a line of it as a protocol message.
func TestMCPRefusalsBeforeServingWriteNothingToStdout(t *testing.T) {
	keyDir := t.TempDir()
	badConfig := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(badConfig, []byte("operations:\n  kernel: [not-a-map]\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	tests := []struct {
		name     string
		args     []string
		code     int
		errCode  string
		contains string
	}{
		{"an unknown flag", []string{"mcp", "--bogus"}, exitUsage, "invalid_arguments", "unknown flag: --bogus"},
		{"a listener flag", []string{"mcp", "--listen", "127.0.0.1:0"}, exitUsage, "invalid_arguments", "unknown flag: --listen"},
		{"an argument", []string{"mcp", "extra"}, exitUsage, "invalid_arguments", "extra"},
		{"a missing config file", []string{"mcp", "--config", filepath.Join(t.TempDir(), "typo.yaml")}, exitRejected, "config_not_found", ""},
		{"an invalid config file", []string{"mcp", "--config", badConfig}, exitRejected, "invalid_yaml", ""},
		{"a malformed identity", []string{"mcp", "--identity", "no-equals-sign"}, exitUsage, "invalid_identity_registration", ""},
		{"a world-readable key", []string{"mcp", "--identity", "default=" + keyFile(t, keyDir, "id", 0o644)}, exitUsage, "insecure_identity_file_permissions", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, "")
			s := newServeApp(t)

			code, stdout := runMCPWithInput(t, s, "", tt.args...)

			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if code != tt.code {
				t.Errorf("exit code = %d, want %d; stderr=%s", code, tt.code, s.stderr.String())
			}
			got := decodeError(t, runResult{stderr: s.stderr.String(), code: code})
			if got.Code != tt.errCode {
				t.Errorf("code = %q, want %q", got.Code, tt.errCode)
			}
			if !strings.Contains(got.Message, tt.contains) {
				t.Errorf("message = %q, want it to mention %q", got.Message, tt.contains)
			}
		})
	}
}

// TestMCPServesOverStdinAndStdout is focal mcp end to end in-process: a tool
// call arrives on stdin, goes through the same runner focal serve uses — the
// registered identity, the default user — and its answer comes back on
// stdout. Closing stdin then ends the process with 0.
func TestMCPServesOverStdinAndStdout(t *testing.T) {
	withConfig(t, "")
	key := keyFile(t, t.TempDir(), "id_ed25519", 0o600)
	s := newServeApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := startMCP(t, ctx, s, "mcp", "--identity", "default="+key, "--user", "deploy")

	run.send(t, metaRequest(1, "tools/call", map[string]any{
		"name": "inspect_logs", "arguments": map[string]any{"host": "web", "service": "nginx"},
	}))
	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	line := run.next(t)
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("stdout line is not JSON: %v (%q)", err, line)
	}
	if resp.JSONRPC != "2.0" || resp.ID != 1 || resp.Error != nil || resp.Result.IsError {
		t.Fatalf("response = %s, want a successful tools/call result", line)
	}

	calls := s.recorder.Calls()
	if len(calls) != 1 {
		t.Fatalf("recorder saw %d calls, want 1", len(calls))
	}
	if got := calls[0].Target.String(); got != "deploy@web" {
		t.Errorf("target = %q, want deploy@web", got)
	}
	if got := calls[0].Command.Program(); got != "journalctl" {
		t.Errorf("program = %q, want journalctl", got)
	}
	if len(s.opts) != 1 || s.opts[0].IdentityFile != key {
		t.Errorf("executor options = %+v, want the key registered as default", s.opts)
	}

	_ = run.in.Close()
	if code := run.exitCode(t); code != exitOK {
		t.Errorf("exit code at the end of stdin = %d, want %d; stderr=%s", code, exitOK, s.stderr.String())
	}
	for line := range run.lines {
		t.Errorf("stdout carried more after the session ended: %q", line)
	}
}

// TestMCPExitsZeroAtTheEndOfInput is how every stdio client ends a server:
// closing its stdin. An empty stdin is that and nothing else.
func TestMCPExitsZeroAtTheEndOfInput(t *testing.T) {
	withConfig(t, "")
	s := newServeApp(t)

	code, stdout := runMCPWithInput(t, s, "", "mcp")

	if code != exitOK {
		t.Errorf("exit code = %d, want %d; stderr=%s", code, exitOK, s.stderr.String())
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if got := s.stderr.String(); got != "" {
		t.Errorf("stderr = %q, want nothing", got)
	}
}

// TestMCPExitsOneOnALineThatIsNotJSON is the session failing: the SDK stops at
// a line it cannot read, and focal says so on stderr in its own error schema
// and exits 1, writing nothing to stdout that is not a protocol message.
func TestMCPExitsOneOnALineThatIsNotJSON(t *testing.T) {
	withConfig(t, "")
	s := newServeApp(t)

	code, stdout := runMCPWithInput(t, s, "this is not json\n", "mcp")

	if code != exitRejected {
		t.Errorf("exit code = %d, want %d; stderr=%s", code, exitRejected, s.stderr.String())
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	got := decodeError(t, runResult{stderr: s.stderr.String(), code: code})
	if got.Code != "mcp_session_failed" {
		t.Errorf("code = %q, want mcp_session_failed", got.Code)
	}
}

// TestMCPExitsOneOnAMessageCutShortByTheEndOfInput keeps a truncated last
// message a failure rather than an ordinary end of input: the client closed
// stdin in the middle of something focal could not read.
func TestMCPExitsOneOnAMessageCutShortByTheEndOfInput(t *testing.T) {
	withConfig(t, "")
	s := newServeApp(t)

	code, stdout := runMCPWithInput(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"`, "mcp")

	if code != exitRejected {
		t.Errorf("exit code = %d, want %d; stderr=%s", code, exitRejected, s.stderr.String())
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if got := decodeError(t, runResult{stderr: s.stderr.String(), code: code}); got.Code != "mcp_session_failed" {
		t.Errorf("code = %q, want mcp_session_failed", got.Code)
	}
}

// TestMCPStopsWithItsContext is the signal path in-process: the context the
// invocation runs under ends while stdin is still open, and focal mcp exits 0.
func TestMCPStopsWithItsContext(t *testing.T) {
	withConfig(t, "")
	s := newServeApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	run := startMCP(t, ctx, s, "mcp")

	run.send(t, metaRequest(1, "tools/list", nil))
	_ = run.next(t)
	cancel()

	if code := run.exitCode(t); code != exitOK {
		t.Errorf("exit code after the context ended = %d, want %d; stderr=%s", code, exitOK, s.stderr.String())
	}
}

// TestMCPNoticesAnUpstreamTokenItDoesNotUse keeps an operator from believing
// the token guards anything here: focal mcp has no listener for it to guard,
// so it says, on stderr, that it is not used — without repeating it — and the
// variable is gone from the environment like for every other command.
func TestMCPNoticesAnUpstreamTokenItDoesNotUse(t *testing.T) {
	withConfig(t, "")
	t.Setenv(upstreamTokenEnv, testToken)
	s := newServeApp(t)

	code, stdout := runMCPWithInput(t, s, "", "mcp")

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitOK, s.stderr.String())
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	got := s.stderr.String()
	if !strings.Contains(got, upstreamTokenEnv) || !strings.Contains(got, "not use") {
		t.Errorf("stderr = %q, want a note that %s is not used by focal mcp", got, upstreamTokenEnv)
	}
	assertNoToken(t, got, testToken)
	if v, set := os.LookupEnv(upstreamTokenEnv); set || v != "" {
		t.Errorf("%s is still in the environment", upstreamTokenEnv)
	}
}

// TestMCPRefusalWithATokenSetIsOneErrorLine keeps a refused start's stderr in
// the shape every other command's has — one line of focal's error JSON — even
// with a token in the environment: the note about the token is for a session
// that starts, and this one does not.
func TestMCPRefusalWithATokenSetIsOneErrorLine(t *testing.T) {
	withConfig(t, "")
	t.Setenv(upstreamTokenEnv, testToken)
	s := newServeApp(t)

	code, stdout := runMCPWithInput(t, s, "", "mcp", "--config", filepath.Join(t.TempDir(), "typo.yaml"))

	if code != exitRejected {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitRejected, s.stderr.String())
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if got := decodeError(t, runResult{stderr: s.stderr.String(), code: code}); got.Code != "config_not_found" {
		t.Errorf("code = %q, want config_not_found", got.Code)
	}
}

// TestMCPSaysNothingAboutATokenThatIsNotSet keeps the note from becoming noise
// on every start.
func TestMCPSaysNothingAboutATokenThatIsNotSet(t *testing.T) {
	withConfig(t, "")
	t.Setenv(upstreamTokenEnv, "")
	s := newServeApp(t)

	if code, _ := runMCPWithInput(t, s, "", "mcp"); code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if got := s.stderr.String(); strings.Contains(got, upstreamTokenEnv) {
		t.Errorf("stderr = %q, want no note when no token is set", got)
	}
}

// TestCLIDoesNotImportTheMCPSDK keeps the protocol in internal/mcp: this
// package hands that one its configuration, a Runner and the process's
// streams, and has no business with JSON-RPC or the SDK's types itself.
func TestCLIDoesNotImportTheMCPSDK(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	var imports int
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, source, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", source, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: bad import %s", source, spec.Path.Value)
			}
			imports++
			if strings.HasPrefix(path, "github.com/modelcontextprotocol/") {
				t.Errorf("%s imports %s; internal/cli reaches MCP only through internal/mcp", source, path)
			}
		}
	}
	if imports == 0 {
		t.Fatal("the import walk found nothing to check")
	}
}
