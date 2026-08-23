package operation

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
	"github.com/youyo/focal/internal/vo"
)

// The helpers below are shared by logs_test.go and kernel_test.go and carry a
// journal prefix so they cannot collide with the helpers other operations'
// tests declare in this same package.

func journalTestTarget(t *testing.T) vo.Target {
	t.Helper()
	target, err := vo.ParseTarget("web01")
	if err != nil {
		t.Fatalf("ParseTarget(web01) = %v", err)
	}
	return target
}

func journalTestService(t *testing.T, v string) vo.ServiceName {
	t.Helper()
	name, err := vo.ParseServiceName(v)
	if err != nil {
		t.Fatalf("ParseServiceName(%q) = %v", v, err)
	}
	return name
}

func journalTestSince(t *testing.T, v string) vo.Duration {
	t.Helper()
	d, err := vo.ParseDuration(v)
	if err != nil {
		t.Fatalf("ParseDuration(%q) = %v", v, err)
	}
	return d
}

func journalTestLines(t *testing.T, v string) vo.LineLimit {
	t.Helper()
	l, err := vo.ParseLineLimit(v)
	if err != nil {
		t.Fatalf("ParseLineLimit(%q) = %v", v, err)
	}
	return l
}

func journalTestPolicy(t *testing.T, operation string, sudo policy.SudoMode) policy.Policy {
	t.Helper()
	p, err := policy.Resolve(operation, sudo)
	if err != nil {
		t.Fatalf("policy.Resolve(%q, %v) = %v", operation, sudo, err)
	}
	return p
}

// journalTestMaxSince and journalTestMaxLines are the ceilings a test that
// does not care about clamping passes: config's defaults for both journal
// readers.
func journalTestMaxSince(t *testing.T) vo.Duration {
	t.Helper()
	return journalTestSince(t, "24h")
}

func journalTestMaxLines(t *testing.T) vo.LineLimit {
	t.Helper()
	return journalTestLines(t, "1000")
}

// journalTestRun executes op against a Recorder and returns the recorder plus
// the Envelope, so a test can assert on the argv that reached the executor and
// on what came back.
func journalTestRun(t *testing.T, op Operation, p policy.Policy, out ssh.Output, execErr error) (*sshtest.Recorder, result.Envelope) {
	t.Helper()
	rec := &sshtest.Recorder{}
	rec.Respond(out, execErr)
	env, err := op.Execute(context.Background(), rec, journalTestTarget(t), p)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	return rec, env
}

func journalTestArgv(t *testing.T, rec *sshtest.Recorder) (string, []string) {
	t.Helper()
	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want exactly 1", len(calls))
	}
	return calls[0].Command.Program(), calls[0].Command.Args()
}

