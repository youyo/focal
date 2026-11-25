// files_test.go is an internal test: the ownership check's reading of ls -d -n
// and the listing's reading of ls -A -b -g -o are unexported, and they are the
// part of this operation that decides what a caller sees.
package operation

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
	"github.com/youyo/focal/internal/vo"
)

const filesTestDir = "/srv/focal-e2e/files/allowed"

// filesTestChain is vo.FilesPath.Chain() for filesTestDir.
var filesTestChain = []string{"/", "/srv", "/srv/focal-e2e", "/srv/focal-e2e/files", filesTestDir}

// filesListingAL2023 is ls -A -b -g -o --time-style=full-iso --block-size=1 of
// the e2e fixture directory as Amazon Linux 2023 (coreutils 8.32) printed it,
// taken as an unprivileged user. ubuntu 24.04 (coreutils 9.4) printed the same
// lines. The second entry is a file whose name holds a newline followed by a
// line ls itself could have printed.
const filesListingAL2023 = `total 20480
-rw-r--r-- 1    1 2026-11-25 08:02:25.932625085 +0000 a\ b\n-rw-r--r--\ 1\ 0\ 2026-01-02\ 03:04:05.000000000\ +0900\ forged
drwxr-xr-x 3 4096 2026-11-25 08:02:25.932625085 +0000 appowned
-rw-r--r-- 1    1 2026-11-25 08:02:25.933625099 +0000 back\\slash\ttab
lrwxrwxrwx 1   11 2026-11-25 08:02:25.933625099 +0000 ends\ -> -> regular.txt
lrwxrwxrwx 1   11 2026-11-25 08:02:25.932625085 +0000 link-file -> regular.txt
lrwxrwxrwx 1   10 2026-11-25 08:02:25.932625085 +0000 link-out -> ../outside
-rw-r--r-- 1    6 2026-01-02 03:04:05.000000000 +0900 regular.txt
drwxr-sr-x 2 4096 2026-11-25 08:02:25.932625085 +0000 sub
`

var filesListingAL2023Items = []map[string]string{
	{"name": `a\ b\n-rw-r--r--\ 1\ 0\ 2026-01-02\ 03:04:05.000000000\ +0900\ forged`, "type": "file", "size": "1", "mtime": "2026-11-25T08:02:25+00:00"},
	{"name": "appowned", "type": "directory", "size": "4096", "mtime": "2026-11-25T08:02:25+00:00"},
	{"name": `back\\slash\ttab`, "type": "file", "size": "1", "mtime": "2026-11-25T08:02:25+00:00"},
	{"name": `ends\ ->`, "type": "symlink", "size": "11", "mtime": "2026-11-25T08:02:25+00:00"},
	{"name": "link-file", "type": "symlink", "size": "11", "mtime": "2026-11-25T08:02:25+00:00"},
	{"name": "link-out", "type": "symlink", "size": "10", "mtime": "2026-11-25T08:02:25+00:00"},
	{"name": "regular.txt", "type": "file", "size": "6", "mtime": "2026-01-02T03:04:05+09:00"},
	{"name": "sub", "type": "directory", "size": "4096", "mtime": "2026-11-25T08:02:25+00:00"},
}

// filesQuestionMarkLines is what ls printed (exit 1) for a directory the user
// may read but not search: each entry it could not lstat has every column
// replaced by "?", the date and time by a single one. AL2023 and ubuntu 24.04
// printed the same two lines.
const filesQuestionMarkListing = `total 0
-????????? ? ?                                   ? f1
-????????? ? ?                                   ? f2
`

// chainLine renders one line of ls -d -n -b --time-style=full-iso.
func chainLine(mode, uid, name string) string {
	return mode + " 1 " + uid + " 0 4096 2026-11-25 08:02:25.929625041 +0000 " + name
}

