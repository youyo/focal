package ssh

import (
	"context"
	"errors"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// Executor runs one already-validated Command against one already-validated
// Target. It deliberately does not take a policy.Policy: by the time a Command
// exists, policy.Resolve has already decided its sudo mode and newCommand has
// burned that decision into it, so Command.Sudo() is the executor's only input
// on the subject. An executor that consulted the policy again would be a second
// place where privilege is decided.
type Executor interface {
	Execute(ctx context.Context, target vo.Target, cmd Command) (Output, error)
}

// Output is everything one Execute observed. Stdout and Stderr are the bytes
// captured up to the executor's output ceiling; Truncated says whether more was
// produced and dropped. ExitCode is the remote command's status, or -1 when no
// status was reached (the process was killed on truncation, on timeout, or by a
// signal). Duration covers the whole Execute, including the sudo retry when one
// happened.
//
// A non-zero ExitCode is not an error: the command ran and said no. Execute
// returns an error only when no exit status was produced at all.
type Output struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
	Duration  time.Duration
}

// PrefixesSudo reports whether the executor puts "sudo -n" in front of cmd on
// its first attempt. Only SudoAlways does; SudoAuto starts unprivileged and may
// escalate afterwards, and SudoNever never escalates at all. The mock executor
// in internal/sshtest records the prefix through this function so that a change
// to the rule cannot leave the mock asserting the old one.
func PrefixesSudo(cmd Command) bool { return cmd.Sudo() == policy.SudoAlways }

// codeTimeout is the Error.Code Execute uses when the timeout elapsed. It is
// unexported because TimedOut is the supported way to ask.
const codeTimeout = "timeout"

// TimedOut reports whether err is the execution error Execute returns when the
// command did not finish within the configured timeout. Callers building a
// result.Envelope use it to choose result.StatusTimeout over StatusFailed.
func TimedOut(err error) bool {
	var e *result.Error
	return errors.As(err, &e) && e.Kind == result.KindExecution && e.Code == codeTimeout
}
