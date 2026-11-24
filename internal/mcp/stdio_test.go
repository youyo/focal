package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/youyo/focal/internal/config"
	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// stdioTestMaxInFlight is the ceiling these tests run ServeStdio with. It is
// the same figure focal mcp is started with, so the concurrency test exercises
// the number that ships.
const stdioTestMaxInFlight = 8

// stdioPeer is the client's end of one ServeStdio: what it writes is the
// server's stdin, what it reads is the server's stdout, and done carries what
// ServeStdio returned.
type stdioPeer struct {
	in   *io.PipeWriter
	out  *io.PipeReader
	done chan error
}

// startStdio runs ServeStdio over a pair of pipes, the way focal mcp runs it
// over the process's stdin and stdout.
func startStdio(t *testing.T, ctx context.Context, cfg config.Config, runner Runner) *stdioPeer {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &stdioPeer{in: inW, out: outR, done: make(chan error, 1)}
	go func() {
		p.done <- ServeStdio(ctx, cfg, runner, Options{Version: "test", MaxInFlight: stdioTestMaxInFlight}, inR, outW)
		// The process exiting is what closes a real stdout; this is the
		// same moment for the pipe standing in for it.
		_ = outW.Close()
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outR.Close()
	})
	return p
}

// wait returns what ServeStdio returned, failing the test if it has not
// returned within limit.
func (p *stdioPeer) wait(t *testing.T, limit time.Duration) error {
	t.Helper()
	select {
	case err := <-p.done:
		return err
	case <-time.After(limit):
		t.Fatalf("ServeStdio had not returned %s after its input ended", limit)
		return nil
	}
}

// connect opens an MCP session to the peer with the SDK's own client, which
// speaks the initialize handshake the way most clients do.
func (p *stdioPeer) connect(t *testing.T, ctx context.Context) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "focal-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &sdk.IOTransport{Reader: p.out, Writer: p.in}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return session
}

// rawLines reads newline-delimited messages off the peer's stdout in the
// background, so a test can send raw JSON-RPC and wait for one answer at a
// time.
func (p *stdioPeer) rawLines() <-chan string {
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(p.out)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return lines
}

func (p *stdioPeer) send(t *testing.T, msg map[string]any) {
	t.Helper()
	line, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := p.in.Write(append(line, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// nextLine is the next message off stdout, failing the test if none arrives.
func nextLine(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case line, ok := <-lines:
		if !ok {
			t.Fatal("stdout ended before the expected message")
		}
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("no message on stdout within 5s")
		return ""
	}
}

// metaParams is the per-request _meta triple a 2026-07-28 client sends in
// place of an initialize handshake.
func metaParams(extra map[string]any) map[string]any {
	params := map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    protocolVersion,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "focal-test", "version": "0"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	}
	for k, v := range extra {
		params[k] = v
	}
	return params
}

// gatedRunner is a Runner whose calls do not finish until the test says so. It
// records how many calls were inside Run at once, which is what the
// concurrency ceiling is about.
type gatedRunner struct {
	release chan struct{}
	started chan string
	// cancelled receives the target of every call whose context ended
	// while it waited.
	cancelled chan string
	// ignoreCancel makes Run keep waiting for release even after its
	// context ends, the way a runner that cannot be interrupted would.
	ignoreCancel bool

	releaseOnce sync.Once

	mu      sync.Mutex
	running int
	peak    int
	calls   []string
}

func newGatedRunner() *gatedRunner {
	return &gatedRunner{release: make(chan struct{}), started: make(chan string, 64), cancelled: make(chan string, 64)}
}

func (r *gatedRunner) Run(ctx context.Context, op operation.Operation, target vo.Target, _ string, _ vo.Transport) (result.Envelope, *result.Error) {
	r.mu.Lock()
	r.running++
	r.peak = max(r.peak, r.running)
	r.calls = append(r.calls, target.String())
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running--
		r.mu.Unlock()
	}()

	r.started <- target.String()
	if r.ignoreCancel {
		<-r.release
	} else {
		select {
		case <-r.release:
		case <-ctx.Done():
			r.cancelled <- target.String()
			return result.Envelope{}, result.ExecutionError("cancelled", ctx.Err().Error())
		}
	}
	return result.Envelope{Operation: op.Name(), Host: target.String(), Status: result.StatusOK}, nil
}

// releaseAll lets every call, running or yet to run, finish. It may be called
// more than once, so a test can both defer it and call it on its way.
func (r *gatedRunner) releaseAll() {
	r.releaseOnce.Do(func() { close(r.release) })
}