// rootChain is the check's answer when every directory on the path is root's
// and writable by nobody else.
func rootChain(chain []string) []string {
	lines := make([]string, len(chain))
	for i, p := range chain {
		lines[i] = chainLine("drwxr-xr-x", "0", p)
	}
	return lines
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") + "\n" }

func filesDir(t *testing.T, path string) vo.FilesPath {
	t.Helper()
	list, err := vo.ParseFilesAllowList([]string{"/srv/focal-e2e/files"})
	if err != nil {
		t.Fatalf("ParseFilesAllowList: %v", err)
	}
	p, err := vo.ParseFilesPath(path, list)
	if err != nil {
		t.Fatalf("ParseFilesPath(%q): %v", path, err)
	}
	return p
}

func filesPolicy(t *testing.T) policy.Policy {
	t.Helper()
	p, err := policy.Resolve("files", policy.SudoNever)
	if err != nil {
		t.Fatalf("policy.Resolve: %v", err)
	}
	return p
}

func runFiles(t *testing.T, responses ...sshtest.Response) (result.Envelope, *sshtest.Recorder) {
	t.Helper()
	rec := &sshtest.Recorder{}
	rec.RespondSeq(responses...)
	env, err := NewFiles(filesDir(t, filesTestDir)).Execute(context.Background(), rec, packageTarget(t), filesPolicy(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return env, rec
}

func okChain() sshtest.Response { return resp(joinLines(rootChain(filesTestChain)), 0) }

// assertOnlyTheCheckRan pins that a refused or failed check sent nothing after
// it: the listing never reached the host.
func assertOnlyTheCheckRan(t *testing.T, rec *sshtest.Recorder) {
	t.Helper()
	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d commands ran, want only the check", len(calls))
	}
	if args := calls[0].Command.Args(); len(args) == 0 || args[0] != "-d" {
		t.Errorf("the one command that ran is %v, want the check", args)
	}
}

func TestFilesHoldsOnlyItsMatchedPath(t *testing.T) {
	typ := reflect.TypeOf(Files{})
	if typ.NumField() != 1 || typ.Field(0).Type != reflect.TypeOf(vo.FilesPath{}) {
		t.Fatalf("Files has fields %v, want exactly one vo.FilesPath", typ)
	}
	if got := NewFiles(filesDir(t, filesTestDir)).Name(); got != "files" {
		t.Errorf("Name() = %q, want files", got)
	}
}

// TestFilesRunsTheCheckThenTheListingOfTheSamePath pins the two commands and
// their order: the check names every leading part of the matched path, and
// the listing names the path itself — the check's last argument.
func TestFilesRunsTheCheckThenTheListingOfTheSamePath(t *testing.T) {
	env, rec := runFiles(t, okChain(), resp(filesListingAL2023, 0))
	calls := rec.Calls()
	if len(calls) != 2 {
		t.Fatalf("%d commands ran, want the check and the listing", len(calls))
	}
	check, list := calls[0].Command, calls[1].Command
	if check.Program() != "ls" || !slices.Equal(check.Args(), append([]string{"-d", "-n", "-b", "--time-style=full-iso"}, filesTestChain...)) {
		t.Errorf("check = %s %q", check.Program(), check.Args())
	}
	wantList := []string{"-A", "-b", "-g", "-o", "--time-style=full-iso", "--block-size=1", filesTestDir}
	if list.Program() != "ls" || !slices.Equal(list.Args(), wantList) {
		t.Errorf("listing = %s %q", list.Program(), list.Args())
	}
	checkArgs := check.Args()
	if checkArgs[len(checkArgs)-1] != list.Args()[len(wantList)-1] {
		t.Error("the check and the listing name different directories")
	}
	for _, c := range calls {
		if c.Command.Sudo() != policy.SudoNever || c.SudoPrefixed {
			t.Errorf("%v runs with sudo", c.Command.Args())
		}
	}
	if env.Status != result.StatusOK || env.Error != nil {
		t.Fatalf("Status = %q, Error = %v", env.Status, env.Error)
	}
}

func TestFilesReturnsOnlyFourKeysPerEntry(t *testing.T) {
	env, _ := runFiles(t, okChain(), resp(filesListingAL2023, 0))
	if !reflect.DeepEqual(env.Items, filesListingAL2023Items) {
		t.Errorf("Items\n got: %v\nwant: %v", env.Items, filesListingAL2023Items)
	}
	want := map[string]string{"path": filesTestDir, "complete": "true", "skipped_lines": "0"}
	if !reflect.DeepEqual(env.Data, want) {
		t.Errorf("Data = %v, want %v", env.Data, want)
	}
	if env.Operation != "files" || env.ExitCode != 0 {
		t.Errorf("Operation = %q, ExitCode = %d", env.Operation, env.ExitCode)
	}
}

// TestFilesReturnsNoRawOutput pins that no remote text reaches the caller
// except through the four parsed keys: a symlink's target, owners, modes and
// ls's own messages are all dropped.
func TestFilesReturnsNoRawOutput(t *testing.T) {
	env, _ := runFiles(t,
		sshtest.Response{Output: ssh.Output{Stdout: joinLines(rootChain(filesTestChain)), Stderr: "check-stderr"}},
		sshtest.Response{Output: ssh.Output{Stdout: filesListingAL2023, Stderr: "ls: cannot access 'secret-name': Permission denied\n", ExitCode: 1}},
	)
	body, err := env.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// The forged entry's escaped name holds "-rw-r--r--" on purpose, so
	// modes are looked for only in the forms no entry name here carries.
	for _, leak := range []string{"../outside", "-> regular.txt", "secret-name", "check-stderr", "total 20480", "drwxr", "lrwx"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the envelope carries %q:\n%s", leak, body)
		}
	}
	if env.Stdout != "" || env.Stderr != "" {
		t.Errorf("envelope Stdout = %q, Stderr = %q", env.Stdout, env.Stderr)
	}
	if len(env.Parts) != 2 || env.Parts[0].Name != "check" || env.Parts[1].Name != "ls" {
		t.Fatalf("Parts = %+v, want check and ls", env.Parts)
	}
	for _, p := range env.Parts {
		if p.Stdout != "" || p.Stderr != "" {
			t.Errorf("part %s carries Stdout %q, Stderr %q", p.Name, p.Stdout, p.Stderr)
		}
	}
}

func TestFilesCheckRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(lines []string) []string
		prefix string
		reason string
	}{
		{"an ancestor not owned by root", func(l []string) []string {
			l[3] = chainLine("drwxr-xr-x", "1000", filesTestChain[3])
			return l
		}, "/srv/focal-e2e/files", "not_root_owned"},
		{"group-writable ancestor whose group is root", func(l []string) []string {
			l[1] = chainLine("drwxrwxr-x", "0", "/srv")
			return l
		}, "/srv", "group_or_other_writable"},
		{"group-writable ancestor with setgid", func(l []string) []string {
			l[2] = chainLine("drwxrwsr-x", "0", "/srv/focal-e2e")
			return l
		}, "/srv/focal-e2e", "group_or_other_writable"},
		{"other-writable ancestor", func(l []string) []string {
			l[2] = chainLine("drwxr-xrwx", "0", "/srv/focal-e2e")
			return l
		}, "/srv/focal-e2e", "group_or_other_writable"},
		{"sticky other-writable ancestor like /tmp", func(l []string) []string {
			l[1] = chainLine("drwxrwxrwt", "0", "/srv")
			return l
		}, "/srv", "group_or_other_writable"},
		{"an ancestor that is a symlink", func(l []string) []string {
			l[2] = chainLine("lrwxrwxrwx", "0", "/srv/focal-e2e -> /mnt/elsewhere")
			return l
		}, "/srv/focal-e2e", "not_directory"},
		{"the directory itself is a symlink", func(l []string) []string {
			l[4] = chainLine("lrwxrwxrwx", "0", filesTestDir+" -> /srv/focal-e2e/files/outside")
			return l
		}, filesTestDir, "not_directory"},
		{"the directory itself is a file", func(l []string) []string {
			l[4] = chainLine("-rw-r--r--", "0", filesTestDir)
			return l
		}, filesTestDir, "not_directory"},
		{"an ACL on an ancestor", func(l []string) []string {
			l[3] = chainLine("drwxr-xr-x+", "0", filesTestChain[3])
			return l
		}, "/srv/focal-e2e/files", "acl_present"},
		{"the shallowest refusal wins", func(l []string) []string {
			l[3] = chainLine("drwxr-xr-x", "1000", filesTestChain[3])
			l[1] = chainLine("drwxr-xrwx", "0", "/srv")
			return l
		}, "/srv", "group_or_other_writable"},
		{"not owned by root comes before writable", func(l []string) []string {
			l[2] = chainLine("drwxrwxrwx", "1000", "/srv/focal-e2e")
			return l
		}, "/srv/focal-e2e", "not_root_owned"},
		{"an ACL comes before writable", func(l []string) []string {
			l[2] = chainLine("drwxrwxr-x+", "0", "/srv/focal-e2e")
			return l
		}, "/srv/focal-e2e", "acl_present"},
		{"a symlink comes before its owner", func(l []string) []string {
			l[2] = chainLine("lrwxrwxrwx", "1000", "/srv/focal-e2e -> x")
			return l
		}, "/srv/focal-e2e", "not_directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := tc.edit(rootChain(filesTestChain))
			env, rec := runFiles(t, resp(joinLines(lines), 0))
			assertOnlyTheCheckRan(t, rec)
			if env.Error == nil || env.Error.Code != "path_not_root_controlled" {
				t.Fatalf("Error = %v, want path_not_root_controlled", env.Error)
			}
			if env.Status != result.StatusFailed || env.ExitCode != 0 {
				t.Errorf("Status = %q, ExitCode = %d, want failed with the check's own 0", env.Status, env.ExitCode)
			}
			want := map[string]string{"path": filesTestDir, "complete": "false", "prefix": tc.prefix, "reason": tc.reason}
			if !reflect.DeepEqual(env.Data, want) {
				t.Errorf("Data = %v, want %v", env.Data, want)
			}
			if env.Items != nil {
				t.Errorf("Items = %v, want none", env.Items)
			}
			body, _ := env.Encode()
			for _, value := range []string{"1000", "rwx", "r-x", "/mnt", "outside", " -> ", "4096"} {
				if strings.Contains(string(body), value) {
					t.Errorf("the refusal carries the remote value %q:\n%s", value, body)
				}
			}
		})
	}
}

