package ssh

import (
	"strconv"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/policy"
)

// The retry lives inside one Execute call, so an Executor-level mock cannot see
// it: from the outside, an escalated SudoAuto command is indistinguishable from
// one that succeeded unprivileged. These tests therefore run the stand-in ssh
// installed by installFakeSSH and read the argv of every invocation, which is
// the only place the decision is observable.

// TestSudoAutoRetriesOnlyOnTheDeclaredExitCodes is the central claim: what
// makes Focal escalate is a number from sudoRetryExitCodes and nothing else.
func TestSudoAutoRetriesOnlyOnTheDeclaredExitCodes(t *testing.T) {
	for _, tc := range []struct {
		code  int
		retry bool
	}{
		{1, true},
		{13, true},
		{77, true},
		{126, true},

		{0, false},
		{2, false},
		// 255 is ssh's own "the connection failed". Escalating it would
		// only make a second connection that fails the same way.
		{255, false},
		{127, false},
	} {
		t.Run("exit "+strconv.Itoa(tc.code), func(t *testing.T) {
			fake := installFakeSSH(t)
			// The second attempt succeeds, so a retry that happened
			// is visible in the returned exit code as well as in the
			// invocation log.
			t.Setenv(fakeSSHExitsEnv, strconv.Itoa(tc.code)+",0")
			ex := newTestExecutor(t, testOptions())

			out, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), uptimeCommand(t, policy.SudoAuto))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}

			argvs := fake.argvs(t)
			want := 1
			if tc.retry {
				want = 2
			}
			if len(argvs) != want {
				t.Fatalf("ssh was invoked %d times for exit %d, want %d: %v", len(argvs), tc.code, want, argvs)
			}
			assertArgvHasSudo(t, argvs[0], false)
			if tc.retry {
				assertArgvHasSudo(t, argvs[1], true)
				if out.ExitCode != 0 {
					t.Errorf("ExitCode = %d, want the retry's 0", out.ExitCode)
				}
			} else if out.ExitCode != tc.code {
				t.Errorf("ExitCode = %d, want %d", out.ExitCode, tc.code)
			}
		})
	}
}

// TestSudoAutoRetriesAtMostOnce covers the case where escalation does not help
// either: the second attempt must be the last, or a host that answers 1 to
// everything would be retried forever.
func TestSudoAutoRetriesAtMostOnce(t *testing.T) {
	fake := installFakeSSH(t)
	t.Setenv(fakeSSHExitsEnv, "1")
	ex := newTestExecutor(t, testOptions())

	out, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), uptimeCommand(t, policy.SudoAuto))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	argvs := fake.argvs(t)
	if len(argvs) != 2 {
		t.Fatalf("ssh was invoked %d times, want exactly 2: %v", len(argvs), argvs)
	}
	assertArgvHasSudo(t, argvs[0], false)
	assertArgvHasSudo(t, argvs[1], true)
	if out.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want the second attempt's 1", out.ExitCode)
	}
}

// TestSudoAutoIgnoresOutputContent is the reason the retry is a table of exit
// codes. A remote host controls its own stdout and stderr; if a phrase there
// could trigger escalation, the host would be choosing when Focal runs sudo.
func TestSudoAutoIgnoresOutputContent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  string
		retry bool
	}{
		{"denial wording on a code that does not retry", "2", false},
		{"denial wording on a code that does retry", "1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := installFakeSSH(t)
			t.Setenv(fakeSSHExitsEnv, tc.code+",0")
			t.Setenv(fakeSSHStderrEnv, "sudo: a password is required\nPermission denied\n")
			ex := newTestExecutor(t, testOptions())

			if _, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), uptimeCommand(t, policy.SudoAuto)); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			want := 1
			if tc.retry {
				want = 2
			}
			if got := len(fake.argvs(t)); got != want {
				t.Errorf("ssh was invoked %d times, want %d: the retry followed the output rather than the exit code", got, want)
			}
		})
	}
}

// TestSudoNeverNeverEscalates pins the mode a zero-value Policy produces. A
// command that was not resolved through policy.Resolve carries SudoNever, so
// this is also what a caller who skipped the policy layer would get.
func TestSudoNeverNeverEscalates(t *testing.T) {
	for _, code := range []string{"1", "13", "77", "126"} {
		t.Run("exit "+code, func(t *testing.T) {
			fake := installFakeSSH(t)
			t.Setenv(fakeSSHExitsEnv, code+",0")
			ex := newTestExecutor(t, testOptions())

			if _, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), uptimeCommand(t, policy.SudoNever)); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			argvs := fake.argvs(t)
			if len(argvs) != 1 {
				t.Fatalf("ssh was invoked %d times, want once: %v", len(argvs), argvs)
			}
			assertArgvHasSudo(t, argvs[0], false)
		})
	}
}

// TestSudoAlwaysEscalatesFromTheStart exercises the third mode. No operation's
// capability admits it today, so the Command is built here with a struct
// literal — which only this package can do, and which is precisely the property
// boundary_test.go asserts about every other package.
func TestSudoAlwaysEscalatesFromTheStart(t *testing.T) {
	fake := installFakeSSH(t)
	t.Setenv(fakeSSHExitsEnv, "1,0")
	ex := newTestExecutor(t, testOptions())

	cmd := Command{program: "uptime", sudo: policy.SudoAlways}
	if _, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), cmd); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	argvs := fake.argvs(t)
	if len(argvs) != 1 {
		t.Fatalf("ssh was invoked %d times, want once: a command that already escalated has nothing to retry: %v", len(argvs), argvs)
	}
	assertArgvHasSudo(t, argvs[0], true)
}

// TestSudoRetryExitCodesIsTheWholeCondition guards the table itself, so that a
// later edit widening it is visible as a test change rather than only as a diff
// in a map literal.
func TestSudoRetryExitCodesIsTheWholeCondition(t *testing.T) {
	var got []string
	for code := range sudoRetryExitCodes {
		got = append(got, strconv.Itoa(code))
	}
	if len(got) != 4 {
		t.Fatalf("sudoRetryExitCodes holds %d codes, want 4", len(got))
	}
	for _, want := range []int{1, 13, 77, 126} {
		if _, ok := sudoRetryExitCodes[want]; !ok {
			t.Errorf("sudoRetryExitCodes is missing %d; it holds %s", want, strings.Join(got, ", "))
		}
	}
}
