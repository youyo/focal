package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// Options are the execution parameters an OpenSSH executor runs under. They are
// declared here rather than taken from internal/config so that this package
// does not depend on how Focal is configured — internal/main copies the values
// across. Timeout and MaxOutput are what keep a coding agent from being held by
// a hung connection or a log stream that never ends.
//
// The zero value of IdentityFile and Port means "leave it to ssh(1)", which
// then applies whatever ~/.ssh/config says. Focal never reads that file itself.
type Options struct {
	Timeout      time.Duration
	MaxOutput    int64
	IdentityFile string
	Port         int
}

const (
	minPort = 1
	maxPort = 65535

	// waitDelay bounds how long Wait may block after the context has ended
	// or the process has been killed, in the case where something still
	// holds the output pipes open. Without it a grandchild inheriting the
	// pipe could keep the executor — and the agent waiting on it — alive
	// past its timeout.
	waitDelay = 2 * time.Second
)

// OpenSSH runs commands through the system's ssh(1) client. Everything about
// how the connection is made — HostName, ProxyJump, IdentityAgent,
// ControlMaster, known_hosts — is OpenSSH's business, read from the operator's
// own ~/.ssh/config. Focal contributes only the destination, the fixed options
// below, and the command, and offers no way to pass ProxyCommand, RemoteCommand
// or LocalCommand.
type OpenSSH struct {
	opts Options
}

var _ Executor = (*OpenSSH)(nil)

// NewOpenSSH validates the execution parameters once, at startup, so that a
// misconfigured timeout or port is reported before any host is contacted rather
// than on every command.
func NewOpenSSH(opts Options) (*OpenSSH, *result.Error) {
	switch {
	case opts.Timeout <= 0:
		return nil, result.ValidationError("invalid_timeout", "execution timeout must be positive",
			"execution.timeout", []string{"a positive duration"})
	case opts.MaxOutput <= 0:
		return nil, result.ValidationError("invalid_max_output", "execution max_output must be positive",
			"execution.max_output", []string{"a positive number of bytes"})
	case opts.Port < 0 || opts.Port > maxPort:
		return nil, result.ValidationError("invalid_port",
			fmt.Sprintf("execution port must be between %d and %d, or absent", minPort, maxPort),
			"execution.port", []string{fmt.Sprintf("%d-%d", minPort, maxPort)})
	}
	if err := checkIdentityFile(opts.IdentityFile); err != nil {
		return nil, err
	}
	return &OpenSSH{opts: opts}, nil
}

// checkIdentityFile keeps the -i value from being read as an option or from
// carrying a byte the exec package would refuse mid-argv. Whether the file
// itself is readable and privately permissioned is settled by internal/config
// when it loads the path, so it is not re-checked here.
func checkIdentityFile(path string) *result.Error {
	switch {
	case path == "":
		return nil
	case strings.HasPrefix(path, "-"):
		return result.ValidationError("invalid_identity_file",
			"identity file path must not start with a hyphen", "execution.identity_file",
			[]string{"a path not starting with a hyphen"})
	case strings.ContainsAny(path, "\x00\n\r"):
		return result.ValidationError("invalid_identity_file",
			"identity file path must not contain a NUL, newline or carriage return", "execution.identity_file",
			[]string{"a path without control characters"})
	}
	return nil
}

// sudoRetryExitCodes is the complete set of exit statuses that make a SudoAuto
// command worth one privileged retry. It is a table rather than a condition
// because the alternative — reading stdout or stderr for a phrase like
// "permission denied" — would let remote content and the remote locale decide
// when Focal escalates. Nothing but these numbers can.
//
//	1   the conventional catch-all a permission failure usually lands on
//	13  EACCES surfaced as an exit status
//	77  EX_NOPERM from sysexits.h, used by sudo and some systemd tools
//	126 the shell's "found but not executable/permitted"
//
// 0 and 2 are a success and an ordinary usage error, and 255 is ssh's own
// "the connection failed" — retrying any of them with sudo would only make a
// second pointless connection.
var sudoRetryExitCodes = map[int]struct{}{1: {}, 13: {}, 77: {}, 126: {}}