func TestFilesCheckAccepts(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(lines []string) []string
	}{
		{"setgid root directory (2755)", func(l []string) []string {
			l[3] = chainLine("drwxr-sr-x", "0", filesTestChain[3])
			return l
		}},
		{"setgid without group search (S)", func(l []string) []string {
			l[3] = chainLine("drwxr-Sr-x", "0", filesTestChain[3])
			return l
		}},
		{"sticky but not other-writable (t and T)", func(l []string) []string {
			l[1] = chainLine("drwxr-xr-t", "0", "/srv")
			l[2] = chainLine("drwxr-xr-T", "0", "/srv/focal-e2e")
			return l
		}},
		{"setuid bits on an ancestor", func(l []string) []string {
			l[1] = chainLine("drwsr-xr-x", "0", "/srv")
			return l
		}},
		{"an SELinux context dot", func(l []string) []string {
			l[2] = chainLine("drwxr-xr-x.", "0", "/srv/focal-e2e")
			return l
		}},
		{"the directory itself belongs to an application and is 777", func(l []string) []string {
			l[4] = chainLine("drwxrwxrwx", "1000", filesTestDir)
			return l
		}},
		{"the directory itself has an ACL", func(l []string) []string {
			l[4] = chainLine("drwxrwxr-x+", "1000", filesTestDir)
			return l
		}},
		{"ls reordered the lines", func(l []string) []string {
			slices.Reverse(l)
			return l
		}},
		{"columns padded with runs of spaces", func(l []string) []string {
			l[0] = "drwxr-xr-x  1    0    0 4096 2026-11-25 08:02:25.929625041 +0000 /"
			return l
		}},
		{"a non-numeric size from BLOCK_SIZE", func(l []string) []string {
			l[1] = "drwxr-xr-x 1 0 0 4.0K 2026-11-25 08:02:25.929625041 +0000 /srv"
			return l
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := tc.edit(rootChain(filesTestChain))
			env, rec := runFiles(t, resp(joinLines(lines), 0), resp(filesListingAL2023, 0))
			if len(rec.Calls()) != 2 {
				t.Fatalf("the listing did not run; Error = %v", env.Error)
			}
			if env.Error != nil {
				t.Errorf("Error = %v", env.Error)
			}
		})
	}
}

