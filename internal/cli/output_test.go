package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/result"
)

func TestWriteEnvelope(t *testing.T) {
	env := result.Envelope{
		Operation: "service",
		Host:      "prod-web",
		Status:    result.StatusOK,
		Stdout:    "ActiveState=active <ok>",
	}

	t.Run("compact is one line and leaves HTML alone", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeEnvelope(&buf, env, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		line := strings.TrimSuffix(buf.String(), "\n")
		if strings.Contains(line, "\n") {
			t.Fatalf("compact output is not one line: %q", line)
		}
		if !strings.Contains(line, "<ok>") {
			t.Fatalf("output escaped HTML: %q", line)
		}
		var back result.Envelope
		if err := json.Unmarshal([]byte(line), &back); err != nil {
			t.Fatalf("output is not JSON: %v", err)
		}
		if back.Operation != env.Operation || back.Host != env.Host {
			t.Fatalf("round trip = %+v, want %+v", back, env)
		}
	})

	t.Run("pretty is indented and carries the same fields", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeEnvelope(&buf, env, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(buf.String(), "\n  \"operation\": \"service\"") {
			t.Fatalf("output is not indented: %q", buf.String())
		}
		var back result.Envelope
		if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
			t.Fatalf("output is not JSON: %v", err)
		}
		if back.Stdout != env.Stdout {
			t.Fatalf("stdout = %q, want %q", back.Stdout, env.Stdout)
		}
	})
}

func TestWriteError(t *testing.T) {
	var buf bytes.Buffer
	writeError(&buf, result.ValidationError("bad_service_name", "service name is not accepted", "service", []string{"nginx"}))

	line := strings.TrimSuffix(buf.String(), "\n")
	if strings.Contains(line, "\n") {
		t.Fatalf("error output is not one line: %q", line)
	}
	var got result.Error
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("error output is not JSON: %v", err)
	}
	if got.Kind != result.KindValidation || got.Code != "bad_service_name" {
		t.Fatalf("error = %+v, want a validation error with the code preserved", got)
	}
	if got.Field != "service" || len(got.Allowed) != 1 {
		t.Fatalf("error lost the field/allowed detail: %+v", got)
	}
}

func TestUsageErrorDefaultsToTheInvocationForm(t *testing.T) {
	err := usageError("wrong_shape", "message", "", nil)
	if err.code != exitUsage {
		t.Fatalf("exit code = %d, want %d", err.code, exitUsage)
	}
	if len(err.err.Allowed) != 1 || err.err.Allowed[0] != usageForm {
		t.Fatalf("allowed = %v, want %v", err.err.Allowed, []string{usageForm})
	}
	if err.err.Kind != result.KindValidation {
		t.Fatalf("kind = %q, want %q", err.err.Kind, result.KindValidation)
	}
}

func TestRejectedKeepsTheOriginalKind(t *testing.T) {
	err := rejected(result.PolicyError("operation_disabled", "message", "operations.kernel.enabled", nil))
	if err.code != exitRejected {
		t.Fatalf("exit code = %d, want %d", err.code, exitRejected)
	}
	if err.err.Kind != result.KindPolicy {
		t.Fatalf("kind = %q, want %q", err.err.Kind, result.KindPolicy)
	}
	if err.Error() == "" {
		t.Fatal("cliError has no text form")
	}
}