func TestLogsName(t *testing.T) {
	logs, err := NewLogs(journalTestService(t, "nginx"), vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
	if err != nil {
		t.Fatalf("NewLogs() = %v", err)
	}
	if logs.Name() != "logs" {
		t.Errorf("Name() = %q, want logs", logs.Name())
	}
	var _ Operation = logs
}

func TestLogsHoldsOnlyTypedParameters(t *testing.T) {
	ty := reflect.TypeOf(Logs{})
	want := []struct {
		name string
		typ  reflect.Type
	}{
		{"service", reflect.TypeOf(vo.ServiceName{})},
		{"since", reflect.TypeOf(vo.Duration{})},
		{"lines", reflect.TypeOf(vo.LineLimit{})},
	}
	if ty.NumField() != len(want) {
		t.Fatalf("Logs has %d fields, want %d (only typed parameters, no Command)", ty.NumField(), len(want))
	}
	for i, w := range want {
		f := ty.Field(i)
		if f.Name != w.name || f.Type != w.typ {
			t.Errorf("field %d = %s %s, want %s %s", i, f.Name, f.Type, w.name, w.typ)
		}
	}
}

func TestLogsGoldenArgvWithDefaults(t *testing.T) {
	logs, err := NewLogs(journalTestService(t, "nginx"), vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
	if err != nil {
		t.Fatalf("NewLogs() = %v", err)
	}

	rec, _ := journalTestRun(t, logs, journalTestPolicy(t, "logs", policy.SudoNever), ssh.Output{}, nil)

	program, args := journalTestArgv(t, rec)
	if program != "journalctl" {
		t.Errorf("Program() = %q, want journalctl", program)
	}
	want := []string{"-u", "nginx.service", "--since=-30m", "-n", "200", "--no-pager", "--output=short-iso"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("Args() = %q, want %q", args, want)
	}
}

func TestLogsGoldenArgvWithExplicitSinceAndLines(t *testing.T) {
	logs, err := NewLogs(
		journalTestService(t, "nginx"),
		journalTestSince(t, "15m"),
		journalTestLines(t, "50"),
		journalTestMaxSince(t), journalTestMaxLines(t),
	)
	if err != nil {
		t.Fatalf("NewLogs() = %v", err)
	}

	rec, _ := journalTestRun(t, logs, journalTestPolicy(t, "logs", policy.SudoNever), ssh.Output{}, nil)

	_, args := journalTestArgv(t, rec)
	want := []string{"-u", "nginx.service", "--since=-15m", "-n", "50", "--no-pager", "--output=short-iso"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("Args() = %q, want %q", args, want)
	}
}

// TestLogsUnitNameNormalization pins that logs goes through exec.go's single
// normalization point: a bare name gains ".service" and a name that already
// names a unit type is left alone.
func TestLogsUnitNameNormalization(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nginx", "nginx.service"},
		{"nginx.service", "nginx.service"},
		{"foo@bar.service", "foo@bar.service"},
		{"docker.socket", "docker.socket"},
		{"logrotate.timer", "logrotate.timer"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			logs, err := NewLogs(journalTestService(t, c.in), vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
			if err != nil {
				t.Fatalf("NewLogs() = %v", err)
			}

			rec, _ := journalTestRun(t, logs, journalTestPolicy(t, "logs", policy.SudoNever), ssh.Output{}, nil)

			_, args := journalTestArgv(t, rec)
			if len(args) < 2 || args[0] != "-u" {
				t.Fatalf("Args() = %q, want -u first", args)
			}
			if args[1] != c.want {
				t.Errorf("unit = %q, want %q", args[1], c.want)
			}
		})
	}
}

// TestLogsDefaultsClampedToCeilings covers the case where the built-in
// defaults are wider than what the administrator allows: the ceiling wins, and
// the request is not rejected for a value the caller never asked for.
func TestLogsDefaultsClampedToCeilings(t *testing.T) {
	logs, err := NewLogs(
		journalTestService(t, "nginx"),
		vo.Duration{},
		vo.LineLimit{},
		journalTestSince(t, "5m"),
		journalTestLines(t, "20"),
	)
	if err != nil {
		t.Fatalf("NewLogs() = %v", err)
	}

	rec, _ := journalTestRun(t, logs, journalTestPolicy(t, "logs", policy.SudoNever), ssh.Output{}, nil)

	_, args := journalTestArgv(t, rec)
	want := []string{"-u", "nginx.service", "--since=-5m", "-n", "20", "--no-pager", "--output=short-iso"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("Args() = %q, want %q", args, want)
	}
}

func TestLogsRejectsAboveCeilings(t *testing.T) {
	cases := []struct {
		name           string
		since, lines   string
		field, allowed string
	}{
		{name: "since above max_since", since: "7d", lines: "200", field: "since", allowed: "24h"},
		{name: "lines above max_lines", since: "30m", lines: "5000", field: "lines", allowed: "1000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewLogs(
				journalTestService(t, "nginx"),
				journalTestSince(t, c.since),
				journalTestLines(t, c.lines),
				journalTestMaxSince(t), journalTestMaxLines(t),
			)
			if err == nil {
				t.Fatalf("NewLogs(%s, %s) = nil error, want a rejection: a value above the ceiling is never rounded down", c.since, c.lines)
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

// TestLogsCeilingComparisonIsNumeric fails for any implementation that
// compares the rendered strings instead of the values behind them: "30d" sorts
// before "7d" and "1000" before "999", so a lexicographic check would let both
// over-ceiling requests through.
func TestLogsCeilingComparisonIsNumeric(t *testing.T) {
	t.Run("30d is above a 7d ceiling", func(t *testing.T) {
		_, err := NewLogs(
			journalTestService(t, "nginx"),
			journalTestSince(t, "30d"),
			journalTestLines(t, "200"),
			journalTestSince(t, "7d"),
			journalTestLines(t, "1000"),
		)
		if err == nil || err.Field != "since" {
			t.Fatalf("NewLogs(since=30d, max_since=7d) = %v, want a rejection on since", err)
		}
	})
	t.Run("7d is below a 30d ceiling", func(t *testing.T) {
		_, err := NewLogs(
			journalTestService(t, "nginx"),
			journalTestSince(t, "7d"),
			journalTestLines(t, "200"),
			journalTestSince(t, "30d"),
			journalTestLines(t, "1000"),
		)
		if err != nil {
			t.Fatalf("NewLogs(since=7d, max_since=30d) = %v, want it accepted", err)
		}
	})
	t.Run("1000 lines is above a 999 ceiling", func(t *testing.T) {
		_, err := NewLogs(
			journalTestService(t, "nginx"),
			journalTestSince(t, "30m"),
			journalTestLines(t, "1000"),
			journalTestSince(t, "24h"),
			journalTestLines(t, "999"),
		)
		if err == nil || err.Field != "lines" {
			t.Fatalf("NewLogs(lines=1000, max_lines=999) = %v, want a rejection on lines", err)
		}
	})
}

// TestLogsNoCeilingLeavesTheDefaultsAlone covers the zero-valued ceiling: an
// administrator who set neither limit gets the built-in defaults, and the
// value objects' own bounds stay the only cap.
func TestLogsNoCeilingLeavesTheDefaultsAlone(t *testing.T) {
	logs, err := NewLogs(journalTestService(t, "nginx"), vo.Duration{}, vo.LineLimit{}, vo.Duration{}, vo.LineLimit{})
	if err != nil {
		t.Fatalf("NewLogs() = %v", err)
	}

	rec, _ := journalTestRun(t, logs, journalTestPolicy(t, "logs", policy.SudoNever), ssh.Output{}, nil)

	_, args := journalTestArgv(t, rec)
	want := []string{"-u", "nginx.service", "--since=-30m", "-n", "200", "--no-pager", "--output=short-iso"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("Args() = %q, want %q", args, want)
	}
}

func TestLogsSudoModeComesFromThePolicy(t *testing.T) {
	cases := []struct {
		name string
		mode policy.SudoMode
	}{
		{name: "never", mode: policy.SudoNever},
		{name: "auto", mode: policy.SudoAuto},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs, err := NewLogs(journalTestService(t, "nginx"), vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
			if err != nil {
				t.Fatalf("NewLogs() = %v", err)
			}

			rec, _ := journalTestRun(t, logs, journalTestPolicy(t, "logs", c.mode), ssh.Output{}, nil)

			calls := rec.Calls()
			if len(calls) != 1 {
				t.Fatalf("recorded %d calls, want 1", len(calls))
			}
			if got := calls[0].Command.Sudo(); got != c.mode {
				t.Errorf("Command.Sudo() = %v, want %v", got, c.mode)
			}
			if calls[0].SudoPrefixed {
				t.Error("SudoPrefixed = true, want false: auto starts unprivileged and escalates only on the exit status")
			}
		})
	}
}

// TestLogsSudoAlwaysIsRefusedByPolicy pins that the always mode never reaches
// an operation at all: policy.Resolve rejects it for logs, so there is no
// Policy a Logs could be executed under that would prefix sudo from the start.
func TestLogsSudoAlwaysIsRefusedByPolicy(t *testing.T) {
	if _, err := policy.Resolve("logs", policy.SudoAlways); err == nil {
		t.Fatal("policy.Resolve(logs, always) = nil error, want a policy rejection")
	}
}

// TestLogsRejectsInjectionServiceNames walks issue #15's reject list through
// the input path a logs request actually takes. Every rejected name is refused
// by vo before a Logs exists, and the zero value left behind by an ignored
// rejection is refused by NewLogs, so neither reaches an argv.
func TestLogsRejectsInjectionServiceNames(t *testing.T) {
	rejected := []string{
		"nginx;id",
		"$(id)",
		"`id`",
		"nginx|cat",
		"../../etc/passwd",
		"nginx service",
		"nginx\nid",
		"nginx\x00id",
		"-u",
	}
	for _, name := range rejected {
		t.Run(name, func(t *testing.T) {
			_, err := vo.ParseServiceName(name)
			if err == nil {
				t.Fatalf("ParseServiceName(%q) = nil error, want a validation rejection", name)
			}
			if err.Kind != result.KindValidation {
				t.Errorf("Kind = %q, want validation", err.Kind)
			}
		})
	}

	accepted := []string{"nginx", "nginx.service", "foo@bar.service"}
	for _, name := range accepted {
		t.Run("accepted/"+name, func(t *testing.T) {
			if _, err := vo.ParseServiceName(name); err != nil {
				t.Fatalf("ParseServiceName(%q) = %v, want it accepted", name, err)
			}
		})
	}
}

func TestLogsRequiresAParsedServiceName(t *testing.T) {
	_, err := NewLogs(vo.ServiceName{}, vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
	if err == nil {
		t.Fatal("NewLogs(zero service name) = nil error, want a validation rejection")
	}
	if err.Kind != result.KindValidation || err.Field != "service" {
		t.Errorf("error = %+v, want a validation error on the service field", err)
	}
}

func TestLogsEnvelope(t *testing.T) {
	logs, err := NewLogs(journalTestService(t, "nginx"), vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
	if err != nil {
		t.Fatalf("NewLogs() = %v", err)
	}
	out := ssh.Output{Stdout: "2026-08-23T09:00:00+0900 web01 nginx[1]: started", ExitCode: 0}

	_, env := journalTestRun(t, logs, journalTestPolicy(t, "logs", policy.SudoNever), out, nil)

	if env.Operation != "logs" {
		t.Errorf("Operation = %q, want logs", env.Operation)
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
		t.Errorf("Parts = %v, want empty: logs runs a single command", env.Parts)
	}
}

func TestLogsEnvelopeReportsANonZeroExitAsFailed(t *testing.T) {
	logs, err := NewLogs(journalTestService(t, "nginx"), vo.Duration{}, vo.LineLimit{}, journalTestMaxSince(t), journalTestMaxLines(t))
	if err != nil {
		t.Fatalf("NewLogs() = %v", err)
	}
	out := ssh.Output{Stderr: "Failed to add match: Invalid argument", ExitCode: 1}

	_, env := journalTestRun(t, logs, journalTestPolicy(t, "logs", policy.SudoNever), out, nil)

	if env.Status != result.StatusFailed {
		t.Errorf("Status = %q, want failed", env.Status)
	}
	if env.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", env.ExitCode)
	}
	if env.Stderr != out.Stderr {
		t.Errorf("Stderr = %q, want %q", env.Stderr, out.Stderr)
	}
}

// TestJournalSourcesOwnNeitherConfigNorTheServiceSuffix reads logs.go and
// kernel.go themselves. The constructors take their ceilings as arguments, so
// neither file may import internal/config; and unit-name normalization belongs
// to exec.go, so neither file may carry a ".service" literal of its own.
func TestJournalSourcesOwnNeitherConfigNorTheServiceSuffix(t *testing.T) {
	for _, file := range []string{"logs.go", "kernel.go"} {
		t.Run(file, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", file, err)
			}
			for _, imp := range f.Imports {
				if strings.Contains(imp.Path.Value, "internal/config") {
					t.Errorf("%s imports %s, want the ceilings passed in as arguments instead", file, imp.Path.Value)
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if strings.Contains(lit.Value, ".service") {
					t.Errorf("%s contains the string literal %s, want unit normalization left to exec.go", file, lit.Value)
				}
				return true
			})
		})
	}
}