func (r *gatedRunner) snapshot() (peak int, calls []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak, slices.Clone(r.calls)
}

func waitStarted(t *testing.T, r *gatedRunner, n int) {
	t.Helper()
	for range n {
		select {
		case <-r.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("fewer than %d calls reached the runner", n)
		}
	}
}

// TestServeStdioListsAndCallsOverInitialize is the ordinary client: the
// initialize handshake, then tools/list and tools/call, over the same tool
// registration focal serve uses.
func TestServeStdioListsAndCallsOverInitialize(t *testing.T) {
	cfg := withConfig(t, "")
	runner := &recordingRunner{envelope: result.Envelope{Operation: "system", Host: "web", Status: result.StatusOK}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	peer := startStdio(t, ctx, cfg, runner)
	session := peer.connect(t, ctx)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if want := listedOverHTTP(t, ""); !slices.Equal(names, want) {
		t.Errorf("stdio tools = %v, want the same set focal serve lists: %v", names, want)
	}

	res, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name:      "inspect_system",
		Arguments: map[string]any{"host": "web"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("inspect_system reported an error: %+v", res.Content)
	}
	if len(runner.calls) != 1 || runner.calls[0].operation != "system" {
		t.Fatalf("runner calls = %+v, want one system call", runner.calls)
	}

	// Closing the session closes the server's stdin, which is how a client
	// ends a stdio server; that is a clean end, not a failure.
	if err := session.Close(); err != nil {
		t.Fatalf("session.Close: %v", err)
	}
	if err := peer.wait(t, 5*time.Second); err != nil {
		t.Errorf("ServeStdio returned %v at the end of its input, want nil", err)
	}
}

// listedOverHTTP is the sorted tool list focal serve offers for the same
// configuration body, the reference the stdio entry must agree with.
func listedOverHTTP(t *testing.T, body string) []string {
	t.Helper()
	url, _ := serverFor(t, body, Options{})
	names := listToolNames(t, url)
	slices.Sort(names)
	return names
}

// TestServeStdioAnswersPerRequestMeta covers the 2026-07-28 way of talking:
// no initialize at all, every request carrying its own _meta. focal leaves the
// choice of protocol revision to the SDK, so both ways have to work.
func TestServeStdioAnswersPerRequestMeta(t *testing.T) {
	cfg := withConfig(t, "")
	runner := &recordingRunner{envelope: result.Envelope{Operation: "system", Host: "web", Status: result.StatusOK}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	peer := startStdio(t, ctx, cfg, runner)
	lines := peer.rawLines()

	peer.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": metaParams(nil)})
	var list struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(nextLine(t, lines)), &list); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if list.Error != nil || len(list.Result.Tools) == 0 {
		t.Fatalf("tools/list without initialize: error=%s tools=%d", list.Error, len(list.Result.Tools))
	}

	peer.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": metaParams(map[string]any{
		"name": "inspect_system", "arguments": map[string]any{"host": "web"},
	})})
	var call struct {
		ID     int `json:"id"`
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(nextLine(t, lines)), &call); err != nil {
		t.Fatalf("decode tools/call: %v", err)
	}
	if call.ID != 2 || call.Error != nil || call.Result.IsError {
		t.Fatalf("tools/call without initialize: %+v", call)
	}
	if len(runner.calls) != 1 {
		t.Errorf("runner saw %d calls, want 1", len(runner.calls))
	}

	_ = peer.in.Close()
	if err := peer.wait(t, 5*time.Second); err != nil {
		t.Errorf("ServeStdio returned %v at the end of its input, want nil", err)
	}
}

// TestServeStdioEndOfInputWithACallInFlight is the client closing stdin while
// a call is still running. The call is cancelled, and the end is as clean as
// an idle one: the client did exactly what a stdio client is meant to do.
func TestServeStdioEndOfInputWithACallInFlight(t *testing.T) {
	cfg := withConfig(t, "")
	runner := newGatedRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	peer := startStdio(t, ctx, cfg, runner)
	_ = peer.rawLines()

	peer.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": metaParams(map[string]any{
		"name": "inspect_system", "arguments": map[string]any{"host": "web"},
	})})
	waitStarted(t, runner, 1)

	_ = peer.in.Close()
	// The running call has to be told to stop, not merely abandoned: it
	// is what holds the ssh(1) processes.
	select {
	case <-runner.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("the running call was not cancelled when stdin closed")
	}
	if err := peer.wait(t, 5*time.Second); err != nil {
		t.Errorf("ServeStdio returned %v when stdin closed under a running call, want nil", err)
	}
}

