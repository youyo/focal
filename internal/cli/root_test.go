package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
)

// run drives one invocation with a Recorder in place of a real SSH client and
// reports everything a test could want to assert on: the exit code, both
// streams, the calls that reached the executor, and the connection options it
// was built from.
type runResult struct {
	code   int
	stdout string
	stderr string
	calls  []sshtest.Call
	opts   ssh.Options
	built  bool
}

func runCLI(t *testing.T, args ...string) runResult {
	t.Helper()
	return runCLIWith(t, &sshtest.Recorder{}, args...)
}

func runCLIWith(t *testing.T, rec *sshtest.Recorder, args ...string) runResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	var res runResult
	a := &app{
		stdout: &stdout,
		stderr: &stderr,
		newExecutor: func(opts ssh.Options) (ssh.Executor, *result.Error) {
			res.opts, res.built = opts, true
			return rec, nil
		},
	}
	res.code = a.run(args)
	res.stdout, res.stderr = stdout.String(), stderr.String()
	res.calls = rec.Calls()
	return res
}

// withConfig points XDG_CONFIG_HOME at a fresh directory holding body as
// focal/config.yaml. An empty body leaves no file at all, which is how a test
// asks for focal's safe-side defaults.
func withConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if body == "" {
		return
	}
	focalDir := filepath.Join(dir, "focal")
	if err := os.MkdirAll(focalDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", focalDir, err)
	}
	if err := os.WriteFile(filepath.Join(focalDir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// decodeError reads the single JSON line focal writes to stderr for a failure.
func decodeError(t *testing.T, res runResult) result.Error {
	t.Helper()
	line := strings.TrimSpace(res.stderr)
	if line == "" {
		t.Fatalf("stderr is empty; stdout=%q code=%d", res.stdout, res.code)
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("stderr is not a single line: %q", res.stderr)
	}
	var got result.Error
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("stderr is not JSON: %v (%q)", err, line)
	}
	return got
}

func decodeEnvelope(t *testing.T, res runResult) result.Envelope {
	t.Helper()
	var env result.Envelope
	if err := json.Unmarshal([]byte(res.stdout), &env); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, res.stdout)
	}
	return env
}

func TestExecute_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--version"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitOK, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("stdout is empty, want a version string")
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// TestLogs_GoldenArgv is the end-to-end check that a command line becomes one
// exact remote argv: everything between the flags a caller typed and the
// journalctl invocation focal would have sent is exercised here.
func TestLogs_GoldenArgv(t *testing.T) {
	withConfig(t, "")

	res := runCLI(t, "prod-web", "logs", "nginx", "--since", "30m", "--lines", "200")
	if res.code != exitOK {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.code, res.stderr)
	}
	if len(res.calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(res.calls))
	}
	call := res.calls[0]
	if got := call.Target.String(); got != "prod-web" {
		t.Fatalf("target = %q, want %q", got, "prod-web")
	}
	if got := call.Command.Program(); got != "journalctl" {
		t.Fatalf("program = %q, want %q", got, "journalctl")
	}
	want := []string{"-u", "nginx.service", "--since=-30m", "-n", "200", "--no-pager", "--output=short-iso"}
	if got := call.Command.Args(); !slices.Equal(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
	if call.SudoPrefixed {
		t.Fatal("logs was prefixed with sudo under the default configuration")
	}
}

func TestLogs_DefaultsWhenFlagsOmitted(t *testing.T) {
	withConfig(t, "")

	res := runCLI(t, "prod-web", "logs", "nginx")
	if res.code != exitOK {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.code, res.stderr)
	}
	want := []string{"-u", "nginx.service", "--since=-30m", "-n", "200", "--no-pager", "--output=short-iso"}
	if got := res.calls[0].Command.Args(); !slices.Equal(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestOutput_DefaultIsOneJSONLine(t *testing.T) {
	withConfig(t, "")

	res := runCLI(t, "prod-web", "service", "nginx")
	if res.code != exitOK {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.code, res.stderr)
	}
	if strings.Count(strings.TrimSpace(res.stdout), "\n") != 0 {
		t.Fatalf("default output is not a single line: %q", res.stdout)
	}
	env := decodeEnvelope(t, res)
	if env.Operation != "service" || env.Host != "prod-web" {
		t.Fatalf("envelope = %+v, want operation service on prod-web", env)
	}
}

