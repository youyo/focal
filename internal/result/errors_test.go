package result_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/result"
)

func TestConstructorsSetKind(t *testing.T) {
	cases := []struct {
		name string
		err  *result.Error
		want result.Kind
	}{
		{
			name: "validation",
			err:  result.ValidationError("invalid_target", "target must not be empty", "target", []string{"user@host"}),
			want: result.KindValidation,
		},
		{
			name: "policy",
			err:  result.PolicyError("sudo_not_allowed", "sudo always is not permitted for logs", "sudo", []string{"never", "auto"}),
			want: result.KindPolicy,
		},
		{
			name: "execution",
			err:  result.ExecutionError("ssh_failed", "ssh: connection refused"),
			want: result.KindExecution,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Kind != tc.want {
				t.Fatalf("Kind = %q, want %q", tc.err.Kind, tc.want)
			}
		})
	}
}

func TestErrorsAsExtractsStructuredError(t *testing.T) {
	base := result.ValidationError("invalid_target", "target must not be empty", "target", nil)
	wrapped := fmt.Errorf("parse target: %w", error(base))

	var got *result.Error
	if !errors.As(wrapped, &got) {
		t.Fatal("errors.As failed to extract *result.Error from wrapped error")
	}
	if got.Kind != result.KindValidation {
		t.Fatalf("Kind = %q, want %q", got.Kind, result.KindValidation)
	}
	if got.Code != "invalid_target" {
		t.Fatalf("Code = %q, want %q", got.Code, "invalid_target")
	}
}

func TestErrorImplementsErrorInterface(t *testing.T) {
	var err error = result.ExecutionError("ssh_failed", "ssh: connection refused")
	if err.Error() == "" {
		t.Fatal("Error() returned empty string")
	}
}

func TestEncodeMatchesGolden(t *testing.T) {
	err := result.ValidationError("invalid_target", "target must not be empty", "target", []string{"user@host"})

	got, encErr := result.Encode(err)
	if encErr != nil {
		t.Fatalf("Encode: %v", encErr)
	}

	want, readErr := os.ReadFile(filepath.Join("testdata", "error_golden.json"))
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

func TestEncodeOmitsEmptyOptionalFields(t *testing.T) {
	err := result.ExecutionError("ssh_failed", "ssh: connection refused")

	got, encErr := result.Encode(err)
	if encErr != nil {
		t.Fatalf("Encode: %v", encErr)
	}

	var decoded map[string]json.RawMessage
	if jsonErr := json.Unmarshal(got, &decoded); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if _, ok := decoded["field"]; ok {
		t.Fatal("field must be omitted when empty")
	}
	if _, ok := decoded["allowed"]; ok {
		t.Fatal("allowed must be omitted when empty")
	}
}

func TestEncodeDoesNotEscapeHTML(t *testing.T) {
	err := result.PolicyError("sudo_not_allowed", "target <host> & policy", "sudo", []string{"never", "auto"})

	got, encErr := result.Encode(err)
	if encErr != nil {
		t.Fatalf("Encode: %v", encErr)
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