// TestServeStdioDoesNotWaitForeverOnARunnerThatIgnoresCancellation bounds how
// long the end of input may take. A client closes stdin and, a few seconds
// later, sends SIGTERM; focal has to be gone before then, whatever is still
// running.
func TestServeStdioDoesNotWaitForeverOnARunnerThatIgnoresCancellation(t *testing.T) {
	setShutdownGrace(t, 200*time.Millisecond)
	cfg := withConfig(t, "")
	runner := newGatedRunner()
	runner.ignoreCancel = true
	t.Cleanup(runner.releaseAll)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	peer := startStdio(t, ctx, cfg, runner)
	_ = peer.rawLines()

	peer.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": metaParams(map[string]any{
		"name": "inspect_system", "arguments": map[string]any{"host": "web"},
	})})
	waitStarted(t, runner, 1)

	start := time.Now()
	_ = peer.in.Close()
	if err := peer.wait(t, 3*time.Second); err != nil {
		t.Errorf("ServeStdio returned %v, want nil: the input ended, which is a clean end", err)
	}
	if took := time.Since(start); took < 150*time.Millisecond {
		t.Errorf("ServeStdio returned after %s, before the grace period was spent on the running call", took)
	}
}

// TestServeStdioShutdownGraceIsUnderFiveSeconds pins the ceiling itself: a
// go-sdk client waits five seconds after closing stdin before it sends
// SIGTERM, so focal must have given up waiting by then.
func TestServeStdioShutdownGraceIsUnderFiveSeconds(t *testing.T) {
	if stdioShutdownGrace <= 0 || stdioShutdownGrace >= 5*time.Second {
		t.Errorf("stdioShutdownGrace = %s, want more than zero and less than 5s", stdioShutdownGrace)
	}
}

// TestServeStdioStopsWithItsContext is the signal path: the context ends, the
// running call is told to stop rather than waited out, ServeStdio returns, and
// that too is a clean end.
func TestServeStdioStopsWithItsContext(t *testing.T) {
	// A grace period longer than the wait below: returning in time has to
	// come from the call being stopped, not from giving up on it.
	setShutdownGrace(t, 4*time.Second)
	cfg := withConfig(t, "")
	runner := newGatedRunner()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer := startStdio(t, ctx, cfg, runner)
	_ = peer.rawLines()

	peer.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": metaParams(map[string]any{
		"name": "inspect_system", "arguments": map[string]any{"host": "web"},
	})})
	waitStarted(t, runner, 1)

	cancel()
	select {
	case <-runner.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("the running call was not cancelled when ServeStdio's context ended")
	}
	if err := peer.wait(t, 2*time.Second); err != nil {
		t.Errorf("ServeStdio returned %v when its context ended, want nil", err)
	}
}

