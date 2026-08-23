package vo_test

import (
	"strings"
	"testing"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// TestParseProcessNameRejectsInjectionInputs runs ProcessName against the same
// payload table every other value object is held to, rather than a private copy
// of it. ProcessName is a local filter and never reaches a remote argv, but a
// type whose allowlist quietly drifted wider than its siblings' would be the
// first place a later change could reintroduce one, so it is checked here on
// the same terms.
func TestParseProcessNameRejectsInjectionInputs(t *testing.T) {
	for _, tc := range injectionInputs {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParseProcessName(tc.input)
			if err == nil {
				t.Fatalf("ParseProcessName(%q) accepted (value %q), want rejected", tc.input, got.String())
			}
			if err.Kind != result.KindValidation {
				t.Errorf("Kind = %q, want %q", err.Kind, result.KindValidation)
			}
			if got.String() != "" {
				t.Errorf("rejected input produced non-zero value %q, want empty", got.String())
			}
		})
	}
}

func TestParseProcessNameAccepts(t *testing.T) {
	accepted := []string{
		"nginx",
		"postgres_exporter",
		"java-app.1",
		"sshd",
		"foo@bar",
		"systemd-journald",
		"a",
		"0",
		strings.Repeat("a", 64),
	}
	for _, in := range accepted {
		t.Run(in, func(t *testing.T) {
			got, err := vo.ParseProcessName(in)
			if err != nil {
				t.Fatalf("ParseProcessName(%q) rejected: %v", in, err)
			}
			if got.String() != in {
				t.Errorf("String() = %q, want %q", got.String(), in)
			}
		})
	}
}

// TestParseProcessNameRejects is the explicit reject list the process filter
// owes its callers: the classic injection payloads named in the issue, plus the
// two rules that are ProcessName's own — a leading hyphen and the 64-byte
// ceiling.
func TestParseProcessNameRejects(t *testing.T) {
	rejected := []struct {
		name  string
		input string
	}{
		{"semicolon", "nginx;id"},
		{"command substitution", "$(id)"},
		{"backtick substitution", "`id`"},
		{"pipe", "nginx|cat"},
		{"path traversal", "../../etc/passwd"},
		{"embedded space", "nginx id"},
		{"embedded newline", "nginx\nid"},
		{"nul byte", "nginx\x00id"},
		{"leading hyphen", "-nginx"},
		{"colon is not a process name character", "nginx:1"},
		{"65 bytes", strings.Repeat("a", 65)},
		{"empty", ""},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParseProcessName(tc.input)
			if err == nil {
				t.Fatalf("ParseProcessName(%q) accepted (value %q), want rejected", tc.input, got.String())
			}
			if err.Kind != result.KindValidation {
				t.Errorf("Kind = %q, want %q", err.Kind, result.KindValidation)
			}
			if err.Field != "process_name" {
				t.Errorf("Field = %q, want process_name", err.Field)
			}
			if strings.Contains(err.Message, tc.input) && tc.input != "" {
				t.Errorf("Message %q echoes the rejected payload", err.Message)
			}
		})
	}
}

func TestProcessNameZeroValueIsEmpty(t *testing.T) {
	var zero vo.ProcessName
	if zero.String() != "" {
		t.Errorf("zero ProcessName String() = %q, want empty", zero.String())
	}
}
