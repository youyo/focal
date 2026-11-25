package vo_test

import (
	"strings"
	"testing"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// TestParsePackageNameRejectsInjectionInputs runs PackageName against the shared
// payload table with one entry set aside: "+" is part of real package names
// (libstdc++6, g++), so "nginx+id" is the one payload in the table that this
// type accepts by design. It is still a single argv token that passes the same
// first-character and length rules; nothing else in the table is excused.
func TestParsePackageNameRejectsInjectionInputs(t *testing.T) {
	for _, tc := range injectionInputs {
		if tc.name == "plus" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParsePackageName(tc.input)
			if err == nil {
				t.Fatalf("ParsePackageName(%q) accepted (value %q), want rejected", tc.input, got.String())
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

func TestParsePackageNameAccepts(t *testing.T) {
	accepted := []string{
		"procps",
		"nginx",
		"libstdc++6",
		"g++",
		"python3.12",
		"libc6-dev",
		"openssh-server",
		"gcc_x",
		"0ad",
		"a",
		strings.Repeat("a", 128),
	}
	for _, in := range accepted {
		t.Run(in, func(t *testing.T) {
			got, err := vo.ParsePackageName(in)
			if err != nil {
				t.Fatalf("ParsePackageName(%q) rejected: %v", in, err)
			}
			if got.String() != in {
				t.Errorf("String() = %q, want %q", got.String(), in)
			}
		})
	}
}

func TestParsePackageNameRejects(t *testing.T) {
	rejected := []struct {
		name  string
		input string
	}{
		{"leading hyphen", "-qa"},
		{"leading option", "--help"},
		{"leading plus", "+x"},
		{"leading dot", ".x"},
		{"space", "foo bar"},
		{"tab", "foo\tbar"},
		{"newline", "foo\nbar"},
		{"asterisk glob", "lib*"},
		{"question glob", "lib?"},
		{"bracket glob", "lib[a]"},
		{"colon arch qualifier", "libc6:amd64"},
		{"at sign", "foo@bar"},
		{"equals", "foo=1"},
		{"semicolon", "nginx;id"},
		{"command substitution", "$(id)"},
		{"slash", "../etc"},
		{"percent", "%h"},
		{"non ascii", "caf\xc3\xa9"},
		{"129 bytes", strings.Repeat("a", 129)},
		{"empty", ""},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParsePackageName(tc.input)
			if err == nil {
				t.Fatalf("ParsePackageName(%q) accepted (value %q), want rejected", tc.input, got.String())
			}
			if err.Kind != result.KindValidation {
				t.Errorf("Kind = %q, want %q", err.Kind, result.KindValidation)
			}
			if err.Field != "package_name" {
				t.Errorf("Field = %q, want package_name", err.Field)
			}
			if strings.Contains(err.Message, tc.input) && tc.input != "" {
				t.Errorf("Message %q echoes the rejected payload", err.Message)
			}
		})
	}
}

func TestPackageNameZeroValueIsEmpty(t *testing.T) {
	var zero vo.PackageName
	if zero.String() != "" {
		t.Errorf("zero PackageName String() = %q, want empty", zero.String())
	}
}
