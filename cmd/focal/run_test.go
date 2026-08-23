package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// These tests cover only what this package is responsible for: that the
// process's arguments and streams reach internal/cli and that its exit code
// comes back. The command line itself is exercised in internal/cli.

func TestRun_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr)
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
	code := run([]string{"--bogus"}, &stdout, &stderr)
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
	code := run([]string{"prod-web", "exec", "id"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown_operation") {
		t.Fatalf("stderr = %q, want an unknown_operation error", stderr.String())
	}
}