func TestOutput_PrettyIsIndented(t *testing.T) {
	withConfig(t, "")

	res := runCLI(t, "--pretty", "prod-web", "service", "nginx")
	if res.code != exitOK {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.code, res.stderr)
	}
	if !strings.Contains(res.stdout, "\n  \"operation\"") {
		t.Fatalf("output is not indented JSON: %q", res.stdout)
	}
	if env := decodeEnvelope(t, res); env.Operation != "service" {
		t.Fatalf("operation = %q, want service", env.Operation)
	}
}

func TestOutput_JSONFlagKeepsOneLine(t *testing.T) {
	withConfig(t, "")

	res := runCLI(t, "--json", "prod-web", "service", "nginx")
	if res.code != exitOK {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.code, res.stderr)
	}
	if strings.Count(strings.TrimSpace(res.stdout), "\n") != 0 {
		t.Fatalf("--json output is not a single line: %q", res.stdout)
	}
}

func TestOutput_JSONAndPrettyTogetherIsUsageError(t *testing.T) {
	withConfig(t, "")

	res := runCLI(t, "--json", "--pretty", "prod-web", "service", "nginx")
	if res.code != exitUsage {
		t.Fatalf("exit code = %d, want %d; stderr=%s", res.code, exitUsage, res.stderr)
	}
	if len(res.calls) != 0 {
		t.Fatalf("recorded %d calls, want 0", len(res.calls))
	}
	if got := decodeError(t, res); got.Kind != result.KindValidation {
		t.Fatalf("kind = %q, want %q", got.Kind, result.KindValidation)
	}
}

// TestDisabledOperation_NeverReachesRemote fixes the rule that decides whether
// an operation exists for this installation before anything is connected to.
func TestDisabledOperation_NeverReachesRemote(t *testing.T) {
	withConfig(t, "")

	res := runCLI(t, "prod-web", "kernel")
	if res.code == exitOK {
		t.Fatalf("exit code = 0, want non-zero; stdout=%q", res.stdout)
	}
	if len(res.calls) != 0 {
		t.Fatalf("recorded %d calls, want 0: a disabled operation reached the host", len(res.calls))
	}
	if res.built {
		t.Fatal("an executor was built for a disabled operation")
	}
	if res.stdout != "" {
		t.Fatalf("stdout = %q, want empty", res.stdout)
	}
	got := decodeError(t, res)
	if got.Kind != result.KindPolicy {
		t.Fatalf("kind = %q, want %q", got.Kind, result.KindPolicy)
	}
	if got.Code != "operation_disabled" {
		t.Fatalf("code = %q, want %q", got.Code, "operation_disabled")
	}
}

