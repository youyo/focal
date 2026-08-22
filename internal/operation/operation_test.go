// Package operation_test is an external test package: it exercises
// operation.Operation the way a real implementation and its caller would,
// through internal/ssh's exported factories and internal/sshtest's Recorder,
// never through anything unexported in internal/operation. That is the
// mechanical proof that M3's ten operations can be implemented outside
// internal/ssh: this package has no special access internal/operation itself
// does not grant.
package operation_test

import (
	"context"
	"testing"
	"time"

	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
	"github.com/youyo/focal/internal/vo"
)

// fakeOperation stands in for one of M3's operations. It carries no fields:
// no Command, no raw command string, not even the target or the policy it
// last ran with. Everything it needs for one Execute arrives as that call's
// arguments, which is the shape done_criteria requires of a real operation —
// only typed parameters (e.g. a Logs operation would hold Service, Since and
// Lines instead of these zero fields), never a pre-built Command.
type fakeOperation struct{}

var _ operation.Operation = fakeOperation{}

func (fakeOperation) Name() string { return "uptime" }

// Execute is the pattern every M3 operation follows: obtain a Command from a
// factory in internal/ssh, passing along the Policy this call was resolved
// for, run it through the Executor it was given, and translate the result
// into a result.Envelope. It never constructs a Command by hand.
func (o fakeOperation) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	cmd, err := ssh.UptimeCommand(p)
	if err != nil {
		return result.Envelope{}, err
	}
	out, execErr := ex.Execute(ctx, t, cmd)
	if execErr != nil {
		return result.Envelope{}, execErr
	}
	status := result.StatusOK
	if out.ExitCode != 0 {
		status = result.StatusFailed
	}
	return result.Envelope{
		Operation:  o.Name(),
		Host:       t.String(),
		Status:     status,
		ExitCode:   out.ExitCode,
		DurationMs: out.Duration.Milliseconds(),
		Truncated:  out.Truncated,
		Stdout:     out.Stdout,
		Stderr:     out.Stderr,
	}, nil
}

func mustTarget(t *testing.T, s string) vo.Target {
	t.Helper()
	target, err := vo.ParseTarget(s)
	if err != nil {
		t.Fatalf("ParseTarget(%q): %v", s, err)
	}
	return target
}

// TestExecuteGoesThroughPolicyAndRecordsCommand is the M3-acceptance
// contract test: a fake operation that never builds a Command itself still
// produces a Command the Recorder can see, obtained by handing policy.Resolve's
// Policy to ssh.UptimeCommand, and Execute's Envelope reports what the
// Recorder was told to answer.
func TestExecuteGoesThroughPolicyAndRecordsCommand(t *testing.T) {
	p, polErr := policy.Resolve("system", policy.SudoNever)
	if polErr != nil {
		t.Fatalf("policy.Resolve: %v", polErr)
	}
	target := mustTarget(t, "example.com")

	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{Stdout: "up 3 days", ExitCode: 0, Duration: 1500 * time.Millisecond}, nil)

	op := fakeOperation{}
	env, err := op.Execute(context.Background(), rec, target, p)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("got %d recorded calls, want 1", len(calls))
	}
	cmd := calls[0].Command
	if cmd.Program() != "uptime" {
		t.Errorf("Command.Program() = %q, want %q", cmd.Program(), "uptime")
	}
	if len(cmd.Args()) != 0 {
		t.Errorf("Command.Args() = %v, want empty", cmd.Args())
	}
	if cmd.Sudo() != policy.SudoNever {
		t.Errorf("Command.Sudo() = %v, want SudoNever", cmd.Sudo())
	}

	if env.Operation != "uptime" {
		t.Errorf("Envelope.Operation = %q, want %q", env.Operation, "uptime")
	}
	if env.Host != target.String() {
		t.Errorf("Envelope.Host = %q, want %q", env.Host, target.String())
	}
	if env.Status != result.StatusOK {
		t.Errorf("Envelope.Status = %q, want %q", env.Status, result.StatusOK)
	}
	if env.ExitCode != 0 {
		t.Errorf("Envelope.ExitCode = %d, want 0", env.ExitCode)
	}
	if env.DurationMs != 1500 {
		t.Errorf("Envelope.DurationMs = %d, want 1500", env.DurationMs)
	}
}

// TestSudoContractAutoFromResolve is case (a) of the sudo contract: a Command
// built from the Policy policy.Resolve("logs", SudoAuto) returns carries that
// mode through to Command.Sudo().
func TestSudoContractAutoFromResolve(t *testing.T) {
	p, polErr := policy.Resolve("logs", policy.SudoAuto)
	if polErr != nil {
		t.Fatalf("policy.Resolve: %v", polErr)
	}
	cmd, err := ssh.UptimeCommand(p)
	if err != nil {
		t.Fatalf("ssh.UptimeCommand: %v", err)
	}
	if cmd.Sudo() != policy.SudoAuto {
		t.Errorf("Command.Sudo() = %v, want SudoAuto", cmd.Sudo())
	}
}

// TestSudoContractZeroPolicyNeverEscalates is case (b) of the sudo contract:
// a Command built from the zero Policy carries SudoNever, and the Recorder
// does not observe a sudo prefix for it.
func TestSudoContractZeroPolicyNeverEscalates(t *testing.T) {
	var zero policy.Policy
	cmd, err := ssh.UptimeCommand(zero)
	if err != nil {
		t.Fatalf("ssh.UptimeCommand: %v", err)
	}
	if cmd.Sudo() != policy.SudoNever {
		t.Fatalf("Command.Sudo() = %v, want SudoNever", cmd.Sudo())
	}

	rec := &sshtest.Recorder{}
	target := mustTarget(t, "example.com")
	if _, execErr := rec.Execute(context.Background(), target, cmd); execErr != nil {
		t.Fatalf("Recorder.Execute: %v", execErr)
	}

	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("got %d recorded calls, want 1", len(calls))
	}
	if calls[0].SudoPrefixed {
		t.Errorf("SudoPrefixed = true, want false for a zero-Policy Command")
	}
}
