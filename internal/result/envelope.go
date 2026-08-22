package result

import (
	"bytes"
	"encoding/json"
)

// Status is the fixed enum of terminal states an Envelope can report.
type Status string

const (
	// StatusOK marks a command that ran to completion with exit code 0.
	StatusOK Status = "ok"
	// StatusFailed marks a command that ran to completion with a non-zero
	// exit code, or an execution failure captured in Error.
	StatusFailed Status = "failed"
	// StatusTimeout marks a command killed after exceeding its configured
	// timeout. ExitCode is always -1 for this status.
	StatusTimeout Status = "timeout"
	// StatusTruncated marks a command whose output was cut off after
	// exceeding max_output. Truncated is always true for this status;
	// ExitCode is the exit code captured before truncation, or -1 if none
	// was captured before the cutoff.
	StatusTruncated Status = "truncated"
)

// Envelope is the common output shape every Focal operation returns,
// shared by CLI and MCP. Field order is fixed and mirrors the JSON key
// order produced by Encode. Stdout is kept as raw text; Data holds the
// small set of operations (e.g. "service") that additionally parse stdout
// into key/value pairs (v0.1: raw + a few parsed operations, not a full
// parse of every command's output).
type Envelope struct {
	Operation  string            `json:"operation"`
	Host       string            `json:"host"`
	Status     Status            `json:"status"`
	ExitCode   int               `json:"exit_code"`
	DurationMs int64             `json:"duration_ms"`
	Truncated  bool              `json:"truncated,omitempty"`
	Stdout     string            `json:"stdout,omitempty"`
	Stderr     string            `json:"stderr,omitempty"`
	Data       map[string]string `json:"data,omitempty"`
	Error      *Error            `json:"error,omitempty"`
}

// Encode marshals e as compact, single-line JSON with HTML escaping
// disabled, so tokens like <, >, and & occurring in stdout/stderr are not
// rewritten.
func (e Envelope) Encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