// Execute runs cmd on target. It resolves ssh(1) through PATH, applies the
// configured timeout to the whole call, and — for a SudoAuto command that came
// back with one of sudoRetryExitCodes — runs it exactly once more with
// "sudo -n". The retry replaces the first attempt's output entirely, so the
// caller sees the result of the attempt that was allowed to work rather than a
// concatenation with a permission error.
func (o *OpenSSH) Execute(ctx context.Context, target vo.Target, cmd Command) (Output, error) {
	if cmd.IsZero() {
		return Output{}, result.ValidationError("empty_command",
			"command was not produced by a factory in internal/ssh", "command", nil)
	}
	// The value object already refused anything outside its grammar; this is
	// the second layer, applied to the exact string that is about to become
	// an argv element. Neither layer is load-bearing on its own.
	if err := assertSafeTarget(target.String()); err != nil {
		return Output{}, err
	}
	// ssh is resolved through PATH with the standard os/exec rules, ErrDot
	// included: a relative match found via "." in PATH is refused rather
	// than run, and Focal never sets GODEBUG=execerrdot=0 to opt out of it.
	sshPath, lookErr := exec.LookPath("ssh")
	if lookErr != nil {
		return Output{}, result.ExecutionError("ssh_not_found", "cannot locate the ssh client: "+lookErr.Error())
	}

	ctx, cancel := context.WithTimeout(ctx, o.opts.Timeout)
	defer cancel()

	started := time.Now()
	out, err := o.run(ctx, sshPath, target.String(), cmd, PrefixesSudo(cmd))
	if err == nil && cmd.Sudo() == policy.SudoAuto {
		if _, retry := sudoRetryExitCodes[out.ExitCode]; retry {
			out, err = o.run(ctx, sshPath, target.String(), cmd, true)
		}
	}
	out.Duration = time.Since(started)
	if err != nil {
		return out, err
	}
	return out, nil
}

// run performs exactly one ssh(1) invocation. It never returns before Wait has
// been called, so no copy goroutine and no child process outlives it.
func (o *OpenSSH) run(ctx context.Context, sshPath, target string, cmd Command, sudo bool) (Output, *result.Error) {
	// #nosec G204 -- the argv is a fixed template: sshPath comes from
	// exec.LookPath, the target passed assertSafeTarget, and the program and
	// arguments passed the allowlists in command.go. No shell is involved,
	// so no token can become anything but one argv element.
	c := exec.CommandContext(ctx, sshPath, buildArgv(o.opts, target, cmd, sudo)...)
	c.WaitDelay = waitDelay

	budget := &outputCap{remaining: o.opts.MaxOutput}
	stdout := &capturedStream{cap: budget}
	stderr := &capturedStream{cap: budget}
	c.Stdout, c.Stderr = stdout, stderr

	if err := c.Start(); err != nil {
		return Output{}, result.ExecutionError("ssh_start_failed", "cannot start the ssh client: "+err.Error())
	}
	budget.armKill(func() { _ = c.Process.Kill() })
	waitErr := c.Wait()

	out := Output{Stdout: stdout.String(), Stderr: stderr.String(), Truncated: budget.wasTruncated(), ExitCode: -1}
	switch {
	case waitErr == nil:
		out.ExitCode = 0
	case ctx.Err() != nil:
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return out, result.ExecutionError(codeTimeout,
				fmt.Sprintf("ssh did not finish within %s", o.opts.Timeout))
		}
		return out, result.ExecutionError("canceled", "ssh was canceled before it finished")
	default:
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return out, result.ExecutionError("ssh_failed", "ssh client failed: "+waitErr.Error())
		}
		// ExitCode is -1 for a signalled process, which is exactly what
		// a truncation kill should report.
		out.ExitCode = exitErr.ExitCode()
	}
	return out, nil
}

// buildArgv renders the arguments to ssh(1), without the program name. It is a
// pure function so that the one thing a remote host actually sees can be
// checked against testdata/argv_golden.txt without running anything.
//
// The "--" goes immediately before the target and nowhere else. Before the
// target it stops a destination from being read as an option; before the remote
// command it would not be a separator at all, since ssh forwards everything
// after the destination verbatim and the remote host would receive "--" as the
// program to run.
func buildArgv(opts Options, target string, cmd Command, sudo bool) []string {
	// -n detaches stdin, -T asks for no pty, and BatchMode=yes turns every
	// interactive prompt into an immediate failure. Together they mean a
	// command can only ever finish or fail, never sit waiting for a human.
	argv := []string{"-n", "-T", "-o", "BatchMode=yes"}
	if opts.IdentityFile != "" {
		argv = append(argv, "-i", opts.IdentityFile)
	}
	if opts.Port != 0 {
		argv = append(argv, "-p", strconv.Itoa(opts.Port))
	}
	argv = append(argv, "--", target)
	if sudo {
		argv = append(argv, "sudo", "-n")
	}
	argv = append(argv, cmd.Program())
	return append(argv, cmd.Args()...)
}