// TestEnabledKernel_RecordsArgv is the other half of the pair: the same command
// line, with the operation turned on, does reach the executor.
func TestEnabledKernel_RecordsArgv(t *testing.T) {
	withConfig(t, "operations:\n  kernel:\n    enabled: true\n")

	res := runCLI(t, "prod-web", "kernel", "--since", "1h", "--lines", "200")
	if res.code != exitOK {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.code, res.stderr)
	}
	if len(res.calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(res.calls))
	}
	want := []string{"-k", "--since=-1h", "-n", "200", "--no-pager", "--output=short-iso"}
	if got := res.calls[0].Command.Args(); !slices.Equal(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

// TestNoEscapeHatch asserts the absences that matter: there is no -o, and no
// operation that names a remote command.
func TestNoEscapeHatch(t *testing.T) {
	withConfig(t, "")

	t.Run("no -o flag", func(t *testing.T) {
		for _, arg := range []string{"-o", "--option"} {
			res := runCLI(t, arg, "ProxyCommand=nc evil 1234", "prod-web", "system")
			if res.code != exitUsage {
				t.Fatalf("%s: exit code = %d, want %d; stderr=%s", arg, res.code, exitUsage, res.stderr)
			}
			if len(res.calls) != 0 {
				t.Fatalf("%s: recorded %d calls, want 0", arg, len(res.calls))
			}
		}
	})

	t.Run("no command-naming operations", func(t *testing.T) {
		for _, name := range []string{"exec", "run", "shell", "command"} {
			res := runCLI(t, "prod-web", name, "id")
			if res.code != exitUsage {
				t.Fatalf("%s: exit code = %d, want %d; stderr=%s", name, res.code, exitUsage, res.stderr)
			}
			if len(res.calls) != 0 {
				t.Fatalf("%s: recorded %d calls, want 0", name, len(res.calls))
			}
			got := decodeError(t, res)
			if got.Code != "unknown_operation" {
				t.Fatalf("%s: code = %q, want %q", name, got.Code, "unknown_operation")
			}
			if slices.Contains(got.Allowed, name) {
				t.Fatalf("%s is listed as an accepted operation", name)
			}
		}
	})

	t.Run("only serve as a subcommand", func(t *testing.T) {
		var g globals
		cmd := (&app{stdout: io.Discard, stderr: io.Discard}).newRootCommand(&g)
		subs := cmd.Commands()
		names := make([]string, 0, len(subs))
		for _, sub := range subs {
			names = append(names, sub.Name())
		}
		if !slices.Equal(names, []string{"serve"}) {
			t.Fatalf("root has subcommands %v, want [serve]", names)
		}
	})
}

func TestUnknownFlagIsUsageError(t *testing.T) {
	withConfig(t, "")

	res := runCLI(t, "--bogus", "prod-web", "system")
	if res.code != exitUsage {
		t.Fatalf("exit code = %d, want %d; stderr=%s", res.code, exitUsage, res.stderr)
	}
	if got := decodeError(t, res); got.Code != "invalid_arguments" {
		t.Fatalf("code = %q, want %q", got.Code, "invalid_arguments")
	}
}

// TestGlobalFlagAfterHostIsUnknown fixes the asymmetry SetInterspersed(false)
// creates: a global flag belongs before HOST, and after OPERATION the same
// spelling is the operation's input space, where focal does not define it.
func TestGlobalFlagAfterHostIsUnknown(t *testing.T) {
	withConfig(t, "")

	before := runCLI(t, "--pretty", "prod-web", "service", "nginx")
	if before.code != exitOK {
		t.Fatalf("before HOST: exit code = %d, want 0; stderr=%s", before.code, before.stderr)
	}

	after := runCLI(t, "prod-web", "service", "nginx", "--pretty")
	if after.code != exitUsage {
		t.Fatalf("after HOST: exit code = %d, want %d; stderr=%s", after.code, exitUsage, after.stderr)
	}
	got := decodeError(t, after)
	if got.Code != "invalid_operation_arguments" {
		t.Fatalf("code = %q, want %q", got.Code, "invalid_operation_arguments")
	}
	if len(after.calls) != 0 {
		t.Fatalf("recorded %d calls, want 0", len(after.calls))
	}
}

func TestUserFlag(t *testing.T) {
	withConfig(t, "")

	t.Run("builds user@host", func(t *testing.T) {
		res := runCLI(t, "-l", "ubuntu", "prod-web", "system")
		if res.code != exitOK {
			t.Fatalf("exit code = %d, want 0; stderr=%s", res.code, res.stderr)
		}
		if got := res.calls[0].Target.String(); got != "ubuntu@prod-web" {
			t.Fatalf("target = %q, want %q", got, "ubuntu@prod-web")
		}
	})

	t.Run("rejects an invalid user", func(t *testing.T) {
		res := runCLI(t, "-l", "ubuntu;id", "prod-web", "system")
		if res.code != exitRejected {
			t.Fatalf("exit code = %d, want %d; stderr=%s", res.code, exitRejected, res.stderr)
		}
		if len(res.calls) != 0 {
			t.Fatalf("recorded %d calls, want 0", len(res.calls))
		}
		if got := decodeError(t, res); got.Kind != result.KindValidation {
			t.Fatalf("kind = %q, want %q", got.Kind, result.KindValidation)
		}
	})

	t.Run("refuses a user named twice", func(t *testing.T) {
		res := runCLI(t, "-l", "ubuntu", "root@prod-web", "system")
		if res.code != exitRejected {
			t.Fatalf("exit code = %d, want %d; stderr=%s", res.code, exitRejected, res.stderr)
		}
		got := decodeError(t, res)
		if got.Code != "conflicting_user" {
			t.Fatalf("code = %q, want %q", got.Code, "conflicting_user")
		}
		if len(res.calls) != 0 {
			t.Fatalf("recorded %d calls, want 0", len(res.calls))
		}
	})
}

// TestInjectionInputsAreRejected walks the three ways a caller could try to
// widen what runs — a shell metacharacter in a value, a destination that is not
// one, and a value above the administrator's ceiling — and fixes that each is
// refused before anything is executed.
func TestInjectionInputsAreRejected(t *testing.T) {
	withConfig(t, "operations:\n  logs:\n    max_lines: 500\n")

	cases := []struct {
		name string
		args []string
		code string
	}{
		{"service name with a command separator", []string{"prod-web", "logs", "nginx;id"}, ""},
		{"service name with a substitution", []string{"prod-web", "service", "$(id)"}, ""},
		{"service name with a traversal", []string{"prod-web", "service", "../../etc/passwd"}, ""},
		{"target with a command separator", []string{"prod-web;id", "system"}, ""},
		{"target with an ssh_config expansion token", []string{"prod-%h", "system"}, ""},
		{"lines above the configured ceiling", []string{"prod-web", "logs", "nginx", "--lines", "900"}, "lines_above_max"},
		{"since above the value object's own bound", []string{"prod-web", "logs", "nginx", "--since", "99d"}, ""},
		{"process name with a command separator", []string{"prod-web", "processes", "--name", "nginx;id"}, ""},
		{"process name with a substitution", []string{"prod-web", "processes", "--name", "$(id)"}, ""},
		{"process name with a traversal", []string{"prod-web", "processes", "--name", "../../etc/passwd"}, ""},
		{"pid with a command separator", []string{"prod-web", "processes", "--pid", "1;id"}, ""},
		{"pid with a leading hyphen", []string{"prod-web", "processes", "--pid", "-1000"}, ""},
		{"pid above its own bound", []string{"prod-web", "processes", "--pid", "4194305"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runCLI(t, tc.args...)
			if res.code != exitRejected {
				t.Fatalf("exit code = %d, want %d; stderr=%s stdout=%s", res.code, exitRejected, res.stderr, res.stdout)
			}
			if len(res.calls) != 0 {
				t.Fatalf("recorded %d calls, want 0: a rejected input reached the host", len(res.calls))
			}
			if res.stdout != "" {
				t.Fatalf("stdout = %q, want empty", res.stdout)
			}
			got := decodeError(t, res)
			if got.Kind != result.KindValidation {
				t.Fatalf("kind = %q, want %q", got.Kind, result.KindValidation)
			}
			if got.Message == "" {
				t.Fatal("the error carries no message saying why it was refused")
			}
			if tc.code != "" && got.Code != tc.code {
				t.Fatalf("code = %q, want %q", got.Code, tc.code)
			}
		})
	}
}

// TestConnectionOptions_Priority fixes CLI override → config file → OpenSSH.
func TestConnectionOptions_Priority(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(key, []byte("key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	t.Run("config values are used when no flag is given", func(t *testing.T) {
		withConfig(t, "execution:\n  timeout: 45s\n  port: 2222\n  identity_file: "+key+"\n")

		res := runCLI(t, "prod-web", "system")
		if res.code != exitOK {
			t.Fatalf("exit code = %d, want 0; stderr=%s", res.code, res.stderr)
		}
		if res.opts.Timeout != 45*time.Second {
			t.Fatalf("timeout = %s, want 45s", res.opts.Timeout)
		}
		if res.opts.Port != 2222 {
			t.Fatalf("port = %d, want 2222", res.opts.Port)
		}
		if res.opts.IdentityFile != key {
			t.Fatalf("identity file = %q, want %q", res.opts.IdentityFile, key)
		}
	})

	t.Run("flags override the config", func(t *testing.T) {
		withConfig(t, "execution:\n  timeout: 45s\n  port: 2222\n  identity_file: "+key+"\n")
		other := filepath.Join(t.TempDir(), "other.pem")
		if err := os.WriteFile(other, []byte("key"), 0o600); err != nil {
			t.Fatalf("write key: %v", err)
		}

		res := runCLI(t, "-i", other, "-p", "2200", "--timeout", "5s", "prod-web", "system")
		if res.code != exitOK {
			t.Fatalf("exit code = %d, want 0; stderr=%s", res.code, res.stderr)
		}
		if res.opts.Timeout != 5*time.Second {
			t.Fatalf("timeout = %s, want 5s", res.opts.Timeout)
		}
		if res.opts.Port != 2200 {
			t.Fatalf("port = %d, want 2200", res.opts.Port)
		}
		if res.opts.IdentityFile != other {
			t.Fatalf("identity file = %q, want %q", res.opts.IdentityFile, other)
		}
	})

	t.Run("nothing configured leaves the choice to OpenSSH", func(t *testing.T) {
		withConfig(t, "")

		res := runCLI(t, "prod-web", "system")
		if res.code != exitOK {
			t.Fatalf("exit code = %d, want 0; stderr=%s", res.code, res.stderr)
		}
		if res.opts.Port != 0 {
			t.Fatalf("port = %d, want 0 (left to ssh)", res.opts.Port)
		}
		if res.opts.IdentityFile != "" {
			t.Fatalf("identity file = %q, want empty (left to ssh)", res.opts.IdentityFile)
		}
	})
}

// TestIdentityFlag_ExpandsHome fixes that -i ~/key names the same file the
// shell would have expanded it to, since a quoted or scripted invocation hands
// focal the tilde unexpanded.
func TestIdentityFlag_ExpandsHome(t *testing.T) {
	withConfig(t, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}

	res := runCLI(t, "-i", "~/.ssh/id_ed25519", "prod-web", "system")
	if res.code != exitOK {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.code, res.stderr)
	}
	want := filepath.Join(home, ".ssh/id_ed25519")
	if res.opts.IdentityFile != want {
		t.Fatalf("identity file = %q, want %q", res.opts.IdentityFile, want)
	}
}