func TestFilesCheckFailures(t *testing.T) {
	good := rootChain(filesTestChain)
	edit := func(f func([]string) []string) string { return joinLines(f(slices.Clone(good))) }
	timeout := result.ExecutionError("timeout", "the command did not finish within 30s")
	for _, tc := range []struct {
		name string
		resp sshtest.Response
	}{
		{"mode of nine characters", resp(edit(func(l []string) []string { l[1] = chainLine("drwxr-xr-", "0", "/srv"); return l }), 0)},
		{"mode of twelve characters", resp(edit(func(l []string) []string { l[1] = chainLine("drwxr-xr-x+.", "0", "/srv"); return l }), 0)},
		{"an unknown trailing mode character", resp(edit(func(l []string) []string { l[1] = chainLine("drwxr-xr-x@", "0", "/srv"); return l }), 0)},
		{"a permission letter out of place", resp(edit(func(l []string) []string { l[1] = chainLine("dwrxr-xr-x", "0", "/srv"); return l }), 0)},
		{"a non-numeric owner", resp(edit(func(l []string) []string { l[1] = chainLine("drwxr-xr-x", "root", "/srv"); return l }), 0)},
		{"a line missing", resp(edit(func(l []string) []string { return l[:4] }), 0)},
		{"an extra well-formed line", resp(edit(func(l []string) []string { return append(l, chainLine("drwxr-xr-x", "0", "/etc")) }), 0)},
		{"an extra copy of a real line", resp(edit(func(l []string) []string { return append(l, l[2]) }), 0)},
		{"a duplicate in place of one", resp(edit(func(l []string) []string { l[3] = l[2]; return l }), 0)},
		{"a name that differs", resp(edit(func(l []string) []string { l[4] = chainLine("drwxr-xr-x", "0", filesTestDir+"x"); return l }), 0)},
		{"a name with a trailing space", resp(edit(func(l []string) []string { l[4] += " "; return l }), 0)},
		{"two spaces before the name", resp(edit(func(l []string) []string {
			l[4] = "drwxr-xr-x 1 0 0 4096 2026-11-25 08:02:25.929625041 +0000  " + filesTestDir
			return l
		}), 0)},
		{"a non-ASCII byte in a line", resp(edit(func(l []string) []string {
			l[1] = "drwxr-xr-x 1 0 0 4096 2026-11-25 08:02:25.929625041 +0000 /srv\u00a0"
			return l
		}), 0)},
		{"a symlink line without its arrow", resp(edit(func(l []string) []string { l[1] = chainLine("lrwxrwxrwx", "0", "/srv"); return l }), 0)},
		{"an empty answer", resp("", 0)},
		{"no trailing newline", resp(strings.TrimSuffix(joinLines(good), "\n"), 0)},
		{"a non-zero exit", resp(joinLines(good), 2)},
		{"ssh's own failure status", resp(joinLines(good), 255)},
		{"truncated", sshtest.Response{Output: ssh.Output{Stdout: joinLines(good), Truncated: true}}},
		{"truncated and killed", sshtest.Response{Output: ssh.Output{Stdout: joinLines(good), Truncated: true, ExitCode: -1}}},
		{"an executor error", sshtest.Response{Err: errors.New("ssh: connect to host example.com port 22: Connection refused")}},
		{"a timeout", sshtest.Response{Output: ssh.Output{ExitCode: -1}, Err: timeout}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, rec := runFiles(t, tc.resp, resp(filesListingAL2023, 0))
			assertOnlyTheCheckRan(t, rec)
			if env.Error == nil || env.Error.Code != "path_check_failed" {
				t.Fatalf("Error = %v, want path_check_failed", env.Error)
			}
			if env.Status == result.StatusOK {
				t.Error("Status = ok for a check that failed")
			}
			want := map[string]string{"path": filesTestDir, "complete": "false"}
			if !reflect.DeepEqual(env.Data, want) {
				t.Errorf("Data = %v, want %v (no prefix, no reason)", env.Data, want)
			}
			if len(env.Parts) != 1 || env.Parts[0].Name != "check" || env.Parts[0].Stdout != "" {
				t.Errorf("Parts = %+v, want the check alone, without its output", env.Parts)
			}
		})
	}
}

