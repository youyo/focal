package cli

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
)

const filesConfig = "operations:\n  files:\n    enabled: true\n    paths:\n      - /var/log\n"

const filesConfigWithSSM = "execution:\n  transports: [ssh, ssm]\n" + filesConfig

func filesChainAnswer(parts ...string) sshtest.Response {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("drwxr-xr-x 1 0 0 4096 2026-11-25 08:02:25.929625041 +0000 " + p + "\n")
	}
	return sshtest.Response{Output: ssh.Output{Stdout: b.String(), Duration: time.Millisecond}}
}

func TestFiles_IsDisabledByDefault(t *testing.T) {
	withConfig(t, "")
	res := runCLI(t, "prod-web", "files", "/var/log")
	if res.code == exitOK || res.built || len(res.calls) != 0 {
		t.Fatalf("code = %d, built = %t, calls = %d: a disabled files reached the host", res.code, res.built, len(res.calls))
	}
	if got := decodeError(t, res); got.Code != "operation_disabled" {
		t.Errorf("code = %q, want operation_disabled", got.Code)
	}
}

// TestFiles_ConfiguredListReachesTheOperation runs the whole CLI path: the
// configured list is what the path is matched against, and the two commands
// that reach the host name the path the caller typed.
func TestFiles_ConfiguredListReachesTheOperation(t *testing.T) {
	withConfig(t, filesConfig)
	rec := &sshtest.Recorder{}
	rec.RespondSeq(
		filesChainAnswer("/", "/var", "/var/log", "/var/log/nginx"),
		sshtest.Response{Output: ssh.Output{Stdout: "total 4\n-rw-r--r-- 1 6 2026-01-02 03:04:05.000000000 +0900 access.log\n"}},
	)
	res := runCLIWith(t, rec, "prod-web", "files", "/var/log/nginx")
	if res.code != exitOK {
		t.Fatalf("exit code = %d; stderr = %s", res.code, res.stderr)
	}
	if len(res.calls) != 2 {
		t.Fatalf("%d calls, want the check and the listing", len(res.calls))
	}
	if got := res.calls[1].Command.Args(); got[len(got)-1] != "/var/log/nginx" {
		t.Errorf("listing args = %q", got)
	}
	env := decodeEnvelope(t, res)
	want := []map[string]string{{"name": "access.log", "type": "file", "size": "6", "mtime": "2026-01-02T03:04:05+09:00"}}
	if !slices.EqualFunc(env.Items, want, func(a, b map[string]string) bool {
		return len(a) == len(b) && a["name"] == b["name"] && a["mtime"] == b["mtime"]
	}) {
		t.Errorf("Items = %v, want %v", env.Items, want)
	}
}

func TestFiles_PathOutsideTheListNeverReachesTheHost(t *testing.T) {
	withConfig(t, filesConfig)
	for _, path := range []string{"/etc", "/var/log2", "/var/log/../../etc", "var/log", "/var/log/", "/"} {
		res := runCLI(t, "prod-web", "files", path)
		if res.code == exitOK || res.built || len(res.calls) != 0 {
			t.Errorf("%q: code = %d, built = %t, calls = %d", path, res.code, res.built, len(res.calls))
			continue
		}
		got := decodeError(t, res)
		if got.Kind != result.KindValidation || (got.Code != "path_not_allowed" && got.Code != "invalid_path") {
			t.Errorf("%q: error = %+v", path, got)
		}
	}
}

func TestFiles_NeedsExactlyOnePath(t *testing.T) {
	withConfig(t, filesConfig)
	for _, argv := range [][]string{{}, {"/var/log", "/var/log/nginx"}} {
		res := runCLI(t, append([]string{"prod-web", "files"}, argv...)...)
		if res.code != exitUsage || res.built {
			t.Errorf("%q: code = %d, built = %t, want a usage error before connecting", argv, res.code, res.built)
		}
	}
}

