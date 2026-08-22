package ssh

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// The tests in this package that need a real process run the test binary itself
// under the name "ssh", the os/exec TestHelperProcess pattern. It is the only
// way to observe what internal/ssh actually hands to the operating system: the
// sudo retry happens inside one Execute, so no Executor-level mock can see the
// second invocation or the argv it carried. Nothing is written to disk beyond a
// symlink and a log of argv, and no shell script is involved.

const (
	fakeSSHEnv       = "FOCAL_FAKE_SSH"
	fakeSSHRecordEnv = "FOCAL_FAKE_SSH_RECORD"
	fakeSSHExitsEnv  = "FOCAL_FAKE_SSH_EXITS"
	fakeSSHStdoutEnv = "FOCAL_FAKE_SSH_STDOUT"
	fakeSSHStderrEnv = "FOCAL_FAKE_SSH_STDERR"
	fakeSSHBytesEnv  = "FOCAL_FAKE_SSH_STDOUT_BYTES"
	fakeSSHSleepEnv  = "FOCAL_FAKE_SSH_SLEEP"

	// argvSep joins one invocation's arguments in the log. It cannot occur
	// in an argv Focal builds, so a recorded line always splits back into
	// exactly the arguments that were passed.
	argvSep = "\x1f"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeSSHEnv) != "" {
		os.Exit(fakeSSHMain())
	}
	os.Exit(m.Run())
}

// fakeSSHMain is what runs when the test binary is invoked as "ssh". It records
// the argv it was called with and then behaves as the current test asked: a
// chosen exit status, chosen output, a chosen amount of output, and an optional
// sleep so that a timeout or a truncation kill has something to interrupt.
func fakeSSHMain() int {
	n := recordFakeSSHInvocation()

	if s := fakeSSHVar(fakeSSHStdoutEnv, n); s != "" {
		fmt.Fprint(os.Stdout, s)
	}
	if s := fakeSSHVar(fakeSSHStderrEnv, n); s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	if s := fakeSSHVar(fakeSSHBytesEnv, n); s != "" {
		count, err := strconv.Atoi(s)
		if err != nil {
			return 127
		}
		fmt.Fprint(os.Stdout, strings.Repeat("x", count))
	}
	if s := fakeSSHVar(fakeSSHSleepEnv, n); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 127
		}
		time.Sleep(d)
	}

	exits := strings.Split(fakeSSHVar(fakeSSHExitsEnv, n), ",")
	code, err := strconv.Atoi(exits[min(n, len(exits)-1)])
	if err != nil {
		return 0
	}
	return code
}

// recordFakeSSHInvocation appends this invocation's argv to the log and returns
// how many invocations came before it, so a test can make the second attempt
// behave differently from the first.
func recordFakeSSHInvocation() int {
	path := os.Getenv(fakeSSHRecordEnv)
	if path == "" {
		return 0
	}
	// #nosec G304 G703 -- the path is the log this test process created in its own
	// t.TempDir and handed to itself through the environment.
	before, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	// #nosec G304 G703 -- same path, same reason.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0
	}
	defer f.Close()
	fmt.Fprintln(f, strings.Join(os.Args[1:], argvSep))
	return strings.Count(string(before), "\n")
}

// fakeSSHVar reads the setting for invocation n, falling back to the setting
// that applies to every invocation.
func fakeSSHVar(name string, n int) string {
	if v, ok := os.LookupEnv(fmt.Sprintf("%s_%d", name, n)); ok {
		return v
	}
	return os.Getenv(name)
}

// fakeSSH is a test's handle on the stand-in ssh(1) it installed.
type fakeSSH struct{ record string }

// installFakeSSH puts a symlink named "ssh" pointing at the test binary alone
// on PATH, so that exec.LookPath("ssh") inside the executor resolves to it.
func installFakeSSH(t *testing.T) *fakeSSH {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(self, filepath.Join(dir, "ssh")); err != nil {
		t.Fatalf("install the fake ssh: %v", err)
	}
	f := &fakeSSH{record: filepath.Join(dir, "argv.log")}
	t.Setenv("PATH", dir)
	t.Setenv(fakeSSHEnv, "1")
	t.Setenv(fakeSSHRecordEnv, f.record)
	t.Setenv(fakeSSHExitsEnv, "0")
	return f
}

// argvs returns one slice of arguments per invocation, in order.
func (f *fakeSSH) argvs(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(f.record)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the recorded argv: %v", err)
	}
	var argvs [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		argvs = append(argvs, strings.Split(line, argvSep))
	}
	return argvs
}

// testOptions are the parameters the process tests run under: a timeout long
// enough that only a deliberately hung command reaches it, and a small output
// ceiling so truncation can be provoked with a few kilobytes.
func testOptions() Options {
	return Options{Timeout: 10 * time.Second, MaxOutput: 64}
}