// TestHelpListsTheOperations keeps the help text from going stale: the list is
// generated from the dispatch table, so it cannot name an operation focal does
// not have or omit one it does.
func TestHelpListsTheOperations(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--help"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
	for _, name := range operationNames() {
		if !strings.Contains(stdout.String(), name) {
			t.Fatalf("help does not mention the %q operation:\n%s", name, stdout.String())
		}
	}
	for _, absent := range []string{"exec", "shell", "-o,", "--option"} {
		if strings.Contains(stdout.String(), absent) {
			t.Fatalf("help mentions %q:\n%s", absent, stdout.String())
		}
	}
}

func TestMissingArgumentsIsUsageError(t *testing.T) {
	withConfig(t, "")

	for _, args := range [][]string{{}, {"prod-web"}} {
		res := runCLI(t, args...)
		if res.code != exitUsage {
			t.Fatalf("%v: exit code = %d, want %d", args, res.code, exitUsage)
		}
		if got := decodeError(t, res); got.Code != "missing_arguments" {
			t.Fatalf("%v: code = %q, want %q", args, got.Code, "missing_arguments")
		}
	}
}

// TestConfigFlagReadsTheNamedFileInsteadOfTheDefaultLocation proves --config
// is not just accepted but actually consulted: the default XDG location
// leaves kernel disabled, while the file --config names enables it, and the
// run is only let through when the flag is honoured.
func TestConfigFlagReadsTheNamedFileInsteadOfTheDefaultLocation(t *testing.T) {
	withConfig(t, "") // default location: kernel stays disabled

	dir := t.TempDir()
	path := filepath.Join(dir, "other.yaml")
	if err := os.WriteFile(path, []byte("operations:\n  kernel:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	res := runCLI(t, "--config", path, "prod-web", "kernel")
	if res.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%s", res.code, exitOK, res.stderr)
	}
	if len(res.calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(res.calls))
	}
}

