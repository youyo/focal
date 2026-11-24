//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// stdioExitLimit is how soon focal mcp must be gone after its stdin closes or
// it is sent SIGTERM. A go-sdk client sends SIGTERM five seconds after closing
// stdin, so focal has to have exited before then for the client to see a
// clean stop.
const stdioExitLimit = 5 * time.Second

// TestStdioToolsAndCall drives `focal mcp` the way an MCP client does: the
// go-sdk client starts the built binary as its child process, lists the tools
// and calls them against the real container over ssh(1), then closes stdin.
// Close reports the child's exit status, so a nil error there is the exit
// code 0 a clean end of input has to produce.
func TestStdioToolsAndCall(t *testing.T) {
	h := requireHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cmd := exec.Command(h.focalBin, "mcp", "--config", h.configFile)
	cmd.Env = append([]string{}, h.env...)
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	client := sdk.NewClient(&sdk.Implementation{Name: "focal-e2e", Version: "0"}, nil)
	session, err := client.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect focal mcp: %v\nstderr: %s", err, stderr.String())
	}

	t.Run("tools/list", func(t *testing.T) {
		if got := listToolNames(t, ctx, session); !slices.Equal(got, expectedTools) {
			t.Errorf("tools = %v, want %v", got, expectedTools)
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
		if env := decodeEnvelope(t, text); env.Status != "ok" {
			t.Errorf("status = %q, want ok", env.Status)
		}
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
		if e := decodeError(t, toolText(t, res)); e.Kind != "validation" {
			t.Errorf("kind = %q, want validation", e.Kind)
		}
	})

	start := time.Now()
	if err := session.Close(); err != nil {
		t.Errorf("closing the session: %v (want focal mcp to exit 0 at the end of stdin)\nstderr: %s", err, stderr.String())
	}
	if took := time.Since(start); took >= stdioExitLimit {
		t.Errorf("focal mcp took %s to exit after stdin closed, want under %s", took, stdioExitLimit)
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// TestStdioStdoutCarriesOnlyJSONRPC writes newline-delimited JSON-RPC to the
// real binary by hand — the initialize handshake most clients use, a tool
// list, and a tool call that runs ssh(1) against the container — and reads
// stdout raw. Every line on it must be a JSON-RPC 2.0 message, before and
// after ssh(1) has run, and closing stdin must end the process with 0.
func TestStdioStdoutCarriesOnlyJSONRPC(t *testing.T) {
	h := requireHarness(t)

	for name, extraEnv := range map[string][]string{
		"no upstream token":     nil,
		"an upstream token set": {"FOCAL_UPSTREAM_TOKEN=" + upstreamToken},
	} {
		t.Run(name, func(t *testing.T) {
			p := h.startStdio(t, extraEnv, "mcp", "--config", h.configFile)

			p.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
				"protocolVersion": "2025-11-25",
				"capabilities":    map[string]any{},
				"clientInfo":      map[string]any{"name": "focal-e2e", "version": "0"},
			}})
			p.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
			p.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
			p.send(t, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
				"name": "inspect_system", "arguments": map[string]any{"host": targetHost},
			}})

			answers := p.awaitIDs(t, 60*time.Second, 1, 2, 3)
			var initialized struct {
				Result struct {
					ProtocolVersion string `json:"protocolVersion"`
				} `json:"result"`
			}
			if err := json.Unmarshal(answers[1], &initialized); err != nil || initialized.Result.ProtocolVersion == "" {
				t.Errorf("initialize answer carries no protocol version: %s", answers[1])
			}
			var call struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal(answers[3], &call); err != nil || call.Result.IsError || len(call.Result.Content) == 0 {
				t.Fatalf("tools/call answer is not a successful result: %s", answers[3])
			}
			if env := decodeEnvelope(t, call.Result.Content[0].Text); env.Status != "ok" {
				t.Errorf("status = %q, want ok", env.Status)
			}

			code, took := p.closeAndWait(t)
			if code != 0 {
				t.Errorf("exit code at the end of stdin = %d, want 0\nstderr: %s", code, p.stderr.String())
			}
			if took >= stdioExitLimit {
				t.Errorf("focal mcp took %s to exit after stdin closed, want under %s", took, stdioExitLimit)
			}
			p.assertStdoutIsJSONRPC(t)

			stderr := p.stderr.String()
			if strings.Contains(stderr, upstreamToken) {
				t.Errorf("stderr repeats the upstream token:\n%s", stderr)
			}
			if extraEnv != nil && !strings.Contains(stderr, "FOCAL_UPSTREAM_TOKEN") {
				t.Errorf("stderr does not say the upstream token is unused:\n%s", stderr)
			}
		})
	}
}

