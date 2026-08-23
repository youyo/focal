package operation_test

import (
	"context"
	"testing"

	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
)

func mustPolicy(t *testing.T, name string, mode policy.SudoMode) policy.Policy {
	t.Helper()
	p, err := policy.Resolve(name, mode)
	if err != nil {
		t.Fatalf("policy.Resolve(%q): %v", name, err)
	}
	return p
}

// TestSystemGoldenArgv is the golden argv test issue #9 requires for
// "system": five commands, run in a fixed order, whose Program/Args match
// the confirmed argv exactly.
func TestSystemGoldenArgv(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)
	p := mustPolicy(t, "system", policy.SudoNever)

	_, err := operation.System{}.Execute(context.Background(), rec, mustTarget(t, "example.com"), p)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	calls := rec.Calls()
	if len(calls) != 5 {
		t.Fatalf("got %d calls, want 5", len(calls))
	}

	want := []struct {
		program string
		args    []string
	}{
		{"uname", []string{"-a"}},
		{"hostname", nil},
		{"uptime", nil},
		{"date", []string{"--iso-8601=seconds"}},
		{"cat", []string{"/etc/os-release"}},
	}
	for i, w := range want {
		if got := calls[i].Command.Program(); got != w.program {
			t.Errorf("call %d Program() = %q, want %q", i, got, w.program)
		}
		if got := calls[i].Command.Args(); !argsEqual(got, w.args) {
			t.Errorf("call %d Args() = %v, want %v", i, got, w.args)
		}
	}
}

// TestSystemAllCallsNeverSudo confirms system's capability (SudoNever) means
// none of its five commands ever gets a sudo prefix.
func TestSystemAllCallsNeverSudo(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)
	p := mustPolicy(t, "system", policy.SudoNever)

	sys := operation.System{}
	if _, err := sys.Execute(context.Background(), rec, mustTarget(t, "example.com"), p); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for i, call := range rec.Calls() {
		if call.SudoPrefixed {
			t.Errorf("call %d SudoPrefixed = true, want false", i)
		}
	}
}

// TestSystemPartNamesAndOrder checks Envelope.Parts carries the fixed Part
// names in the order the Recorder saw the calls, not some other ordering.
func TestSystemPartNamesAndOrder(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)
	p := mustPolicy(t, "system", policy.SudoNever)

	env, err := operation.System{}.Execute(context.Background(), rec, mustTarget(t, "example.com"), p)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	wantNames := []string{"uname", "hostname", "uptime", "date", "os-release"}
	if len(env.Parts) != len(wantNames) {
		t.Fatalf("got %d parts, want %d", len(env.Parts), len(wantNames))
	}
	for i, name := range wantNames {
		if env.Parts[i].Name != name {
			t.Errorf("Parts[%d].Name = %q, want %q", i, env.Parts[i].Name, name)
		}
	}
}

// TestSystemOneFailureKeepsTheRest is the partial-result contract: when one
// of the five commands fails, the remaining four still run, their stdout is
// kept, and the Envelope-level Status reflects the failure.
func TestSystemOneFailureKeepsTheRest(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.RespondSeq(
		sshtest.Response{Output: ssh.Output{Stdout: "Linux host 6.1", ExitCode: 0}},
		sshtest.Response{Output: ssh.Output{ExitCode: 1, Stderr: "permission denied"}},
		sshtest.Response{Output: ssh.Output{Stdout: "up 3 days", ExitCode: 0}},
		sshtest.Response{Output: ssh.Output{Stdout: "2026-08-23T00:00:00+00:00", ExitCode: 0}},
		sshtest.Response{Output: ssh.Output{Stdout: "NAME=focal", ExitCode: 0}},
	)
	p := mustPolicy(t, "system", policy.SudoNever)

	env, err := operation.System{}.Execute(context.Background(), rec, mustTarget(t, "example.com"), p)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(rec.Calls()) != 5 {
		t.Fatalf("got %d calls, want 5 (a failing step must not stop the rest)", len(rec.Calls()))
	}
	if env.Status != result.StatusFailed {
		t.Errorf("Envelope.Status = %q, want failed", env.Status)
	}
	if env.Parts[0].Status != result.StatusOK || env.Parts[0].Stdout != "Linux host 6.1" {
		t.Errorf("Parts[0] = %+v, want the uname stdout preserved", env.Parts[0])
	}
	if env.Parts[1].Status != result.StatusFailed {
		t.Errorf("Parts[1].Status = %q, want failed", env.Parts[1].Status)
	}
	if env.Parts[2].Status != result.StatusOK || env.Parts[2].Stdout != "up 3 days" {
		t.Errorf("Parts[2] = %+v, want the uptime stdout preserved", env.Parts[2])
	}
}

func argsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
