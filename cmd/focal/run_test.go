package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// These tests cover only what this package is responsible for: that the
// process's arguments and streams reach internal/cli and that its exit code
// comes back. The command line itself is exercised in internal/cli.

func TestRun_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("stdout is empty, want a version string")
	}
}

func TestRun_UnknownFlag_FailsWithStructuredError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--bogus"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	line := strings.TrimSpace(stderr.String())
	var payload struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("stderr is not a single JSON line: %v (stderr=%q)", err, line)
	}
	if payload.Kind != "validation" {
		t.Fatalf("kind = %q, want %q", payload.Kind, "validation")
	}
	if payload.Message == "" {
		t.Fatal("the error carries no message")
	}
}

func TestRun_UnknownOperation_FailsWithStructuredError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"prod-web", "exec", "id"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown_operation") {
		t.Fatalf("stderr = %q, want an unknown_operation error", stderr.String())
	}
}

// TestRun_MCPSpeaksOnTheProcessStreams covers the one stream this package
// hands over that the others do not use: focal mcp reads its requests from the
// stdin given here and answers on the stdout given here.
func TestRun_MCPSpeaksOnTheProcessStreams(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"mcp"}, inR, outW, &stderr)
		_ = outW.Close()
	}()
	t.Cleanup(func() { _ = inW.Close(); _ = outR.Close() })

	go func() {
		_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{"_meta":{`+
			`"io.modelcontextprotocol/protocolVersion":"2026-07-28",`+
			`"io.modelcontextprotocol/clientInfo":{"name":"focal-test","version":"0"},`+
			`"io.modelcontextprotocol/clientCapabilities":{}}}}`+"\n")
	}()
	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatalf("read stdout: %v; stderr=%s", err, stderr.String())
	}
	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("stdout is not a JSON-RPC message: %v (%q)", err, line)
	}
	if resp.JSONRPC != "2.0" || resp.ID != 7 || resp.Error != nil {
		t.Errorf("response = %s, want the answer to tools/list", line)
	}

	_ = inW.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0; stderr=%s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("focal mcp did not exit within 5s of stdin closing")
	}
}
