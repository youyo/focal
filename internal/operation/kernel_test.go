package operation

import (
	"reflect"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// kernel_test.go uses the journalTest* helpers declared in logs_test.go: the
// two journal readers take the same ceilings and run against the same
// Recorder, and duplicating the setup would let the two drift apart.

func TestKernelName(t *testing.T) {
	kernel, err := NewKernel(vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
	if err != nil {
		t.Fatalf("NewKernel() = %v", err)
	}
	if kernel.Name() != "kernel" {
		t.Errorf("Name() = %q, want kernel", kernel.Name())
	}
	var _ Operation = kernel
}

// TestKernelHoldsOnlyTypedParameters pins that kernel carries no unit name:
// journalctl -k selects the messages, so there is nothing for a caller to
// name and nothing of theirs in the argv but the two limits.
func TestKernelHoldsOnlyTypedParameters(t *testing.T) {
	ty := reflect.TypeOf(Kernel{})
	want := []struct {
		name string
		typ  reflect.Type
	}{
		{"since", reflect.TypeOf(vo.Duration{})},
		{"lines", reflect.TypeOf(vo.LineLimit{})},
	}
	if ty.NumField() != len(want) {
		t.Fatalf("Kernel has %d fields, want %d (since and lines only)", ty.NumField(), len(want))
	}
	for i, w := range want {
		f := ty.Field(i)
		if f.Name != w.name || f.Type != w.typ {
			t.Errorf("field %d = %s %s, want %s %s", i, f.Name, f.Type, w.name, w.typ)
		}
	}
}

func TestKernelGoldenArgvWithDefaults(t *testing.T) {
	kernel, err := NewKernel(vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
	if err != nil {
		t.Fatalf("NewKernel() = %v", err)
	}

	rec, _ := journalTestRun(t, kernel, journalTestPolicy(t, "kernel", policy.SudoNever), ssh.Output{}, nil)

	program, args := journalTestArgv(t, rec)
	if program != "journalctl" {
		t.Errorf("Program() = %q, want journalctl", program)
	}
	want := []string{"-k", "--since=-1h", "-n", "200", "--no-pager", "--output=short-iso"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("Args() = %q, want %q", args, want)
	}
}

func TestKernelGoldenArgvWithExplicitSinceAndLines(t *testing.T) {
	kernel, err := NewKernel(
		journalTestSince(t, "2h"),
		journalTestLines(t, "500"),
		journalTestMaxSince(t), journalTestMaxLines(t),
	)
	if err != nil {
		t.Fatalf("NewKernel() = %v", err)
	}

	rec, _ := journalTestRun(t, kernel, journalTestPolicy(t, "kernel", policy.SudoNever), ssh.Output{}, nil)

	_, args := journalTestArgv(t, rec)
	want := []string{"-k", "--since=-2h", "-n", "500", "--no-pager", "--output=short-iso"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("Args() = %q, want %q", args, want)
	}
}

// TestKernelDefaultsClampedToCeilings pins that the 1h default is narrowed
// rather than rejected when the administrator allows less.
func TestKernelDefaultsClampedToCeilings(t *testing.T) {
	kernel, err := NewKernel(
		vo.Duration{},
		vo.LineLimit{},
		journalTestSince(t, "10m"),
		journalTestLines(t, "25"),
	)
	if err != nil {
		t.Fatalf("NewKernel() = %v", err)
	}

	rec, _ := journalTestRun(t, kernel, journalTestPolicy(t, "kernel", policy.SudoNever), ssh.Output{}, nil)

	_, args := journalTestArgv(t, rec)
	want := []string{"-k", "--since=-10m", "-n", "25", "--no-pager", "--output=short-iso"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("Args() = %q, want %q", args, want)
	}
}

func TestKernelRejectsAboveCeilings(t *testing.T) {
	cases := []struct {
		name           string
		since, lines   string
		field, allowed string
	}{
		{name: "since above max_since", since: "30d", lines: "200", field: "since", allowed: "24h"},
		{name: "lines above max_lines", since: "1h", lines: "100000", field: "lines", allowed: "1000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewKernel(
				journalTestSince(t, c.since),
				journalTestLines(t, c.lines),
				journalTestMaxSince(t), journalTestMaxLines(t),
			)
			if err == nil {
				t.Fatalf("NewKernel(%s, %s) = nil error, want a rejection rather than a silent narrowing", c.since, c.lines)
			}
			if err.Kind != result.KindValidation {
				t.Errorf("Kind = %q, want validation", err.Kind)
			}
			if err.Field != c.field {
				t.Errorf("Field = %q, want %q", err.Field, c.field)
			}
			if !strings.Contains(strings.Join(err.Allowed, " "), c.allowed) {
				t.Errorf("Allowed = %q, want it to name the ceiling %q", err.Allowed, c.allowed)
			}
		})
	}
}

func TestKernelSudoModeComesFromThePolicy(t *testing.T) {
	cases := []struct {
		name string
		mode policy.SudoMode
	}{
		{name: "never", mode: policy.SudoNever},
		{name: "auto", mode: policy.SudoAuto},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kernel, err := NewKernel(vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
			if err != nil {
				t.Fatalf("NewKernel() = %v", err)
			}

			rec, _ := journalTestRun(t, kernel, journalTestPolicy(t, "kernel", c.mode), ssh.Output{}, nil)

			calls := rec.Calls()
			if len(calls) != 1 {
				t.Fatalf("recorded %d calls, want 1", len(calls))
			}
			if got := calls[0].Command.Sudo(); got != c.mode {
				t.Errorf("Command.Sudo() = %v, want %v", got, c.mode)
			}
			if calls[0].SudoPrefixed {
				t.Error("SudoPrefixed = true, want false: auto escalates only after an unprivileged attempt is denied")
			}
		})
	}
}