// TestFiles_SingleCLIRunSaysNothingAboutSSM keeps the start-up note about
// SSM's view to the long-running commands: a one-off run does not print it.
func TestFiles_SingleCLIRunSaysNothingAboutSSM(t *testing.T) {
	withConfig(t, filesConfigWithSSM)
	rec := &sshtest.Recorder{}
	rec.RespondSeq(filesChainAnswer("/", "/var", "/var/log"), sshtest.Response{Output: ssh.Output{Stdout: "total 0\n"}})
	res := runCLIWith(t, rec, "prod-web", "files", "/var/log")
	if res.code != exitOK {
		t.Fatalf("exit code = %d; stderr = %s", res.code, res.stderr)
	}
	if res.stderr != "" {
		t.Errorf("stderr = %q, want nothing from a single run", res.stderr)
	}
}

func TestServeWarnsOnceWhenFilesRunsOverSSM(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		warn bool
	}{
		{"files enabled with ssm", filesConfigWithSSM, true},
		{"files enabled without ssm", filesConfig, false},
		{"ssm without files", "execution:\n  transports: [ssh, ssm]\n", false},
		{"ssm with files listed but disabled", "execution:\n  transports: [ssh, ssm]\noperations:\n  files:\n    paths: [/var/log]\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withConfig(t, tc.body)
			t.Setenv(upstreamTokenEnv, testToken)
			s := newServeApp(t)
			if err := serveUntilCancelled(s, serveOptions{listen: "127.0.0.1:0"}); err != nil {
				t.Fatalf("serve: %v", err.err)
			}
			assertSSMFilesWarning(t, s.stderr.String(), "focal serve", tc.warn)
		})
	}
}

// TestServeRefusalWithFilesOverSSMIsOneErrorLine keeps the note for a server
// that starts: a start refused for its address leaves stderr at the one error
// line every refusal is.
func TestServeRefusalWithFilesOverSSMIsOneErrorLine(t *testing.T) {
	withConfig(t, filesConfigWithSSM)
	t.Setenv(upstreamTokenEnv, "")
	s := newServeApp(t)
	err := serveUntilCancelled(s, serveOptions{listen: "127.0.0.1:0"})
	if err == nil {
		t.Fatal("serve started on loopback TCP with no token")
	}
	got := s.stderr.String()
	if strings.Count(strings.TrimSuffix(got, "\n"), "\n") != 0 || strings.Contains(got, "root") {
		t.Errorf("stderr = %q, want only the refusal", got)
	}
	if e := decodeError(t, runResult{stderr: got}); e.Code != "unauthenticated_listen_address" {
		t.Errorf("code = %q", e.Code)
	}
}

func TestMCPWarnsOnceWhenFilesRunsOverSSM(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		warn bool
	}{
		{"files enabled with ssm", filesConfigWithSSM, true},
		{"files enabled without ssm", filesConfig, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withConfig(t, tc.body)
			t.Setenv(upstreamTokenEnv, "")
			s := newServeApp(t)
			code, stdout := runMCPWithInput(t, s, "", "mcp")
			if code != exitOK || stdout != "" {
				t.Fatalf("exit code = %d, stdout = %q", code, stdout)
			}
			assertSSMFilesWarning(t, s.stderr.String(), "focal mcp", tc.warn)
		})
	}
}

func assertSSMFilesWarning(t *testing.T, stderr, command string, want bool) {
	t.Helper()
	has := strings.Contains(stderr, "root")
	if has != want {
		t.Fatalf("stderr = %q, want the SSM note: %t", stderr, want)
	}
	if !want {
		return
	}
	if got := strings.Count(stderr, "root's permissions"); got != 1 {
		t.Errorf("the note appears %d times, want once:\n%s", got, stderr)
	}
	for _, needle := range []string{command, "ssm", "files"} {
		if !strings.Contains(stderr, needle) {
			t.Errorf("stderr = %q, want it to name %q", stderr, needle)
		}
	}
}
