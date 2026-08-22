package result_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/result"
)

func TestEnvelopeEncodeMatchesGolden(t *testing.T) {
	env := result.Envelope{
		Operation:  "service",
		Host:       "user@host.example",
		Status:     result.StatusOK,
		ExitCode:   0,
		DurationMs: 42,
		Stdout:     "ActiveState=active\n",
		Data:       map[string]string{"ActiveState": "active"},
	}

	got, err := env.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	want, readErr := os.ReadFile(filepath.Join("testdata", "envelope_golden.json"))
	if readErr != nil {
		t.Fatalf("read golden: %v", readErr)
	}

	if string(got) != string(want) {
		t.Fatalf("Encode mismatch:\ngot:  %s\nwant: %s", got, want)
	}

	var decoded map[string]json.RawMessage
	if jsonErr := json.Unmarshal(got, &decoded); jsonErr != nil {
		t.Fatalf("Encode output is not valid JSON: %v", jsonErr)
	}
}

func TestEnvelopeEncodeOmitsEmptyOptionalFields(t *testing.T) {
	env := result.Envelope{
		Operation:  "system",
		Host:       "host",
		Status:     result.StatusFailed,
		ExitCode:   1,
		DurationMs: 0,
	}

	got, err := env.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var decoded map[string]json.RawMessage
	if jsonErr := json.Unmarshal(got, &decoded); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	for _, key := range []string{"truncated", "stdout", "stderr", "data", "error"} {
		if _, ok := decoded[key]; ok {
			t.Fatalf("%s must be omitted when empty, got %s", key, got)
		}
	}

	for _, key := range []string{"operation", "host", "status", "exit_code", "duration_ms"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("%s is a required field and must always be present, got %s", key, got)
		}
	}
}

func TestEnvelopeEncodeDoesNotEscapeHTML(t *testing.T) {
	env := result.Envelope{
		Operation:  "logs",
		Host:       "host",
		Status:     result.StatusOK,
		ExitCode:   0,
		DurationMs: 1,
		Stdout:     "<tag> a & b",
	}

	got, err := env.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !json.Valid(got) {
		t.Fatal("Encode output is not valid JSON")
	}
	for _, r := range []string{"<", ">", "&"} {
		if !strings.Contains(string(got), r) {
			t.Fatalf("Encode output escaped %q, want raw character preserved", r)
		}
	}
}

func TestEnvelopeStatusEnumValues(t *testing.T) {
	cases := map[result.Status]string{
		result.StatusOK:        "ok",
		result.StatusFailed:    "failed",
		result.StatusTimeout:   "timeout",
		result.StatusTruncated: "truncated",
	}
	for status, want := range cases {
		if string(status) != want {
			t.Fatalf("status = %q, want %q", status, want)
		}
	}
}

func TestEnvelopeTimeoutHasExitCodeMinusOne(t *testing.T) {
	env := result.Envelope{
		Operation:  "logs",
		Host:       "host",
		Status:     result.StatusTimeout,
		ExitCode:   -1,
		DurationMs: 30000,
	}

	got, err := env.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var decoded struct {
		Status   result.Status `json:"status"`
		ExitCode int           `json:"exit_code"`
	}
	if jsonErr := json.Unmarshal(got, &decoded); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if decoded.Status != result.StatusTimeout {
		t.Fatalf("status = %q, want %q", decoded.Status, result.StatusTimeout)
	}
	if decoded.ExitCode != -1 {
		t.Fatalf("exit_code = %d, want -1", decoded.ExitCode)
	}
}

func TestEnvelopeTruncatedSetsFlagAndExitCode(t *testing.T) {
	cases := []struct {
		name         string
		exitCode     int
		wantExitCode int
	}{
		{name: "exit code captured before cutoff", exitCode: 0, wantExitCode: 0},
		{name: "no exit code captured before cutoff", exitCode: -1, wantExitCode: -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := result.Envelope{
				Operation:  "logs",
				Host:       "host",
				Status:     result.StatusTruncated,
				ExitCode:   tc.exitCode,
				DurationMs: 5000,
				Truncated:  true,
				Stdout:     "some partial output",
			}

			got, err := env.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			var decoded struct {
				Status    result.Status `json:"status"`
				ExitCode  int           `json:"exit_code"`
				Truncated bool          `json:"truncated"`
			}
			if jsonErr := json.Unmarshal(got, &decoded); jsonErr != nil {
				t.Fatalf("unmarshal: %v", jsonErr)
			}
			if decoded.Status != result.StatusTruncated {
				t.Fatalf("status = %q, want %q", decoded.Status, result.StatusTruncated)
			}
			if !decoded.Truncated {
				t.Fatal("truncated = false, want true")
			}
			if decoded.ExitCode != tc.wantExitCode {
				t.Fatalf("exit_code = %d, want %d", decoded.ExitCode, tc.wantExitCode)
			}
		})
	}
}

func TestEnvelopeErrorFieldIsReplaceable(t *testing.T) {
	env := result.Envelope{
		Operation:  "system",
		Host:       "host",
		Status:     result.StatusFailed,
		ExitCode:   255,
		DurationMs: 10,
		Error:      result.ExecutionError("ssh_failed", "ssh: connection refused"),
	}

	got, err := env.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var decoded struct {
		Error *result.Error `json:"error"`
	}
	if jsonErr := json.Unmarshal(got, &decoded); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if decoded.Error == nil {
		t.Fatal("error field was not decoded")
	}
	if decoded.Error.Kind != result.KindExecution {
		t.Fatalf("error.kind = %q, want %q", decoded.Error.Kind, result.KindExecution)
	}
	if decoded.Error.Code != "ssh_failed" {
		t.Fatalf("error.code = %q, want %q", decoded.Error.Code, "ssh_failed")
	}
}