func newTestExecutor(t *testing.T, opts Options) *OpenSSH {
	t.Helper()
	ex, err := NewOpenSSH(opts)
	if err != nil {
		t.Fatalf("NewOpenSSH(%+v): %v", opts, err)
	}
	return ex
}

func mustTarget(t *testing.T, s string) vo.Target {
	t.Helper()
	target, err := vo.ParseTarget(s)
	if err != nil {
		t.Fatalf("ParseTarget(%q): %v", s, err)
	}
	return target
}

// uptimeCommand builds the command through the exported factory, the same way
// an operation in M3 will.
func uptimeCommand(t *testing.T, mode policy.SudoMode) Command {
	t.Helper()
	p := policy.Policy{}
	if mode != policy.SudoNever {
		// "logs" is the only operation whose capability admits a sudo
		// mode, so it is the only way to obtain a non-zero Policy.
		resolved, err := policy.Resolve("logs", mode)
		if err != nil {
			t.Fatalf("policy.Resolve(logs, %s): %v", mode, err)
		}
		p = resolved
	}
	cmd, err := UptimeCommand(p)
	if err != nil {
		t.Fatalf("UptimeCommand: %v", err)
	}
	if cmd.Sudo() != mode {
		t.Fatalf("UptimeCommand carries sudo %s, want %s", cmd.Sudo(), mode)
	}
	return cmd
}

func TestNewOpenSSHRejectsUnusableOptions(t *testing.T) {
	base := testOptions()
	for _, tc := range []struct {
		name string
		opts Options
		code string
	}{
		{"zero timeout", Options{MaxOutput: base.MaxOutput}, "invalid_timeout"},
		{"negative timeout", Options{Timeout: -time.Second, MaxOutput: base.MaxOutput}, "invalid_timeout"},
		{"zero max output", Options{Timeout: base.Timeout}, "invalid_max_output"},
		{"negative max output", Options{Timeout: base.Timeout, MaxOutput: -1}, "invalid_max_output"},
		{"negative port", Options{Timeout: base.Timeout, MaxOutput: base.MaxOutput, Port: -1}, "invalid_port"},
		{"port above range", Options{Timeout: base.Timeout, MaxOutput: base.MaxOutput, Port: 65536}, "invalid_port"},
		{"identity file as option", Options{Timeout: base.Timeout, MaxOutput: base.MaxOutput, IdentityFile: "-oProxyCommand=evil"}, "invalid_identity_file"},
		{"identity file with newline", Options{Timeout: base.Timeout, MaxOutput: base.MaxOutput, IdentityFile: "/key\nProxyCommand=evil"}, "invalid_identity_file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex, err := NewOpenSSH(tc.opts)
			if err == nil {
				t.Fatalf("NewOpenSSH(%+v) succeeded, want %s", tc.opts, tc.code)
			}
			if ex != nil {
				t.Errorf("NewOpenSSH returned an executor alongside an error")
			}
			if err.Kind != result.KindValidation || err.Code != tc.code {
				t.Errorf("error is %s/%s, want %s/%s", err.Kind, err.Code, result.KindValidation, tc.code)
			}
		})
	}
}

func TestNewOpenSSHAcceptsUsableOptions(t *testing.T) {
	opts := Options{Timeout: time.Second, MaxOutput: 1 << 20, IdentityFile: "/home/focal/.ssh/id_ed25519", Port: 22}
	if _, err := NewOpenSSH(opts); err != nil {
		t.Fatalf("NewOpenSSH(%+v): %v", opts, err)
	}
	// Port and IdentityFile are optional; ssh(1) then applies ~/.ssh/config.
	if _, err := NewOpenSSH(Options{Timeout: time.Second, MaxOutput: 1 << 20}); err != nil {
		t.Fatalf("NewOpenSSH without a port or identity file: %v", err)
	}
}

func TestAssertSafeTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		accept bool
	}{
		{"host", "example.com", true},
		{"host with user", "ops@example.com", true},
		{"host with hyphen inside a label", "web-1.example.com", true},
		{"ipv4", "192.0.2.10", true},
		{"bracketed ipv6", "[2001:db8::1]", true},
		{"bracketed ipv6 with user", "ops@[2001:db8::1]", true},
		{"config alias", "prod_web", true},
		{"at the length ceiling", strings.Repeat("a", maxSafeTargetLen), true},

		{"empty", "", false},
		{"option injection", "-oProxyCommand=evil", false},
		{"ssh_config host expansion", "%h", false},
		{"ssh_config expansion inside a host", "host%h.example.com", false},
		{"ssh_config user expansion", "%u@example.com", false},
		{"space", "example.com host", false},
		{"semicolon", "example.com;id", false},
		{"pipe", "example.com|id", false},
		{"ampersand", "example.com&id", false},
		{"dollar", "example.com$USER", false},
		{"backquote", "example.com`id`", false},
		{"newline", "example.com\nHost evil", false},
		{"carriage return", "example.com\rHost evil", false},
		{"nul", "example.com\x00", false},
		{"redirection", "example.com>file", false},
		{"single quote", "example.com'", false},
		{"non-ascii", "example.comé", false},
		{"past the length ceiling", strings.Repeat("a", maxSafeTargetLen+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := assertSafeTarget(tc.target)
			switch {
			case tc.accept && err != nil:
				t.Errorf("assertSafeTarget rejected an acceptable target: %v", err)
			case !tc.accept && err == nil:
				t.Errorf("assertSafeTarget accepted a target it must refuse")
			case !tc.accept && (err.Kind != result.KindValidation || err.Code != "invalid_target"):
				t.Errorf("error is %s/%s, want %s/invalid_target", err.Kind, err.Code, result.KindValidation)
			}
		})
	}
}

func TestExecuteRejectsZeroCommand(t *testing.T) {
	fake := installFakeSSH(t)
	ex := newTestExecutor(t, testOptions())

	_, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), Command{})
	assertValidationError(t, err, "empty_command")
	if got := fake.argvs(t); got != nil {
		t.Errorf("a zero Command reached ssh: %v", got)
	}
}

func TestExecuteRejectsUnsafeTarget(t *testing.T) {
	fake := installFakeSSH(t)
	ex := newTestExecutor(t, testOptions())

	// The zero Target is the one unsafe destination that is reachable
	// without forging a value object, and it is what a caller that skipped
	// vo.ParseTarget would hold.
	_, err := ex.Execute(t.Context(), vo.Target{}, uptimeCommand(t, policy.SudoNever))
	assertValidationError(t, err, "invalid_target")
	if got := fake.argvs(t); got != nil {
		t.Errorf("an unsafe target reached ssh: %v", got)
	}
}

func TestExecuteBuildsTheGoldenArgv(t *testing.T) {
	fake := installFakeSSH(t)
	ex := newTestExecutor(t, testOptions())

	if _, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), uptimeCommand(t, policy.SudoNever)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	argvs := fake.argvs(t)
	if len(argvs) != 1 {
		t.Fatalf("ssh was invoked %d times, want once", len(argvs))
	}
	want := readGoldenArgv(t)["plain"]
	if got := "ssh " + strings.Join(argvs[0], " "); got != want {
		t.Errorf("the argv the operating system saw differs from the golden file\n got: %s\nwant: %s", got, want)
	}
}

func TestExecuteCapturesOutputAndExitCode(t *testing.T) {
	fake := installFakeSSH(t)
	t.Setenv(fakeSSHStdoutEnv, "up 3 days")
	t.Setenv(fakeSSHStderrEnv, "a warning")
	t.Setenv(fakeSSHExitsEnv, "3")
	ex := newTestExecutor(t, testOptions())

	out, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), uptimeCommand(t, policy.SudoNever))
	if err != nil {
		t.Fatalf("a non-zero exit status must be reported in Output, not as an error: %v", err)
	}
	if out.Stdout != "up 3 days" || out.Stderr != "a warning" {
		t.Errorf("captured stdout %q and stderr %q", out.Stdout, out.Stderr)
	}
	if out.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", out.ExitCode)
	}
	if out.Truncated {
		t.Errorf("Truncated is set for output well under the ceiling")
	}
	if out.Duration <= 0 {
		t.Errorf("Duration = %s, want a positive measurement", out.Duration)
	}
	if len(fake.argvs(t)) != 1 {
		t.Errorf("a SudoNever command was run more than once")
	}
}

// TestExecuteReportsSSHNotFound and the ErrDot test below cover the two ways
// resolving the client can fail. Both must surface as execution errors rather
// than as a panic or a bare os/exec error.
func TestExecuteReportsSSHNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	ex := newTestExecutor(t, testOptions())

	_, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), uptimeCommand(t, policy.SudoNever))
	assertExecutionError(t, err, "ssh_not_found")
}

