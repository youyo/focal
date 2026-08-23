package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/config"
	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// protocolVersion is the MCP revision these tests speak. It is written out
// rather than taken from the SDK because the point of the e2e tests is that
// focal answers this specific revision's stateless requests.
const protocolVersion = "2026-07-28"

// recordingRunner is the stand-in for the layer below internal/mcp: it
// remembers what it was asked to run and answers with whatever the test
// programmed. Nothing here touches SSH, which is the whole point — this
// package builds an Operation and a Target and hands them on.
type recordingRunner struct {
	calls    []runnerCall
	envelope result.Envelope
	err      *result.Error
}

type runnerCall struct {
	operation string
	target    vo.Target
	identity  string
}

func (r *recordingRunner) Run(_ context.Context, op operation.Operation, t vo.Target, identity string) (result.Envelope, *result.Error) {
	r.calls = append(r.calls, runnerCall{operation: op.Name(), target: t, identity: identity})
	if r.err != nil {
		return result.Envelope{}, r.err
	}
	return r.envelope, nil
}

// withConfig points XDG_CONFIG_HOME at a fresh directory holding body as
// focal/config.yaml. An empty body leaves no file at all, which is how a test
// asks for focal's safe-side defaults.
func withConfig(t *testing.T, body string) config.Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if body != "" {
		focalDir := filepath.Join(dir, "focal")
		if err := os.MkdirAll(focalDir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", focalDir, err)
		}
		if err := os.WriteFile(filepath.Join(focalDir, "config.yaml"), []byte(body), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// serverFor starts an httptest server carrying focal's MCP handler and returns
// its URL together with the runner it was built with.
func serverFor(t *testing.T, body string, opts Options) (string, *recordingRunner) {
	t.Helper()
	cfg := withConfig(t, body)
	runner := &recordingRunner{envelope: result.Envelope{Operation: "system", Host: "web", Status: result.StatusOK}}
	srv := httptest.NewServer(NewHandler(cfg, runner, opts))
	t.Cleanup(srv.Close)
	return srv.URL, runner
}

// rpcResponse is as much of a JSON-RPC response as these tests read.
type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// callResult is the tools/call result shape: the content the agent reads and
// the flag that says whether focal refused.
type callResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// rpc posts one JSON-RPC request the way a 2026-07-28 client does: the
// per-request _meta triple in the body, and the Mcp-Method/Mcp-Name headers a
// gateway in front of focal would route on.
func rpc(t *testing.T, url, method, name string, params map[string]any) rpcResponse {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    protocolVersion,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "focal-test", "version": "0"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", protocolVersion)
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	// From 2026-07-28 a JSON-RPC error is also reflected in the HTTP
	// status: an unknown tool is invalid params, and so 400 rather than
	// 200. Both statuses carry a JSON-RPC message, and which one arrived
	// is what the tests below assert on.
	switch resp.StatusCode {
	case http.StatusOK, http.StatusBadRequest, http.StatusNotFound:
	default:
		body, _ := readAll(resp)
		t.Fatalf("status = %d, want a JSON-RPC response; body = %s", resp.StatusCode, body)
	}
	return decodeResponse(t, resp)
}

// decodeResponse reads the single JSON-RPC message out of the response, which
// arrives either as application/json or as one SSE data frame.
func decodeResponse(t *testing.T, resp *http.Response) rpcResponse {
	t.Helper()
	var payload []byte
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				payload = []byte(data)
				break
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("read stream: %v", err)
		}
	} else {
		var err error
		if payload, err = readAll(resp); err != nil {
			t.Fatalf("read body: %v", err)
		}
	}
	if len(payload) == 0 {
		t.Fatal("no JSON-RPC message in the response")
	}
	var out rpcResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("decode %q: %v", payload, err)
	}
	return out
}

func readAll(resp *http.Response) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(resp.Body)
	return buf.Bytes(), err
}

// listToolNames asks for the tool list and returns the names in the order the
// server gave them.
func listToolNames(t *testing.T, url string) []string {
	t.Helper()
	resp := rpc(t, url, "tools/list", "", nil)
	if resp.Error != nil {
		t.Fatalf("tools/list: %+v", resp.Error)
	}
	var out struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	names := make([]string, 0, len(out.Tools))
	for _, tool := range out.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// callTool runs one tools/call and returns its result, failing the test if the
// server answered with a JSON-RPC error instead.
func callTool(t *testing.T, url, tool string, args map[string]any) callResult {
	t.Helper()
	resp := rpc(t, url, "tools/call", tool, map[string]any{"name": tool, "arguments": args})
	if resp.Error != nil {
		t.Fatalf("tools/call %s: JSON-RPC error %+v, want a tool result", tool, resp.Error)
	}
	var out callResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return out
}

// toolError decodes the structured error out of an isError tool result.
func toolErrorOf(t *testing.T, res callResult) result.Error {
	t.Helper()
	if !res.IsError {
		t.Fatalf("result is not an error: %+v", res)
	}
	if len(res.Content) == 0 {
		t.Fatal("error result carries no content")
	}
	var got result.Error
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("error content is not focal's error schema: %v (%q)", err, res.Content[0].Text)
	}
	return got
}