// TestStdioEndOfInputDuringACall closes stdin while a composite inspection is
// running against the container. The call is abandoned, and the end is still
// a clean one: exit 0, in time, with nothing but JSON-RPC on stdout.
func TestStdioEndOfInputDuringACall(t *testing.T) {
	h := requireHarness(t)
	p := h.startStdio(t, nil, "mcp", "--config", h.configFile)

	p.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "focal-e2e", "version": "0"},
	}})
	p.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	p.awaitIDs(t, 30*time.Second, 1)
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "inspect", "arguments": map[string]any{"host": targetHost},
	}})
	// inspect runs a few dozen ssh(1) processes and takes about half a
	// second against the container; closing stdin a moment after sending
	// it lands while they are running rather than before the call starts.
	time.Sleep(150 * time.Millisecond)

	code, took := p.closeAndWait(t)
	if code != 0 {
		t.Errorf("exit code = %d, want 0 when stdin closes under a running call\nstderr: %s", code, p.stderr.String())
	}
	if took >= stdioExitLimit {
		t.Errorf("focal mcp took %s to exit after stdin closed, want under %s", took, stdioExitLimit)
	}
	p.assertStdoutIsJSONRPC(t)
	// The ssh(1) processes the call started must not outlive focal. Every
	// one of them runs through the harness's wrapper, whose arguments name
	// the suite's own known_hosts file, so a survivor can be found by it.
	if survivors := sshProcessesOf(t, h); len(survivors) != 0 {
		t.Errorf("ssh processes left running after focal mcp exited:\n%s", strings.Join(survivors, "\n"))
	}
	if p.answered(2) {
		t.Log("the inspect call finished before stdin closed; this run did not exercise an abandoned call")
	}
}

// TestStdioSIGTERM is the other ordinary way a client stops a stdio server:
// SIGTERM with stdin still open. focal mcp exits 0, in time.
func TestStdioSIGTERM(t *testing.T) {
	h := requireHarness(t)
	p := h.startStdio(t, nil, "mcp", "--config", h.configFile)

	p.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "focal-e2e", "version": "0"},
	}})
	p.awaitIDs(t, 30*time.Second, 1)

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	code, took := p.wait(t)
	if code != 0 {
		t.Errorf("exit code after SIGTERM = %d, want 0\nstderr: %s", code, p.stderr.String())
	}
	if took >= stdioExitLimit {
		t.Errorf("focal mcp took %s to exit after SIGTERM, want under %s", took, stdioExitLimit)
	}
	p.assertStdoutIsJSONRPC(t)
}