// TestExecuteReportsErrDot pins that Focal keeps the os/exec protection against
// running an "ssh" found in the current directory. Setting GODEBUG=execerrdot=0
// would turn this test's error back into a successful lookup, which is why the
// executor never sets it.
func TestExecuteReportsErrDot(t *testing.T) {
	dir := t.TempDir()
	// #nosec G306 -- the decoy has to carry the executable bit, otherwise
	// LookPath skips it and there is no ErrDot to observe.
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write the decoy ssh: %v", err)
	}
	t.Chdir(dir)
	t.Setenv("PATH", ".")

	ex := newTestExecutor(t, testOptions())
	_, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), uptimeCommand(t, policy.SudoNever))
	assertExecutionError(t, err, "ssh_not_found")

	if _, direct := exec.LookPath("ssh"); !errors.Is(direct, exec.ErrDot) {
		t.Fatalf("the decoy did not reproduce ErrDot, so this test proved nothing: %v", direct)
	}
}

func TestExecuteTimesOut(t *testing.T) {
	installFakeSSH(t)
	t.Setenv(fakeSSHSleepEnv, "30s")
	opts := testOptions()
	opts.Timeout = 300 * time.Millisecond
	ex := newTestExecutor(t, opts)

	started := time.Now()
	out, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), uptimeCommand(t, policy.SudoNever))
	elapsed := time.Since(started)

	if !TimedOut(err) {
		t.Fatalf("Execute returned %v, want the timeout error", err)
	}
	assertExecutionError(t, err, "timeout")
	if elapsed > 5*time.Second {
		t.Errorf("Execute took %s to give up on a command with a %s timeout", elapsed, opts.Timeout)
	}
	if out.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for a command that never reached an exit status", out.ExitCode)
	}
	if out.Duration < opts.Timeout {
		t.Errorf("Duration = %s, want at least the timeout %s", out.Duration, opts.Timeout)
	}
}

// TestExecuteTruncatesOutput covers the second half of the reason max_output
// exists: not only that the caller is handed a bounded string, but that the
// command producing the flood is stopped and everything started to run it is
// cleaned up. A truncation that left the child alive would still hang the agent
// the ceiling was meant to protect.
func TestExecuteTruncatesOutput(t *testing.T) {
	installFakeSSH(t)
	t.Setenv(fakeSSHBytesEnv, "8192")
	t.Setenv(fakeSSHSleepEnv, "30s")
	opts := testOptions()
	ex := newTestExecutor(t, opts)

	before := goroutineCount()
	started := time.Now()
	out, err := ex.Execute(t.Context(), mustTarget(t, "example.com"), uptimeCommand(t, policy.SudoNever))
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.Truncated {
		t.Errorf("Truncated is not set for output past the %d byte ceiling", opts.MaxOutput)
	}
	if int64(len(out.Stdout)) != opts.MaxOutput {
		t.Errorf("captured %d bytes, want exactly the ceiling of %d", len(out.Stdout), opts.MaxOutput)
	}
	if out.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for a command killed before it exited", out.ExitCode)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Execute took %s: the flooding command was drained rather than killed", elapsed)
	}
	if after := settledGoroutineCount(before); after > before {
		t.Errorf("goroutine count went from %d to %d: a copy goroutine outlived Execute", before, after)
	}
}

func goroutineCount() int {
	runtime.GC()
	return runtime.NumGoroutine()
}

// settledGoroutineCount gives the runtime a moment to retire goroutines that
// have already returned, so the comparison measures a leak rather than
// scheduling.
func settledGoroutineCount(want int) int {
	count := goroutineCount()
	for range 50 {
		if count <= want {
			return count
		}
		time.Sleep(10 * time.Millisecond)
		count = goroutineCount()
	}
	return count
}

func assertValidationError(t *testing.T, err error, code string) {
	t.Helper()
	assertError(t, err, result.KindValidation, code)
}

func assertExecutionError(t *testing.T, err error, code string) {
	t.Helper()
	assertError(t, err, result.KindExecution, code)
}

func assertError(t *testing.T, err error, kind result.Kind, code string) {
	t.Helper()
	var e *result.Error
	if !errors.As(err, &e) {
		t.Fatalf("error is %v, want a *result.Error with code %s", err, code)
	}
	if e.Kind != kind || e.Code != code {
		t.Errorf("error is %s/%s, want %s/%s", e.Kind, e.Code, kind, code)
	}
}

// assertArgvHasSudo states where "sudo -n" must sit relative to the destination
// for the retry tests below.
func assertArgvHasSudo(t *testing.T, argv []string, want bool) {
	t.Helper()
	i := slices.Index(argv, "sudo")
	if !want {
		if i >= 0 {
			t.Errorf("argv escalated when it must not: %v", argv)
		}
		return
	}
	switch {
	case i < 0:
		t.Errorf("argv did not escalate when it must: %v", argv)
	case argv[i+1] != "-n":
		t.Errorf("sudo is not followed by -n, so it could prompt: %v", argv)
	case argv[i-1] != "example.com":
		t.Errorf("sudo is not directly after the destination: %v", argv)
	}
}