func TestToolsListExposesEnabledOperationsOnly(t *testing.T) {
	url, _ := serverFor(t, "", Options{})

	got := listToolNames(t, url)
	want := []string{
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
	if !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
}

func TestToolsListOrderIsDeterministic(t *testing.T) {
	url, _ := serverFor(t, "", Options{})

	first := listToolNames(t, url)
	second := listToolNames(t, url)
	if !slices.Equal(first, second) {
		t.Errorf("tools/list is not stable: %v then %v", first, second)
	}
	if !slices.IsSorted(first) {
		t.Errorf("tools/list is not sorted: %v", first)
	}
}

func TestDisabledOperationIsNeitherListedNorCallable(t *testing.T) {
	url, _ := serverFor(t, "", Options{})

	if slices.Contains(listToolNames(t, url), "inspect_kernel") {
		t.Fatal("kernel is disabled by default but its tool is listed")
	}

	resp := rpc(t, url, "tools/call", "inspect_kernel",
		map[string]any{"name": "inspect_kernel", "arguments": map[string]any{"host": "web"}})
	if resp.Error == nil {
		t.Fatalf("calling a disabled operation succeeded: %s", resp.Result)
	}
	if !strings.Contains(resp.Error.Message, "inspect_kernel") {
		t.Errorf("error message = %q, want it to name the unknown tool", resp.Error.Message)
	}
}

func TestEnablingKernelListsItsTool(t *testing.T) {
	url, _ := serverFor(t, "operations:\n  kernel:\n    enabled: true\n", Options{})

	if !slices.Contains(listToolNames(t, url), "inspect_kernel") {
		t.Error("kernel is enabled but its tool is not listed")
	}
}

func TestToolsCallReturnsTheEnvelope(t *testing.T) {
	url, runner := serverFor(t, "", Options{})
	runner.envelope = result.Envelope{
		Operation: "logs", Host: "web", Status: result.StatusOK, Stdout: "hello",
	}

	res := callTool(t, url, "inspect_logs", map[string]any{"host": "web", "service": "nginx"})
	if res.IsError {
		t.Fatalf("call reported an error: %+v", res)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("content = %+v, want one text part", res.Content)
	}
	var env result.Envelope
	if err := json.Unmarshal([]byte(res.Content[0].Text), &env); err != nil {
		t.Fatalf("content is not an envelope: %v (%q)", err, res.Content[0].Text)
	}
	if env.Operation != "logs" || env.Stdout != "hello" {
		t.Errorf("envelope = %+v, want the one the runner returned", env)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner saw %d calls, want 1", len(runner.calls))
	}
	if got := runner.calls[0].target.String(); got != "web" {
		t.Errorf("target = %q, want %q", got, "web")
	}
}

func TestUserAndIdentityReachTheRunner(t *testing.T) {
	url, runner := serverFor(t, "", Options{})

	callTool(t, url, "inspect_system", map[string]any{
		"host": "web", "user": "ec2-user", "identity": "customer-a",
	})
	if len(runner.calls) != 1 {
		t.Fatalf("runner saw %d calls, want 1", len(runner.calls))
	}
	if got := runner.calls[0].target.String(); got != "ec2-user@web" {
		t.Errorf("target = %q, want %q", got, "ec2-user@web")
	}
	if got := runner.calls[0].identity; got != "customer-a" {
		t.Errorf("identity = %q, want %q", got, "customer-a")
	}
}

func TestDefaultUserAppliesWhenTheCallNamesNone(t *testing.T) {
	url, runner := serverFor(t, "", Options{DefaultUser: "ec2-user"})

	callTool(t, url, "inspect_system", map[string]any{"host": "web"})
	if got := runner.calls[0].target.String(); got != "ec2-user@web" {
		t.Errorf("target = %q, want the server default applied", got)
	}

	// A host that names its own user keeps it: the server default is a
	// default, not an override.
	callTool(t, url, "inspect_system", map[string]any{"host": "root@web"})
	if got := runner.calls[1].target.String(); got != "root@web" {
		t.Errorf("target = %q, want the host's own user kept", got)
	}
}

func TestValidationErrorIsAToolErrorNotAProtocolError(t *testing.T) {
	url, runner := serverFor(t, "", Options{})

	res := callTool(t, url, "inspect_logs", map[string]any{"host": "web", "service": "nginx;id"})
	got := toolErrorOf(t, res)
	if got.Kind != result.KindValidation {
		t.Errorf("kind = %q, want %q", got.Kind, result.KindValidation)
	}
	if got.Code == "" || got.Message == "" || got.Field == "" {
		t.Errorf("error = %+v, want code, message and field set for the agent", got)
	}
	if len(runner.calls) != 0 {
		t.Errorf("a rejected input still reached the runner: %+v", runner.calls)
	}
}

func TestPolicyErrorIsAToolError(t *testing.T) {
	url, runner := serverFor(t, "", Options{})
	runner.err = result.PolicyError("sudo_not_allowed", "escalation is not permitted", "operations.logs.sudo", []string{"never"})

	res := callTool(t, url, "inspect_logs", map[string]any{"host": "web", "service": "nginx"})
	got := toolErrorOf(t, res)
	if got.Kind != result.KindPolicy || got.Code != "sudo_not_allowed" {
		t.Errorf("error = %+v, want the runner's policy error", got)
	}
	if !slices.Equal(got.Allowed, []string{"never"}) {
		t.Errorf("allowed = %v, want the runner's allowed list", got.Allowed)
	}
}

func TestHostIsNotAllowlistedButIsValidated(t *testing.T) {
	url, runner := serverFor(t, "", Options{})

	// Any host is accepted: focal constrains what may be run, not where.
	for _, host := range []string{"web", "10.0.0.1", "bastion.internal", "ec2-user@10.0.0.1"} {
		if res := callTool(t, url, "inspect_system", map[string]any{"host": host}); res.IsError {
			t.Errorf("host %q was refused: %+v", host, toolErrorOf(t, res))
		}
	}
	calls := len(runner.calls)

	// An injection attempt is not.
	for _, host := range []string{"web;id", "$(id)", "web|cat", "-oProxyCommand=id", "web host", "web\nid"} {
		res := callTool(t, url, "inspect_system", map[string]any{"host": host})
		if !res.IsError {
			t.Errorf("host %q was accepted", host)
			continue
		}
		if got := toolErrorOf(t, res); got.Kind != result.KindValidation {
			t.Errorf("host %q: kind = %q, want %q", host, got.Kind, result.KindValidation)
		}
	}
	if len(runner.calls) != calls {
		t.Errorf("a rejected host still reached the runner: %+v", runner.calls[calls:])
	}
}

func TestUnknownToolIsAProtocolError(t *testing.T) {
	url, _ := serverFor(t, "", Options{})

	resp := rpc(t, url, "tools/call", "run_command",
		map[string]any{"name": "run_command", "arguments": map[string]any{"host": "web"}})
	if resp.Error == nil {
		t.Fatalf("unknown tool succeeded: %s", resp.Result)
	}
}

// TestNumericBoundsAreTheSchema'sJob fixes where the JSON type boundary of a
// numeric parameter is enforced: the SDK validates it against the inferred
// input schema before focal's handler runs, and only a well-typed integer
// reaches internal/vo. Delegating this is deliberate — a second, hand-written
// type check here would be a second description of the same schema.
func TestNumericBoundsAreEnforcedBySchema(t *testing.T) {
	url, runner := serverFor(t, "", Options{})

	for _, lines := range []any{"200", 1.5, true, []any{200}} {
		res := callTool(t, url, "inspect_logs", map[string]any{
			"host": "web", "service": "nginx", "lines": lines,
		})
		if !res.IsError {
			t.Errorf("lines = %#v was accepted", lines)
		}
	}
	if len(runner.calls) != 0 {
		t.Errorf("a malformed lines value reached the runner: %+v", runner.calls)
	}

	// A negative count is a well-typed integer, so the schema lets it
	// through and internal/vo is what refuses it.
	res := callTool(t, url, "inspect_logs", map[string]any{"host": "web", "service": "nginx", "lines": -1})
	if got := toolErrorOf(t, res); got.Kind != result.KindValidation {
		t.Errorf("lines = -1: kind = %q, want %q", got.Kind, result.KindValidation)
	}

	// null is how JSON spells "not named", and focal reads it that way.
	if res := callTool(t, url, "inspect_logs", map[string]any{
		"host": "web", "service": "nginx", "lines": nil,
	}); res.IsError {
		t.Errorf("lines = null was refused: %+v", toolErrorOf(t, res))
	}
}

func TestUnknownArgumentIsRejected(t *testing.T) {
	url, runner := serverFor(t, "", Options{})

	res := callTool(t, url, "inspect_system", map[string]any{"host": "web", "command": "id"})
	if !res.IsError {
		t.Fatal("an argument focal does not declare was accepted")
	}
	if len(runner.calls) != 0 {
		t.Errorf("it reached the runner: %+v", runner.calls)
	}
}