// TestServeStdioFailsOnALineThatIsNotJSON is the one way a session ends badly:
// the SDK stops reading at a line it cannot parse, and the caller has to be
// told, so it can exit non-zero.
func TestServeStdioFailsOnALineThatIsNotJSON(t *testing.T) {
	cfg := withConfig(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	peer := startStdio(t, ctx, cfg, &recordingRunner{})
	_ = peer.rawLines()

	if _, err := peer.in.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := peer.wait(t, 5*time.Second); err == nil {
		t.Error("ServeStdio returned nil after a line that is not JSON, want an error")
	}
}

// TestServeStdioFailsOnMalformedInputFollowedByEOF is a malformed line the
// client closes stdin right after, and a last message cut off by the end of
// input. Both are input the SDK could not read, which is a failed session
// however soon the end of input followed it.
func TestServeStdioFailsOnMalformedInputFollowedByEOF(t *testing.T) {
	for name, input := range map[string]string{
		"a line that is not JSON":                 "this is not json\n",
		"a line that is not JSON with no newline": "garbage",
		"a truncated last message":                `{"jsonrpc":"2.0","id":1,"method":"tools/list"`,
		"a valid message, then a truncated one":   `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n" + `{"jsonrpc":`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := withConfig(t, "")
			for range 20 {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := ServeStdio(ctx, cfg, &recordingRunner{},
					Options{MaxInFlight: stdioTestMaxInFlight}, strings.NewReader(input), io.Discard)
				cancel()
				if err == nil {
					t.Fatalf("ServeStdio returned nil for %q followed by EOF, want an error", input)
				}
			}
		})
	}
}

// TestServeStdioIdleEndOfInputIsClean pins the other side of the same rule:
// complete messages and then EOF, with nothing running, is the ordinary end.
func TestServeStdioIdleEndOfInputIsClean(t *testing.T) {
	cfg := withConfig(t, "")
	for _, input := range []string{"", "\n", `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"} {
		err := ServeStdio(context.Background(), cfg, &recordingRunner{},
			Options{MaxInFlight: stdioTestMaxInFlight}, strings.NewReader(input), io.Discard)
		if err != nil {
			t.Errorf("ServeStdio(%q then EOF) = %v, want nil", input, err)
		}
	}
}

// TestServeStdioStopsPromptlyOnItsContextWithStdinOpen is the signal arriving
// while nothing runs and stdin stays open: ServeStdio returns at once rather
// than spending its grace period, even though the read of stdin it started
// cannot itself be interrupted.
func TestServeStdioStopsPromptlyOnItsContextWithStdinOpen(t *testing.T) {
	setShutdownGrace(t, 4*time.Second)
	cfg := withConfig(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer := startStdio(t, ctx, cfg, &recordingRunner{})
	lines := peer.rawLines()
	peer.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": metaParams(nil)})
	_ = nextLine(t, lines)

	start := time.Now()
	cancel()
	if err := peer.wait(t, 3*time.Second); err != nil {
		t.Errorf("ServeStdio returned %v when its context ended, want nil", err)
	}
	if took := time.Since(start); took >= time.Second {
		t.Errorf("ServeStdio took %s to return with stdin open, want well under its grace period", took)
	}
}

// TestSessionEndKeepsAFailureASignalFollowed keeps a failed session failed when
// a signal happens to arrive right after: only the cancellation the signal
// caused is a clean end, not every error that comes back once it has arrived.
func TestSessionEndKeepsAFailureASignalFollowed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failure := errors.New("invalid character 'h' in literal true (expecting 'r')")
	if err := sessionEnd(ctx, failure); err == nil {
		t.Error("a failed session was reported clean because the context had also ended")
	}
	if err := sessionEnd(ctx, context.Canceled); err != nil {
		t.Errorf("the cancellation the context caused = %v, want nil", err)
	}
	if err := sessionEnd(context.Background(), context.Canceled); err == nil {
		t.Error("a cancellation the caller's context did not cause was reported clean")
	}
}

// TestLimitToolCallsDoesNotStartACallAlreadyCancelled covers the free-slot
// case of the ceiling: a call whose context has already ended is not started
// just because a slot happened to be free when it arrived.
func TestLimitToolCallsDoesNotStartACallAlreadyCancelled(t *testing.T) {
	ran := 0
	handler := limitToolCalls(stdioTestMaxInFlight)(func(context.Context, string, sdk.Request) (sdk.Result, error) {
		ran++
		return &sdk.CallToolResult{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 200 {
		if _, err := handler(ctx, methodCallTool, nil); err == nil {
			t.Fatal("a cancelled call was answered without an error")
		}
	}
	if ran != 0 {
		t.Errorf("%d cancelled calls were started", ran)
	}
}

// TestServeStdioWritesOnlyJSONRPCToItsOutput is the property focal mcp rests
// on: every line on stdout is a JSON-RPC 2.0 message, including around a
// tools/call and a refused one.
func TestServeStdioWritesOnlyJSONRPCToItsOutput(t *testing.T) {
	cfg := withConfig(t, "")
	runner := &recordingRunner{envelope: result.Envelope{Operation: "system", Host: "web", Status: result.StatusOK}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	peer := startStdio(t, ctx, cfg, runner)
	lines := peer.rawLines()

	peer.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "focal-test", "version": "0"},
	}})
	peer.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}})
	peer.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "inspect_system", "arguments": map[string]any{"host": "web"},
	}})
	peer.send(t, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
		"name": "inspect_service", "arguments": map[string]any{"host": "web", "service": "nginx;id"},
	}})
	got := []string{nextLine(t, lines), nextLine(t, lines), nextLine(t, lines)}
	_ = peer.in.Close()
	if err := peer.wait(t, 5*time.Second); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	for line := range lines {
		got = append(got, line)
	}

	for _, line := range got {
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Errorf("stdout line is not JSON: %v (%q)", err, line)
			continue
		}
		if msg.JSONRPC != "2.0" || (msg.ID == nil && msg.Method == "") {
			t.Errorf("stdout line is not a JSON-RPC 2.0 message: %q", line)
		}
	}
}

