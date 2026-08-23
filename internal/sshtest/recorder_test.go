package sshtest_test

import (
	"errors"
	"testing"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
	"github.com/youyo/focal/internal/vo"
)

func TestRecorderRecordsWhatItWasAsked(t *testing.T) {
	target := mustTarget(t, "ops@example.com")
	cmd := mustUptime(t, policy.Policy{})

	var rec sshtest.Recorder
	if _, err := rec.Execute(t.Context(), target, cmd); err != nil {
		t.Fatalf("Execute on a zero Recorder: %v", err)
	}

	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(calls))
	}
	if got := calls[0].Target.String(); got != target.String() {
		t.Errorf("recorded target %q, want %q", got, target.String())
	}
	if got := calls[0].Command.Program(); got != "uptime" {
		t.Errorf("recorded program %q, want uptime", got)
	}
	if got := calls[0].Command.Sudo(); got != policy.SudoNever {
		t.Errorf("recorded sudo mode %s, want %s", got, policy.SudoNever)
	}
	if calls[0].SudoPrefixed {
		t.Errorf("a command from the zero Policy was recorded as escalating")
	}
}

// TestRecorderRecordsTheSudoModeFromPolicy is the check operations rely on:
// what reaches the executor is the mode policy.Resolve arrived at, not one the
// caller chose.
func TestRecorderRecordsTheSudoModeFromPolicy(t *testing.T) {
	// "logs" is the one operation whose capability admits a sudo mode.
	p, policyErr := policy.Resolve("logs", policy.SudoAuto)
	if policyErr != nil {
		t.Fatalf("policy.Resolve(logs, auto): %v", policyErr)
	}

	var rec sshtest.Recorder
	if _, err := rec.Execute(t.Context(), mustTarget(t, "example.com"), mustUptime(t, p)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	call := rec.Calls()[0]
	if got := call.Command.Sudo(); got != policy.SudoAuto {
		t.Errorf("recorded sudo mode %s, want %s", got, policy.SudoAuto)
	}
	// SudoAuto starts unprivileged; whether it escalates depends on an exit
	// status the Recorder never produces.
	if call.SudoPrefixed {
		t.Errorf("a SudoAuto command was recorded as escalating on its first attempt")
	}
}

func TestRecorderReturnsTheProgrammedResponse(t *testing.T) {
	want := ssh.Output{Stdout: "up 3 days", Stderr: "a warning", ExitCode: 7, Truncated: true, Duration: 42 * time.Millisecond}
	wantErr := result.ExecutionError("ssh_failed", "ssh client failed")

	var rec sshtest.Recorder
	rec.Respond(want, wantErr)

	got, err := rec.Execute(t.Context(), mustTarget(t, "example.com"), mustUptime(t, policy.Policy{}))
	if got != want {
		t.Errorf("Execute returned %+v, want %+v", got, want)
	}
	var e *result.Error
	if !errors.As(err, &e) || e.Code != "ssh_failed" {
		t.Errorf("Execute returned %v, want the programmed error", err)
	}
}

// TestRecorderCallsAreACopy keeps a test that captured the history from having
// it change underneath it.
func TestRecorderCallsAreACopy(t *testing.T) {
	var rec sshtest.Recorder
	cmd := mustUptime(t, policy.Policy{})
	if _, err := rec.Execute(t.Context(), mustTarget(t, "example.com"), cmd); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	before := rec.Calls()
	if _, err := rec.Execute(t.Context(), mustTarget(t, "other.example.com"), cmd); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(before) != 1 {
		t.Errorf("a previously returned slice grew to %d calls", len(before))
	}
	if len(rec.Calls()) != 2 {
		t.Errorf("the Recorder holds %d calls, want 2", len(rec.Calls()))
	}
}

// TestRecorderRespondSeqAnswersEachCallInOrder is the API a multi-call
// operation (system, inspect) needs from the mock: successive Execute calls
// see successive programmed responses, not the same one repeated.
func TestRecorderRespondSeqAnswersEachCallInOrder(t *testing.T) {
	first := ssh.Output{Stdout: "first", ExitCode: 0}
	second := ssh.Output{Stdout: "second", ExitCode: 0}
	thirdErr := result.ExecutionError("ssh_failed", "third failed")

	var rec sshtest.Recorder
	rec.RespondSeq(
		sshtest.Response{Output: first},
		sshtest.Response{Output: second},
		sshtest.Response{Err: thirdErr},
	)

	target := mustTarget(t, "example.com")
	cmd := mustUptime(t, policy.Policy{})

	got1, err1 := rec.Execute(t.Context(), target, cmd)
	if err1 != nil || got1 != first {
		t.Fatalf("call 1 = %+v, %v, want %+v, nil", got1, err1, first)
	}
	got2, err2 := rec.Execute(t.Context(), target, cmd)
	if err2 != nil || got2 != second {
		t.Fatalf("call 2 = %+v, %v, want %+v, nil", got2, err2, second)
	}
	got3, err3 := rec.Execute(t.Context(), target, cmd)
	var e3 *result.Error
	if !errors.As(err3, &e3) || e3.Code != "ssh_failed" {
		t.Fatalf("call 3 error = %v, want ssh_failed", err3)
	}
	if got3.Stdout != "" {
		t.Fatalf("call 3 output = %+v, want zero Output alongside the error", got3)
	}

	if len(rec.Calls()) != 3 {
		t.Fatalf("recorded %d calls, want 3", len(rec.Calls()))
	}
}

// TestRecorderRespondSeqRepeatsLastResponseOnceExhausted keeps a test that
// calls Execute more times than it programmed from panicking or wrapping
// around to the first response.
func TestRecorderRespondSeqRepeatsLastResponseOnceExhausted(t *testing.T) {
	last := ssh.Output{Stdout: "last", ExitCode: 0}

	var rec sshtest.Recorder
	rec.RespondSeq(sshtest.Response{Output: last})

	target := mustTarget(t, "example.com")
	cmd := mustUptime(t, policy.Policy{})

	for i := 0; i < 3; i++ {
		got, err := rec.Execute(t.Context(), target, cmd)
		if err != nil || got != last {
			t.Fatalf("call %d = %+v, %v, want %+v, nil", i, got, err, last)
		}
	}
}

// TestRecorderRespondReplacesRespondSeq documents that the two programming
// APIs are not additive: the most recent call wins, so a test cannot leave a
// RespondSeq behind and have a later Respond silently ignored.
func TestRecorderRespondReplacesRespondSeq(t *testing.T) {
	var rec sshtest.Recorder
	rec.RespondSeq(sshtest.Response{Output: ssh.Output{Stdout: "seq"}})
	rec.Respond(ssh.Output{Stdout: "single"}, nil)

	got, err := rec.Execute(t.Context(), mustTarget(t, "example.com"), mustUptime(t, policy.Policy{}))
	if err != nil || got.Stdout != "single" {
		t.Fatalf("Execute = %+v, %v, want Stdout=single, nil", got, err)
	}
}

func mustTarget(t *testing.T, s string) vo.Target {
	t.Helper()
	target, err := vo.ParseTarget(s)
	if err != nil {
		t.Fatalf("ParseTarget(%q): %v", s, err)
	}
	return target
}

func mustUptime(t *testing.T, p policy.Policy) ssh.Command {
	t.Helper()
	cmd, err := ssh.UptimeCommand(p)
	if err != nil {
		t.Fatalf("UptimeCommand: %v", err)
	}
	return cmd
}