func TestKernelSudoAlwaysIsRefusedByPolicy(t *testing.T) {
	if _, err := policy.Resolve("kernel", policy.SudoAlways); err == nil {
		t.Fatal("policy.Resolve(kernel, always) = nil error, want a policy rejection")
	}
}

func TestKernelEnvelope(t *testing.T) {
	kernel, err := NewKernel(vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
	if err != nil {
		t.Fatalf("NewKernel() = %v", err)
	}
	out := ssh.Output{Stdout: "2026-08-23T09:00:00+0900 web01 kernel: Linux version 6.8.0", ExitCode: 0}

	_, env := journalTestRun(t, kernel, journalTestPolicy(t, "kernel", policy.SudoNever), out, nil)

	if env.Operation != "kernel" {
		t.Errorf("Operation = %q, want kernel", env.Operation)
	}
	if env.Host != "web01" {
		t.Errorf("Host = %q, want web01", env.Host)
	}
	if env.Status != result.StatusOK {
		t.Errorf("Status = %q, want ok", env.Status)
	}
	if env.Stdout != out.Stdout {
		t.Errorf("Stdout = %q, want %q", env.Stdout, out.Stdout)
	}
	if len(env.Parts) != 0 {
		t.Errorf("Parts = %v, want empty: kernel runs a single command", env.Parts)
	}
}

// TestKernelEnvelopeKeepsTruncationVisible pins that a cut-off journal read is
// reported as truncated rather than as a complete answer.
func TestKernelEnvelopeKeepsTruncationVisible(t *testing.T) {
	kernel, err := NewKernel(vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
	if err != nil {
		t.Fatalf("NewKernel() = %v", err)
	}
	out := ssh.Output{Stdout: "partial", ExitCode: -1, Truncated: true}

	_, env := journalTestRun(t, kernel, journalTestPolicy(t, "kernel", policy.SudoNever), out, nil)

	if env.Status != result.StatusTruncated {
		t.Errorf("Status = %q, want truncated", env.Status)
	}
	if !env.Truncated {
		t.Error("Truncated = false, want true")
	}
}