func TestFilesCheckFailureKeepsTheExecutorsStatus(t *testing.T) {
	timeout := result.ExecutionError("timeout", "the command did not finish within 30s")
	env, _ := runFiles(t, sshtest.Response{Output: ssh.Output{ExitCode: -1}, Err: timeout})
	if env.Status != result.StatusTimeout || env.ExitCode != -1 {
		t.Errorf("Status = %q, ExitCode = %d, want the check's timeout and -1", env.Status, env.ExitCode)
	}
	if env.Parts[0].Error == nil || env.Parts[0].Error.Code != "timeout" {
		t.Errorf("check part Error = %v, want the executor's timeout", env.Parts[0].Error)
	}
	env, _ = runFiles(t, resp(joinLines(rootChain(filesTestChain)), 2))
	if env.Status != result.StatusFailed || env.ExitCode != 2 {
		t.Errorf("Status = %q, ExitCode = %d, want failed and ls's own 2", env.Status, env.ExitCode)
	}
}

func listingResp(stdout string, exit int, truncated bool) sshtest.Response {
	return sshtest.Response{Output: ssh.Output{Stdout: stdout, ExitCode: exit, Truncated: truncated, Duration: 5 * time.Millisecond}}
}

func listingOf(lines ...string) string { return joinLines(append([]string{"total 8"}, lines...)) }

