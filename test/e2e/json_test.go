//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

// envelope mirrors the fields of result.Envelope the suite asserts on. It is a
// local copy rather than an import so the E2E suite reads focal's JSON exactly
// as any other client would, through the wire shape and not the Go type.
type envelope struct {
	Operation string `json:"operation"`
	Status    string `json:"status"`
	Stdout    string `json:"stdout"`
}

// errDoc mirrors the result.Error JSON focal writes for a refusal, on stderr
// for the CLI and inside a failed tool result for MCP.
type errDoc struct {
	Kind string `json:"kind"`
	Code string `json:"code"`
}

// decodeEnvelope parses one line of focal's stdout as an envelope, failing the
// test with the raw text when it is not valid JSON.
func decodeEnvelope(t *testing.T, s string) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &env); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\n%s", err, s)
	}
	return env
}

// decodeError parses one line of focal's error JSON, failing the test with the
// raw text when it is not valid JSON.
func decodeError(t *testing.T, s string) errDoc {
	t.Helper()
	var e errDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &e); err != nil {
		t.Fatalf("output is not a JSON error: %v\n%s", err, s)
	}
	return e
}