// TestConfigFlagRejectsAnUnreadablePath and TestConfigFlagRejectsInvalidYAML
// pin that a bad --config path or a malformed file leaves through the same
// structured errors Load already produces for the default location.
func TestConfigFlagRejectsAnUnreadablePath(t *testing.T) {
	withConfig(t, "")

	res := runCLI(t, "--config", t.TempDir(), "prod-web", "system")
	if res.code != exitRejected {
		t.Fatalf("exit code = %d, want %d; stderr=%s", res.code, exitRejected, res.stderr)
	}
	got := decodeError(t, res)
	if got.Code != "unreadable_config" {
		t.Fatalf("code = %q, want %q", got.Code, "unreadable_config")
	}
}

func TestConfigFlagRejectsInvalidYAML(t *testing.T) {
	withConfig(t, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("operations:\n  kernel: [not-a-map]\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	res := runCLI(t, "--config", path, "prod-web", "system")
	if res.code != exitRejected {
		t.Fatalf("exit code = %d, want %d; stderr=%s", res.code, exitRejected, res.stderr)
	}
	got := decodeError(t, res)
	if got.Code != "invalid_yaml" {
		t.Fatalf("code = %q, want %q", got.Code, "invalid_yaml")
	}
}

// TestServeIsReachableAsASubcommand pins the wiring itself: `focal serve` must
// route to the serve subcommand rather than being read as HOST=serve with a
// missing operation.
func TestServeIsReachableAsASubcommand(t *testing.T) {
	res := runCLI(t, "serve", "--help")
	if res.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%s", res.code, exitOK, res.stderr)
	}
	if !strings.Contains(res.stdout, "focal serve exposes") {
		t.Fatalf("stdout = %q, want the serve command's help text", res.stdout)
	}
}