func TestFilesListingLines(t *testing.T) {
	const ts = " 2026-01-02 03:04:05.000000000 +0900 "
	for _, tc := range []struct {
		name    string
		line    string
		want    map[string]string // nil: the line is skipped
		comment string
	}{
		{name: "a regular file", line: "-rw-r--r-- 1 6" + ts + "regular.txt",
			want: map[string]string{"name": "regular.txt", "type": "file", "size": "6", "mtime": "2026-01-02T03:04:05+09:00"}},
		{name: "a directory", line: "drwxr-xr-x 2 4096" + ts + "subdir",
			want: map[string]string{"name": "subdir", "type": "directory", "size": "4096", "mtime": "2026-01-02T03:04:05+09:00"}},
		{name: "a symlink whose target is dropped", line: "lrwxrwxrwx 1 11" + ts + "link -> /etc/shadow",
			want: map[string]string{"name": "link", "type": "symlink", "size": "11", "mtime": "2026-01-02T03:04:05+09:00"}},
		{name: "a symlink named with a trailing arrow", line: `lrwxrwxrwx 1 11` + ts + `a\ -> -> target`,
			want: map[string]string{"name": `a\ ->`, "type": "symlink", "size": "11", "mtime": "2026-01-02T03:04:05+09:00"}},
		{name: "a file named with an arrow", line: `-rw-r--r-- 1 4` + ts + `name\ ->\ arrow`,
			want: map[string]string{"name": `name\ ->\ arrow`, "type": "file", "size": "4", "mtime": "2026-01-02T03:04:05+09:00"}},
		{name: "an escaped backslash stays escaped", line: `-rw-r--r-- 1 1` + ts + `a\\b`,
			want: map[string]string{"name": `a\\b`, "type": "file", "size": "1", "mtime": "2026-01-02T03:04:05+09:00"}},
		{name: "an escaped C-locale non-ASCII name stays escaped", line: `-rw-r--r-- 1 0` + ts + `caf\303\251`,
			want: map[string]string{"name": `caf\303\251`, "type": "file", "size": "0", "mtime": "2026-01-02T03:04:05+09:00"}},
		{name: "a socket is other", line: "srwxrwxrwx 1 0" + ts + "app.sock",
			want: map[string]string{"name": "app.sock", "type": "other", "size": "0", "mtime": "2026-01-02T03:04:05+09:00"}},
		{name: "a fifo is other", line: "prw-r--r-- 1 0" + ts + "fifo",
			want: map[string]string{"name": "fifo", "type": "other", "size": "0", "mtime": "2026-01-02T03:04:05+09:00"}},
		{name: "an SELinux dot after the mode", line: "-rw-r--r--. 1 6" + ts + "labelled",
			want: map[string]string{"name": "labelled", "type": "file", "size": "6", "mtime": "2026-01-02T03:04:05+09:00"}},
		{name: "an ACL plus after the mode", line: "-rw-rw-r--+ 1 6" + ts + "acl",
			want:    map[string]string{"name": "acl", "type": "file", "size": "6", "mtime": "2026-01-02T03:04:05+09:00"},
			comment: "the form the ACL plus is assumed to take in -g -o output; the e2e fixture takes it from a real ls"},
		{name: "a UTC timestamp keeps its offset", line: "-rw-r--r-- 1 6 2026-11-25 08:02:25.932625085 +0000 utc",
			want: map[string]string{"name": "utc", "type": "file", "size": "6", "mtime": "2026-11-25T08:02:25+00:00"}},
		{name: "a negative offset", line: "-rw-r--r-- 1 6 2026-11-25 08:02:25.932625085 -0330 nst",
			want: map[string]string{"name": "nst", "type": "file", "size": "6", "mtime": "2026-11-25T08:02:25-03:30"}},
		{name: "a large file", line: "-rw-r--r-- 1 18446744073709551615" + ts + "huge",
			want: map[string]string{"name": "huge", "type": "file", "size": "18446744073709551615", "mtime": "2026-01-02T03:04:05+09:00"}},

		{name: "a device's major, minor", line: "crw-rw-rw- 1 1, 7" + ts + "full"},
		{name: "a question-mark line from exit 1", line: "-????????? ? ?                                   ? f1"},
		{name: "an unknown type letter", line: "Xrw-r--r-- 1 6" + ts + "odd"},
		{name: "a size above uint64", line: "-rw-r--r-- 1 18446744073709551616" + ts + "huge"},
		{name: "a human-readable size", line: "-rw-r--r-- 1 4.0K" + ts + "human"},
		{name: "a missing column", line: "-rw-r--r-- 1 2026-01-02 03:04:05.000000000 +0900 short"},
		{name: "a date that is not a date", line: "-rw-r--r-- 1 6 2026-13-02 03:04:05.000000000 +0900 bad"},
		{name: "a time without nanoseconds", line: "-rw-r--r-- 1 6 2026-01-02 03:04:05 +0900 bad"},
		{name: "a locale-style date", line: "-rw-r--r-- 1 6 Jan  2 03:04 bad"},
		{name: "an offset with a colon", line: "-rw-r--r-- 1 6 2026-01-02 03:04:05.000000000 +09:00 bad"},
		{name: "an unescaped space in a name", line: "-rw-r--r-- 1 6" + ts + "two words"},
		{name: "two spaces before the name", line: "-rw-r--r-- 1 6" + ts + " lead"},
		{name: "an unpaired trailing backslash", line: `-rw-r--r-- 1 6` + ts + `trail\`},
		{name: "a symlink without its arrow", line: "lrwxrwxrwx 1 6" + ts + "link"},
		{name: "a symlink with an empty target", line: "lrwxrwxrwx 1 6" + ts + "link -> "},
		{name: "a raw control character", line: "-rw-r--r-- 1 6" + ts + "bell\x07"},
		{name: "a raw DEL", line: "-rw-r--r-- 1 6" + ts + "del\x7f"},
		{name: "a raw UTF-8 name", line: "-rw-r--r-- 1 6" + ts + "café"},
		{name: "a no-break space", line: "-rw-r--r-- 1 6" + ts + "a\u00a0b"},
		{name: "an ideographic space", line: "-rw-r--r-- 1 6" + ts + "a\u3000b"},
		{name: "a bidi override", line: "-rw-r--r-- 1 6" + ts + "txt.\u202eexe"},
		{name: "a lone high byte", line: "-rw-r--r-- 1 6" + ts + "x\xff"},
		{name: "a tab between columns", line: "-rw-r--r--\t1 6" + ts + "tab"},
		{name: "an empty line", line: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = tc.comment
			env, _ := runFiles(t, okChain(), listingResp(listingOf(tc.line), 0, false))
			if env.Error != nil {
				t.Fatalf("Error = %v", env.Error)
			}
			if tc.want == nil {
				if len(env.Items) != 0 {
					t.Errorf("Items = %v, want the line skipped", env.Items)
				}
				if env.Data["skipped_lines"] != "1" || env.Data["complete"] != "false" {
					t.Errorf("Data = %v, want one skipped line and complete=false", env.Data)
				}
				return
			}
			if len(env.Items) != 1 || !reflect.DeepEqual(env.Items[0], tc.want) {
				t.Errorf("Items = %v, want [%v]", env.Items, tc.want)
			}
			if env.Data["skipped_lines"] != "0" || env.Data["complete"] != "true" {
				t.Errorf("Data = %v", env.Data)
			}
		})
	}
}

func TestFilesListingSummaryLine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stdout string
		ok     bool
	}{
		{"total", "total 12\n", true},
		{"total in bytes", "total 20480\n", true},
		{"a translated word", "合計 12\n", true},
		{"no summary line: ls was given a symlink", "lrwxrwxrwx 1 28 2026-11-25 08:02:25.932625085 +0000 /srv/focal-e2e/files/allowed-link -> /srv/focal-e2e/files/allowed\n", false},
		{"no summary line: ls was given a file", "-rw-r--r-- 1 3 2026-11-25 08:02:25.932625085 +0000 /srv/a\n", false},
		{"a human-readable total", "total 20K\n", false},
		{"three words", "total size 12\n", false},
		{"nothing at all", "", false},
		{"an empty first line", "\ntotal 12\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, rec := runFiles(t, okChain(), listingResp(tc.stdout, 0, false))
			if len(rec.Calls()) != 2 {
				t.Fatal("the listing did not run")
			}
			if tc.ok {
				if env.Error != nil {
					t.Errorf("Error = %v", env.Error)
				}
				return
			}
			if env.Error == nil || env.Error.Code != "unexpected_listing" {
				t.Fatalf("Error = %v, want unexpected_listing", env.Error)
			}
			if env.Items != nil || env.Status != result.StatusFailed || env.ExitCode != 0 {
				t.Errorf("Items = %v, Status = %q, ExitCode = %d", env.Items, env.Status, env.ExitCode)
			}
			if env.Data["complete"] != "false" {
				t.Errorf("Data = %v", env.Data)
			}
		})
	}
}

// TestFilesExitStatusTable walks every row of ADR 0012's table for the
// listing's exit status, Truncated and an executor error.
func TestFilesExitStatusTable(t *testing.T) {
	const two = "total 8\n-rw-r--r-- 1 1 2026-01-02 03:04:05.000000000 +0900 a\n-rw-r--r-- 1 2 2026-01-02 03:04:05.000000000 +0900 b\n"
	timeout := result.ExecutionError("timeout", "the command did not finish within 30s")
	for _, tc := range []struct {
		name     string
		resp     sshtest.Response
		items    []string // names; nil means no items at all
		complete string
		skipped  string
		status   result.Status
		exit     int
	}{
		{"0, not truncated", listingResp(two, 0, false), []string{"a", "b"}, "true", "0", result.StatusOK, 0},
		{"0, truncated (SSM's ceiling)", listingResp(two, 0, true), []string{"a"}, "false", "0", result.StatusTruncated, 0},
		{"-1, truncated (killed at max_output)", listingResp(two, -1, true), []string{"a"}, "false", "0", result.StatusTruncated, -1},
		{"1, not truncated", listingResp(two, 1, false), []string{"a", "b"}, "false", "0", result.StatusFailed, 1},
		{"1, truncated", listingResp(two, 1, true), []string{"a"}, "false", "0", result.StatusTruncated, 1},
		{"1, with question-mark lines", listingResp(filesQuestionMarkListing, 1, false), []string{}, "false", "2", result.StatusFailed, 1},
		{"2", listingResp(two, 2, false), nil, "false", "", result.StatusFailed, 2},
		{"127: no ls", listingResp("", 127, false), nil, "false", "", result.StatusFailed, 127},
		{"-1, not truncated", listingResp(two, -1, false), nil, "false", "", result.StatusFailed, -1},
		{"an executor error", sshtest.Response{Output: ssh.Output{Stdout: two}, Err: errors.New("ssh: connection reset")}, nil, "false", "", result.StatusFailed, -1},
		{"a timeout", sshtest.Response{Output: ssh.Output{Stdout: two, ExitCode: -1}, Err: timeout}, nil, "false", "", result.StatusTimeout, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := runFiles(t, okChain(), tc.resp)
			if env.Status != tc.status || env.ExitCode != tc.exit {
				t.Errorf("Status = %q, ExitCode = %d, want %q and %d", env.Status, env.ExitCode, tc.status, tc.exit)
			}
			if env.Data["complete"] != tc.complete || env.Data["skipped_lines"] != tc.skipped {
				t.Errorf("Data = %v, want complete=%s skipped_lines=%q", env.Data, tc.complete, tc.skipped)
			}
			if tc.items == nil {
				if env.Items != nil {
					t.Errorf("Items = %v, want none", env.Items)
				}
				if _, ok := env.Data["skipped_lines"]; ok {
					t.Errorf("Data = %v carries skipped_lines without a listing", env.Data)
				}
				return
			}
			var names []string
			for _, it := range env.Items {
				names = append(names, it["name"])
			}
			if !slices.Equal(names, tc.items) {
				t.Errorf("names = %q, want %q", names, tc.items)
			}
		})
	}
}

// TestFilesUnterminatedOutputDropsTheLastLine covers the cut that Truncated
// did not report: the half line is not an entry.
func TestFilesUnterminatedOutputDropsTheLastLine(t *testing.T) {
	cut := "total 8\n-rw-r--r-- 1 1 2026-01-02 03:04:05.000000000 +0900 a\n-rw-r--r-- 1 2 2026-01-02 03:04:05.000000000 +0900 secret-con"
	env, _ := runFiles(t, okChain(), listingResp(cut, 0, false))
	if len(env.Items) != 1 || env.Items[0]["name"] != "a" {
		t.Errorf("Items = %v, want only a", env.Items)
	}
	if env.Data["complete"] != "false" || env.Data["skipped_lines"] != "0" {
		t.Errorf("Data = %v", env.Data)
	}
	if env.Status != result.StatusOK || env.ExitCode != 0 {
		t.Errorf("Status = %q, ExitCode = %d: the executor's own answer is not rewritten", env.Status, env.ExitCode)
	}
}

// TestFilesForgedLinesAreNotEntries pins the line-forging case end to end: a
// name holding a newline and a whole ls line comes back as one escaped name.
func TestFilesForgedLinesAreNotEntries(t *testing.T) {
	env, _ := runFiles(t, okChain(), resp(filesListingAL2023, 0))
	if got := len(env.Items); got != 8 {
		t.Fatalf("%d items, want the 8 real entries", got)
	}
	for _, it := range env.Items {
		if it["name"] == "forged" {
			t.Error("a forged line became an entry")
		}
	}
	if env.Data["skipped_lines"] != "0" {
		t.Errorf("skipped_lines = %s", env.Data["skipped_lines"])
	}
}

func TestFilesSumsDurationsAndTruncation(t *testing.T) {
	env, _ := runFiles(t,
		sshtest.Response{Output: ssh.Output{Stdout: joinLines(rootChain(filesTestChain)), Duration: 7 * time.Millisecond}},
		listingResp(filesListingAL2023, 0, true),
	)
	if env.DurationMs != 12 {
		t.Errorf("DurationMs = %d, want 7+5", env.DurationMs)
	}
	if !env.Truncated {
		t.Error("Truncated = false with a truncated listing")
	}
}

func TestFilesRefusesTheZeroPath(t *testing.T) {
	rec := &sshtest.Recorder{}
	_, err := NewFiles(vo.FilesPath{}).Execute(context.Background(), rec, packageTarget(t), filesPolicy(t))
	if err == nil {
		t.Fatal("Execute accepted the zero FilesPath")
	}
	if len(rec.Calls()) != 0 {
		t.Error("a command ran for the zero FilesPath")
	}
}

func TestRegistryFilesGoesThroughVOParse(t *testing.T) {
	r := NewRegistry(nil)
	list, err := vo.ParseFilesAllowList([]string{"/var/log"})
	if err != nil {
		t.Fatalf("ParseFilesAllowList: %v", err)
	}
	for _, tc := range []struct {
		params Params
		code   string
	}{
		{Params{Path: "/var/log/nginx", FilesAllowList: list}, ""},
		{Params{Path: "/var/log", FilesAllowList: list}, ""},
		{Params{Path: "", FilesAllowList: list}, "invalid_path"},
		{Params{Path: "/var/log/../../etc", FilesAllowList: list}, "invalid_path"},
		{Params{Path: "/var/log2", FilesAllowList: list}, "path_not_allowed"},
		{Params{Path: "/var/log"}, "path_not_allowed"},
	} {
		op, buildErr := r.Build("files", tc.params)
		if tc.code == "" {
			if buildErr != nil {
				t.Errorf("Build(files, %q): %v", tc.params.Path, buildErr)
			} else if op.Name() != "files" {
				t.Errorf("Name() = %q", op.Name())
			}
			continue
		}
		if buildErr == nil || buildErr.Code != tc.code {
			t.Errorf("Build(files, %q) = %v, want %s", tc.params.Path, buildErr, tc.code)
		}
	}
}