// TestStdioLineThatIsNotJSON is the session failing: input the SDK cannot
// read ends it. focal mcp says so on stderr in its own error schema and exits
// 1, and stdout still carries nothing that is not JSON-RPC — whether stdin
// stays open after the bad input or the client closes it straight away, and
// whether the input is a line that is not JSON or a last message cut short.
func TestStdioLineThatIsNotJSON(t *testing.T) {
	h := requireHarness(t)

	for _, tt := range []struct {
		name      string
		input     string
		thenClose bool
	}{
		{"a line that is not JSON, stdin left open", "this is not json\n", false},
		{"a line that is not JSON, stdin closed at once", "this is not json\n", true},
		{"a last message cut short by the end of stdin", `{"jsonrpc":"2.0","id":1,"method":"tools/list"`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := h.startStdio(t, nil, "mcp", "--config", h.configFile)

			if _, err := io.WriteString(p.stdin, tt.input); err != nil {
				t.Fatalf("write: %v", err)
			}
			if tt.thenClose {
				if err := p.stdin.Close(); err != nil {
					t.Fatalf("close stdin: %v", err)
				}
			}
			code, _ := p.wait(t)
			if code != 1 {
				t.Errorf("exit code = %d, want 1\nstderr: %s", code, p.stderr.String())
			}
			if e := decodeError(t, p.stderr.String()); e.Code != "mcp_session_failed" {
				t.Errorf("code = %q, want mcp_session_failed", e.Code)
			}
			p.assertStdoutIsJSONRPC(t)
		})
	}
}

// TestStdioRefusalsLeaveStdoutEmpty covers focal mcp refusing to start: a
// usage error or a bad configuration is reported on stderr in focal's error
// schema, and stdout — which a client reads as protocol — stays empty.
func TestStdioRefusalsLeaveStdoutEmpty(t *testing.T) {
	h := requireHarness(t)

	badConfig := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(badConfig, []byte("operations:\n  kernel: [not-a-map]\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	tests := []struct {
		name    string
		args    []string
		code    int
		errCode string
	}{
		{"unknown flag", []string{"mcp", "--bogus"}, 2, "invalid_arguments"},
		{"listener flag", []string{"mcp", "--listen", "127.0.0.1:0"}, 2, "invalid_arguments"},
		{"invalid config file", []string{"mcp", "--config", badConfig}, 1, "invalid_yaml"},
		{"missing config file", []string{"mcp", "--config", filepath.Join(t.TempDir(), "typo.yaml")}, 1, "config_not_found"},
		{"malformed identity", []string{"mcp", "--config", h.configFile, "--identity", "no-equals-sign"}, 2, "invalid_identity_registration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			stdout, stderr, code := h.run(ctx, nil, tt.args...)
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if code != tt.code {
				t.Errorf("exit code = %d, want %d\nstderr: %s", code, tt.code, stderr)
			}
			if e := decodeError(t, stderr); e.Code != tt.errCode {
				t.Errorf("code = %q, want %q", e.Code, tt.errCode)
			}
		})
	}
}

// stdioProcess is one focal mcp child process with its stdin held open by the
// test, its stdout read line by line and its stderr kept for diagnostics.
type stdioProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *lockedBuffer

	mu     sync.Mutex
	lines  []string
	notify chan struct{}
	eof    chan struct{}
	done   chan struct{}
}

// startStdio starts the focal binary with args, the harness environment plus
// extraEnv, and pipes for stdin and stdout.
func (h *harness) startStdio(t *testing.T, extraEnv []string, args ...string) *stdioProcess {
	t.Helper()
	cmd := exec.Command(h.focalBin, args...)
	cmd.Env = append(append([]string{}, h.env...), extraEnv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	p := &stdioProcess{
		cmd:    cmd,
		stdin:  stdin,
		stderr: &lockedBuffer{},
		notify: make(chan struct{}, 1),
		eof:    make(chan struct{}),
		done:   make(chan struct{}),
	}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start focal mcp: %v", err)
	}
	go func() {
		defer close(p.eof)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			p.mu.Lock()
			p.lines = append(p.lines, scanner.Text())
			p.mu.Unlock()
			select {
			case p.notify <- struct{}{}:
			default:
			}
		}
	}()
	go func() {
		// Wait only after stdout has been read to its end: Wait closes the
		// pipe, and reading must not lose what was already written.
		<-p.eof
		_ = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

func (p *stdioProcess) send(t *testing.T, msg map[string]any) {
	t.Helper()
	line, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		t.Fatalf("write stdin: %v\nstderr: %s", err, p.stderr.String())
	}
}