// TestServeStdioCapsConcurrentToolCalls is the ceiling focal serve puts on
// HTTP requests, applied to the one connection stdio has: no more tool calls
// reach the runner at once than the ceiling allows, the rest wait, and a
// tools/list is not held up behind them.
func TestServeStdioCapsConcurrentToolCalls(t *testing.T) {
	cfg := withConfig(t, "")
	runner := newGatedRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	peer := startStdio(t, ctx, cfg, runner)
	session := peer.connect(t, ctx)
	defer session.Close()
	// Deferred after the session so it runs first: a session cannot close
	// cleanly while its calls are still held.
	defer runner.releaseAll()

	const extra = 2
	var wg sync.WaitGroup
	errs := make(chan error, stdioTestMaxInFlight+extra)
	for i := range stdioTestMaxInFlight + extra {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := session.CallTool(ctx, &sdk.CallToolParams{
				Name:      "inspect_system",
				Arguments: map[string]any{"host": "web" + strconv.Itoa(i)},
			})
			errs <- err
		}()
	}
	waitStarted(t, runner, stdioTestMaxInFlight)

	// Give the calls over the ceiling every chance to slip through before
	// looking: a ceiling that leaks does so here.
	select {
	case host := <-runner.started:
		t.Fatalf("a call for %s reached the runner while %d were already running", host, stdioTestMaxInFlight)
	case <-time.After(300 * time.Millisecond):
	}
	if peak, _ := runner.snapshot(); peak != stdioTestMaxInFlight {
		t.Errorf("peak concurrent calls = %d, want %d", peak, stdioTestMaxInFlight)
	}

	// The ceiling counts tool calls only: listing the tools is not work on a
	// host, and an agent must be able to do it while its calls run.
	listCtx, listCancel := context.WithTimeout(ctx, 2*time.Second)
	defer listCancel()
	if _, err := session.ListTools(listCtx, nil); err != nil {
		t.Errorf("tools/list while the ceiling is reached: %v", err)
	}

	runner.releaseAll()
	waitStarted(t, runner, extra)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("CallTool: %v", err)
		}
	}
	if peak, calls := runner.snapshot(); peak > stdioTestMaxInFlight || len(calls) != stdioTestMaxInFlight+extra {
		t.Errorf("peak = %d, calls = %d; want peak <= %d and every call run once released",
			peak, len(calls), stdioTestMaxInFlight)
	}
}

// TestServeStdioAWaitingCallThatIsCancelledNeverRuns is the stdio counterpart
// of the 503 focal serve answers a request that gave up at the door: the call
// is answered with a JSON-RPC error and nothing is started for it.
func TestServeStdioAWaitingCallThatIsCancelledNeverRuns(t *testing.T) {
	cfg := withConfig(t, "")
	runner := newGatedRunner()
	t.Cleanup(runner.releaseAll)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	peer := startStdio(t, ctx, cfg, runner)
	lines := peer.rawLines()

	for i := range stdioTestMaxInFlight {
		peer.send(t, map[string]any{"jsonrpc": "2.0", "id": 100 + i, "method": "tools/call", "params": metaParams(map[string]any{
			"name": "inspect_system", "arguments": map[string]any{"host": "busy"},
		})})
	}
	waitStarted(t, runner, stdioTestMaxInFlight)

	peer.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": metaParams(map[string]any{
		"name": "inspect_system", "arguments": map[string]any{"host": "waiting"},
	})})
	// Let the call reach the ceiling and start waiting before it is
	// cancelled; cancelled earlier, it would never have been waiting at all.
	time.Sleep(100 * time.Millisecond)
	peer.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": metaParams(map[string]any{
		"requestId": 1,
	})})

	var resp struct {
		ID    int             `json:"id"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(nextLine(t, lines)), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != 1 || resp.Error == nil {
		t.Fatalf("response = %+v, want a JSON-RPC error for the cancelled call", resp)
	}
	if _, calls := runner.snapshot(); slices.Contains(calls, "waiting") {
		t.Error("the cancelled call reached the runner")
	}
}

// TestServeStdioRefusesANonPositiveCeiling keeps a forgotten MaxInFlight from
// meaning "no ceiling": ServeStdio refuses before it reads anything.
func TestServeStdioRefusesANonPositiveCeiling(t *testing.T) {
	cfg := withConfig(t, "")
	err := ServeStdio(context.Background(), cfg, &recordingRunner{}, Options{}, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("ServeStdio accepted MaxInFlight 0")
	}
	if errors.Is(err, io.EOF) {
		t.Errorf("error = %v, want a refusal of the ceiling, not the end of input", err)
	}
}

// setShutdownGrace shortens how long ServeStdio waits for running calls after
// its input ends, so the test that exercises it does not take seconds.
func setShutdownGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := stdioShutdownGrace
	stdioShutdownGrace = d
	t.Cleanup(func() { stdioShutdownGrace = old })
}