const maxSafeTargetLen = 320

// safeTargetChars is the execution layer's own view of what may appear in a
// destination. "%" is absent on purpose: it is the character ssh_config expands
// in ProxyCommand, LocalCommand and Match exec directives, which is the path
// CVE-2023-51385 and CVE-2025-61984 were exploited through. A destination Focal
// hands to ssh can therefore never carry an expansion token into an operator's
// own config.
var safeTargetChars = newCharSet("A-Za-z0-9_@.:[]-")

var safeTargetAllowed = []string{
	"A-Za-z0-9 and _@.:[]- , not starting with a hyphen",
	"at most 320 bytes",
}

// assertSafeTarget is the second of the two layers guarding a destination. The
// first is vo.ParseTarget, which decides what Focal accepts as input; this one
// re-checks the exact string on its way into an argv, so a Target that reached
// this package some other way still cannot introduce an option or an
// ssh_config expansion token. It is a pure function and is tested directly:
// vo.Target cannot be forged, so the only way to exercise this layer is to call
// it.
func assertSafeTarget(s string) *result.Error {
	switch {
	case s == "":
		return rejectTarget("must not be empty")
	case len(s) > maxSafeTargetLen:
		return rejectTarget(fmt.Sprintf("must be at most %d bytes", maxSafeTargetLen))
	case s[0] == '-':
		return rejectTarget("must not start with a hyphen")
	}
	for i := 0; i < len(s); i++ {
		if !safeTargetChars[s[i]] {
			return rejectTarget(fmt.Sprintf("contains a character that is not allowed at byte %d", i))
		}
	}
	return nil
}

func rejectTarget(reason string) *result.Error {
	return result.ValidationError("invalid_target", "target "+reason, "target", safeTargetAllowed)
}

// outputCap is the byte budget shared by a command's two streams. One budget
// rather than two means max_output states what an operator expects it to: the
// most output a single command can hand back, whichever stream produced it.
type outputCap struct {
	mu        sync.Mutex
	remaining int64
	truncated bool
	killed    bool
	kill      func()
}

// truncate records the cutoff and kills the child once, so that a command still
// producing output stops rather than being drained forever. The caller holds mu.
func (c *outputCap) truncate() {
	c.truncated = true
	if c.kill != nil && !c.killed {
		c.killed = true
		c.kill()
	}
}

// armKill hands truncate the way to stop the child, once there is a child to
// stop. If the budget was already exhausted by then, the kill happens here.
func (c *outputCap) armKill(kill func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kill = kill
	if c.truncated && !c.killed {
		c.killed = true
		c.kill()
	}
}

func (c *outputCap) wasTruncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncated
}

// capturedStream keeps one stream's bytes up to the shared budget.
//
// Past the budget it keeps accepting writes and drops them, rather than
// returning an error. That is deliberate: os/exec copies each stream in a
// goroutine and stops on the first write error, leaving the child blocked on a
// pipe nobody is reading. Accepting and discarding lets that copy run to EOF —
// the io.Discard half of the drain — while the kill from truncate makes EOF
// arrive at once. Wait then returns promptly and no goroutine is left behind.
type capturedStream struct {
	cap *outputCap
	buf bytes.Buffer
}

func (s *capturedStream) Write(p []byte) (int, error) {
	s.cap.mu.Lock()
	defer s.cap.mu.Unlock()
	if int64(len(p)) > s.cap.remaining {
		_, _ = s.buf.Write(p[:s.cap.remaining])
		s.cap.remaining = 0
		s.cap.truncate()
		return len(p), nil
	}
	_, _ = s.buf.Write(p)
	s.cap.remaining -= int64(len(p))
	return len(p), nil
}

// String returns the captured bytes. It takes the shared lock because the copy
// goroutine may still be running when a kill races the last write.
func (s *capturedStream) String() string {
	s.cap.mu.Lock()
	defer s.cap.mu.Unlock()
	return s.buf.String()
}
