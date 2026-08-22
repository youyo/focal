// Package policy_test exercises internal/policy from outside the package so
// that the tests can only reach the exported surface an administrator-facing
// caller (config) and the ssh command factories actually have.
package policy_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
)

// allOperations lists every operation the capability table is expected to
// declare. It is duplicated here on purpose: the table is the production
// declaration and this slice is the test's independent expectation of it.
var allOperations = []string{
	"system", "cpu", "memory", "storage",
	"network", "processes", "service", "logs", "kernel",
}

func TestSudoModeZeroValueIsNever(t *testing.T) {
	var m policy.SudoMode
	if m != policy.SudoNever {
		t.Fatalf("zero SudoMode = %v, want SudoNever (fail-safe zero value)", m)
	}
	if policy.SudoNever == policy.SudoAuto || policy.SudoAuto == policy.SudoAlways {
		t.Fatal("SudoNever/SudoAuto/SudoAlways must be three distinct values")
	}
}

func TestSudoModeString(t *testing.T) {
	for _, tc := range []struct {
		mode policy.SudoMode
		want string
	}{
		{policy.SudoNever, "never"},
		{policy.SudoAuto, "auto"},
		{policy.SudoAlways, "always"},
	} {
		if got := tc.mode.String(); got != tc.want {
			t.Errorf("SudoMode(%d).String() = %q, want %q", uint8(tc.mode), got, tc.want)
		}
	}
}

func TestParseSudoModeAccepts(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want policy.SudoMode
	}{
		{"never", policy.SudoNever},
		{"auto", policy.SudoAuto},
		{"always", policy.SudoAlways},
	} {
		got, err := policy.ParseSudoMode(tc.in)
		if err != nil {
			t.Errorf("ParseSudoMode(%q) returned error %v, want %v", tc.in, err, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseSudoMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseSudoModeRejects(t *testing.T) {
	for _, in := range []string{
		"", " ", "Never", "AUTO", "sudo", "always ", " always", "0", "true", "root",
	} {
		got, err := policy.ParseSudoMode(in)
		if err == nil {
			t.Errorf("ParseSudoMode(%q) = %v, want error", in, got)
			continue
		}
		if err.Kind != result.KindValidation {
			t.Errorf("ParseSudoMode(%q) error kind = %q, want %q", in, err.Kind, result.KindValidation)
		}
		if got != policy.SudoNever {
			t.Errorf("ParseSudoMode(%q) = %v on error, want SudoNever (fail-safe)", in, got)
		}
	}
}

func TestResolveAllowsNeverForEveryOperation(t *testing.T) {
	for _, name := range allOperations {
		p, err := policy.Resolve(name, policy.SudoNever)
		if err != nil {
			t.Errorf("Resolve(%q, never) returned error %v, want success", name, err)
			continue
		}
		if p.Operation() != name {
			t.Errorf("Resolve(%q, never).Operation() = %q, want %q", name, p.Operation(), name)
		}
		if p.Sudo() != policy.SudoNever {
			t.Errorf("Resolve(%q, never).Sudo() = %v, want SudoNever", name, p.Sudo())
		}
	}
}

func TestResolveAllowsAutoForLogsOnly(t *testing.T) {
	p, err := policy.Resolve("logs", policy.SudoAuto)
	if err != nil {
		t.Fatalf("Resolve(\"logs\", auto) returned error %v, want success", err)
	}
	if p.Operation() != "logs" || p.Sudo() != policy.SudoAuto {
		t.Fatalf("Resolve(\"logs\", auto) = {%q, %v}, want {\"logs\", auto}", p.Operation(), p.Sudo())
	}

	for _, name := range allOperations {
		if name == "logs" {
			continue
		}
		if _, err := policy.Resolve(name, policy.SudoAuto); err == nil {
			t.Errorf("Resolve(%q, auto) succeeded, want capability violation", name)
		}
	}
}

// TestResolveRejectsCapabilityViolations covers the three cases issue #5 calls
// out: a mode outside the operation's capability must be a startup error, not
// a silent fallback to a weaker mode.
func TestResolveRejectsCapabilityViolations(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode policy.SudoMode
	}{
		{"kernel", policy.SudoAlways},
		{"logs", policy.SudoAlways},
		{"shell", policy.SudoNever},
	} {
		p, err := policy.Resolve(tc.name, tc.mode)
		if err == nil {
			t.Errorf("Resolve(%q, %v) succeeded, want policy error", tc.name, tc.mode)
			continue
		}
		if err.Kind != result.KindPolicy {
			t.Errorf("Resolve(%q, %v) error kind = %q, want %q", tc.name, tc.mode, err.Kind, result.KindPolicy)
		}
		if len(err.Allowed) == 0 {
			t.Errorf("Resolve(%q, %v) error has no Allowed list", tc.name, tc.mode)
		}
		if p != (policy.Policy{}) {
			t.Errorf("Resolve(%q, %v) returned non-zero Policy %+v on error", tc.name, tc.mode, p)
		}
		var target *result.Error
		if !errors.As(error(err), &target) {
			t.Errorf("Resolve(%q, %v) error is not a *result.Error", tc.name, tc.mode)
		}
	}
}

// TestResolveRejectsUndeclaredSudoMode guards the enum boundary: a numeric
// SudoMode that is not one of the three declared modes must never resolve.
func TestResolveRejectsUndeclaredSudoMode(t *testing.T) {
	undeclared := policy.SudoAlways + 1
	for _, name := range allOperations {
		if _, err := policy.Resolve(name, undeclared); err == nil {
			t.Errorf("Resolve(%q, SudoMode(%d)) succeeded, want policy error", name, uint8(undeclared))
		}
	}
}

func TestZeroPolicyIsNeverSudo(t *testing.T) {
	var p policy.Policy
	if p.Operation() != "" {
		t.Errorf("zero Policy.Operation() = %q, want empty", p.Operation())
	}
	if p.Sudo() != policy.SudoNever {
		t.Errorf("zero Policy.Sudo() = %v, want SudoNever", p.Sudo())
	}
}

// TestPolicyHasNoExportedFields is the mechanical half of the sudo contract:
// with every field unexported and Resolve the only constructor, no package
// outside internal/policy can build a Policy carrying a sudo mode that the
// capability table did not approve.
func TestPolicyHasNoExportedFields(t *testing.T) {
	typ := reflect.TypeOf(policy.Policy{})
	if typ.Kind() != reflect.Struct {
		t.Fatalf("policy.Policy kind = %v, want struct", typ.Kind())
	}
	if typ.NumField() == 0 {
		t.Fatal("policy.Policy has no fields; expected unexported operation and sudo fields")
	}
	for i := range typ.NumField() {
		if f := typ.Field(i); f.IsExported() {
			t.Errorf("policy.Policy has exported field %q; Policy must be constructible only via Resolve", f.Name)
		}
	}
}