// awaitIDs waits until stdout has carried a response for every id and returns
// them by id.
func (p *stdioProcess) awaitIDs(t *testing.T, limit time.Duration, ids ...int) map[int]json.RawMessage {
	t.Helper()
	deadline := time.After(limit)
	for {
		found := map[int]json.RawMessage{}
		p.mu.Lock()
		for _, line := range p.lines {
			var msg struct {
				ID *int `json:"id"`
			}
			if json.Unmarshal([]byte(line), &msg) == nil && msg.ID != nil && slices.Contains(ids, *msg.ID) {
				found[*msg.ID] = json.RawMessage(line)
			}
		}
		p.mu.Unlock()
		if len(found) == len(ids) {
			return found
		}
		select {
		case <-p.notify:
		case <-p.eof:
			t.Fatalf("stdout ended before responses %v arrived (got %d)\nstderr: %s", ids, len(found), p.stderr.String())
		case <-deadline:
			t.Fatalf("responses %v did not all arrive within %s\nstderr: %s", ids, limit, p.stderr.String())
		}
	}
}

// closeAndWait closes stdin, as a client ending the session does, and waits
// for the process, returning its exit code and how long it took to exit.
func (p *stdioProcess) closeAndWait(t *testing.T) (int, time.Duration) {
	t.Helper()
	if err := p.stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	return p.wait(t)
}

// wait waits for the process to exit, failing the test if it is still running
// well past the limit, and returns its exit code and how long that took.
func (p *stdioProcess) wait(t *testing.T) (int, time.Duration) {
	t.Helper()
	start := time.Now()
	select {
	case <-p.done:
	case <-time.After(3 * stdioExitLimit):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Fatalf("focal mcp was still running %s later\nstderr: %s", 3*stdioExitLimit, p.stderr.String())
	}
	return p.cmd.ProcessState.ExitCode(), time.Since(start)
}

// sshProcessesOf lists the running processes whose command line names the
// harness's known_hosts file — the ssh(1) processes the suite's focal started.
func sshProcessesOf(t *testing.T, h *harness) []string {
	t.Helper()
	knownHosts := filepath.Join(filepath.Dir(h.configFile), "known_hosts")
	out, err := exec.Command("pgrep", "-fl", knownHosts).Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil // pgrep found nothing
	}
	if err != nil {
		t.Fatalf("pgrep: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// answered reports whether stdout has carried a response for id.
func (p *stdioProcess) answered(id int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, line := range p.lines {
		var msg struct {
			ID *int `json:"id"`
		}
		if json.Unmarshal([]byte(line), &msg) == nil && msg.ID != nil && *msg.ID == id {
			return true
		}
	}
	return false
}

// assertStdoutIsJSONRPC checks every line the process wrote to stdout. It is
// called after the process has exited, so the lines are all of them.
func (p *stdioProcess) assertStdoutIsJSONRPC(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, line := range p.lines {
		if err := checkJSONRPCLine(line); err != nil {
			t.Errorf("stdout line is not a JSON-RPC 2.0 message: %v\n%q", err, line)
		}
	}
}

// checkJSONRPCLine reports whether line is one JSON-RPC 2.0 message: a
// request, a notification or a response.
func checkJSONRPCLine(line string) error {
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	dec := json.NewDecoder(strings.NewReader(line))
	if err := dec.Decode(&msg); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the message")
	}
	if msg.JSONRPC != "2.0" {
		return fmt.Errorf("jsonrpc = %q", msg.JSONRPC)
	}
	if msg.Method == "" && msg.Result == nil && msg.Error == nil {
		return errors.New("neither a request, a notification nor a response")
	}
	return nil
}

// lockedBuffer is a bytes.Buffer safe to write from the process's stderr
// copier while a test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
