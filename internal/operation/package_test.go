// package_test.go is an internal test: the Status-line parse and the rule that
// decides which of the two package managers' answers an Envelope reports are
// unexported, and they are the part of this operation worth pinning down.
package operation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
	"github.com/youyo/focal/internal/vo"
)

const dpkgInstalledSample = `Package: procps
Status: install ok installed
Priority: important
Section: admin
Installed-Size: 2240
Maintainer: Ubuntu Developers <ubuntu-devel-discuss@lists.ubuntu.com>
Architecture: arm64
Version: 2:4.0.4-4ubuntu3
Description: /proc file system utilities
 Status: install ok installed
 Version: 9.9
`

const dpkgRemovedSample = `Package: cron
Status: deinstall ok config-files
Priority: important
Version: 3.0pl1-184ubuntu2
Description: process scheduling daemon
`

func packageTarget(t *testing.T) vo.Target {
	t.Helper()
	target, err := vo.ParseTarget("example.com")
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	return target
}

func packagePolicy(t *testing.T) policy.Policy {
	t.Helper()
	p, err := policy.Resolve("package", policy.SudoNever)
	if err != nil {
		t.Fatalf("policy.Resolve: %v", err)
	}
	return p
}

func packageMustNew(t *testing.T, name string) Package {
	t.Helper()
	parsed, err := vo.ParsePackageName(name)
	if err != nil {
		t.Fatalf("ParsePackageName(%q): %v", name, err)
	}
	return NewPackage(parsed)
}

func runPackage(t *testing.T, responses ...sshtest.Response) (result.Envelope, *sshtest.Recorder) {
	t.Helper()
	rec := &sshtest.Recorder{}
	rec.RespondSeq(responses...)
	env, err := packageMustNew(t, "procps").Execute(context.Background(), rec, packageTarget(t), packagePolicy(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return env, rec
}

func resp(stdout string, exit int) sshtest.Response {
	return sshtest.Response{Output: ssh.Output{Stdout: stdout, ExitCode: exit, Duration: 10 * time.Millisecond}}
}

func partNames(env result.Envelope) []string {
	var names []string
	for _, p := range env.Parts {
		names = append(names, p.Name)
	}
	return names
}

func TestPackageFieldIsAPackageName(t *testing.T) {
	typ := reflect.TypeOf(Package{})
	if typ.NumField() != 1 {
		t.Fatalf("Package has %d fields, want 1", typ.NumField())
	}
	if got := typ.Field(0).Type; got != reflect.TypeOf(vo.PackageName{}) {
		t.Errorf("field %q is %s, want vo.PackageName", typ.Field(0).Name, got)
	}
}

func TestPackageName(t *testing.T) {
	if got := (Package{}).Name(); got != "package" {
		t.Errorf("Name() = %q, want package", got)
	}
}

// TestPackageRPMAnswersAndDpkgIsNotRun: exit 0 from rpm ends the run.
func TestPackageRPMAnswersAndDpkgIsNotRun(t *testing.T) {
	env, rec := runPackage(t, resp("bash-5.2.15-1.amzn2023.0.2.aarch64\n", 0))

	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want exactly 1 (rpm only)", len(calls))
	}
	if calls[0].Command.Program() != "rpm" || !reflect.DeepEqual(calls[0].Command.Args(), []string{"-q", "procps"}) {
		t.Errorf("first call = %s %v, want rpm -q procps", calls[0].Command.Program(), calls[0].Command.Args())
	}
	if calls[0].Command.Sudo() != policy.SudoNever {
		t.Errorf("Sudo() = %v, want SudoNever", calls[0].Command.Sudo())
	}
	if env.Operation != "package" || env.Host != "example.com" {
		t.Errorf("Operation/Host = %q/%q", env.Operation, env.Host)
	}
	if env.Status != result.StatusOK || env.ExitCode != 0 || env.Error != nil {
		t.Errorf("Status/ExitCode/Error = %q/%d/%v, want ok/0/nil", env.Status, env.ExitCode, env.Error)
	}
	wantData := map[string]string{"manager": "rpm", "installed": "true", "version": "bash-5.2.15-1.amzn2023.0.2.aarch64"}
	if !reflect.DeepEqual(env.Data, wantData) {
		t.Errorf("Data = %v, want %v", env.Data, wantData)
	}
	if got := partNames(env); !reflect.DeepEqual(got, []string{"rpm"}) {
		t.Errorf("Parts = %v, want [rpm]", got)
	}
}

// TestPackageRPMExit1RunsDpkg: any non-zero exit other than ssh's own — here
// rpm's 1 — makes dpkg-query run, and its answer is the one the Envelope
// carries.
func TestPackageRPMExit1RunsDpkg(t *testing.T) {
	env, rec := runPackage(t,
		resp("package procps is not installed\n", 1),
		resp(dpkgInstalledSample, 0),
	)

	calls := rec.Calls()
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	if calls[1].Command.Program() != "dpkg-query" || !reflect.DeepEqual(calls[1].Command.Args(), []string{"-s", "procps"}) {
		t.Errorf("second call = %s %v, want dpkg-query -s procps", calls[1].Command.Program(), calls[1].Command.Args())
	}
	if env.Status != result.StatusOK || env.ExitCode != 0 {
		t.Errorf("Status/ExitCode = %q/%d, want ok/0 (taken from the manager that answered yes)", env.Status, env.ExitCode)
	}
	wantData := map[string]string{"manager": "dpkg", "installed": "true", "version": "2:4.0.4-4ubuntu3"}
	if !reflect.DeepEqual(env.Data, wantData) {
		t.Errorf("Data = %v, want %v", env.Data, wantData)
	}
	if got := partNames(env); !reflect.DeepEqual(got, []string{"rpm", "dpkg"}) {
		t.Errorf("Parts = %v, want [rpm dpkg]", got)
	}
	if env.Parts[0].ExitCode != 1 || env.Parts[0].Status != result.StatusFailed {
		t.Errorf("rpm Part = %d/%q, want 1/failed kept as it was", env.Parts[0].ExitCode, env.Parts[0].Status)
	}
	if env.DurationMs != 20 {
		t.Errorf("DurationMs = %d, want 20 (sum of parts)", env.DurationMs)
	}
}

func TestPackageRPMExit127DpkgInstalled(t *testing.T) {
	env, _ := runPackage(t,
		resp("", 127),
		resp(dpkgInstalledSample, 0),
	)
	if env.Status != result.StatusOK || env.ExitCode != 0 {
		t.Errorf("Status/ExitCode = %q/%d, want ok/0", env.Status, env.ExitCode)
	}
	if env.Data["manager"] != "dpkg" || env.Data["installed"] != "true" {
		t.Errorf("Data = %v", env.Data)
	}
}

func TestPackageBothManagersMissing(t *testing.T) {
	env, rec := runPackage(t, resp("", 127), resp("", 127))
	if len(rec.Calls()) != 2 {
		t.Fatalf("got %d calls, want 2", len(rec.Calls()))
	}
	if env.Status != result.StatusFailed || env.ExitCode != 127 {
		t.Errorf("Status/ExitCode = %q/%d, want failed/127", env.Status, env.ExitCode)
	}
	if env.Error == nil || env.Error.Code != "package_manager_not_found" || env.Error.Kind != result.KindExecution {
		t.Errorf("Error = %+v, want execution package_manager_not_found", env.Error)
	}
	if _, ok := env.Data["manager"]; ok {
		t.Errorf("Data = %v: no manager answered, so none may be named", env.Data)
	}
	if got := partNames(env); !reflect.DeepEqual(got, []string{"rpm", "dpkg"}) {
		t.Errorf("Parts = %v, want [rpm dpkg]", got)
	}
}

func TestPackageNotInstalledOnDpkgHost(t *testing.T) {
	env, _ := runPackage(t,
		resp("", 127),
		sshtest.Response{Output: ssh.Output{Stderr: "dpkg-query: package 'x' is not installed and no information is available\n", ExitCode: 1}},
	)
	if env.Status != result.StatusFailed || env.ExitCode != 1 {
		t.Errorf("Status/ExitCode = %q/%d, want failed/1", env.Status, env.ExitCode)
	}
	want := map[string]string{"manager": "dpkg", "installed": "false"}
	if !reflect.DeepEqual(env.Data, want) {
		t.Errorf("Data = %v, want %v", env.Data, want)
	}
	if env.Error != nil {
		t.Errorf("Error = %+v, want nil: a package that is simply absent is not an execution error", env.Error)
	}
}

// TestPackageNotInstalledOnRPMHost: dpkg-query is missing, so rpm is the last
// manager that answered and its exit status is the one reported.
func TestPackageNotInstalledOnRPMHost(t *testing.T) {
	env, _ := runPackage(t, resp("package x is not installed\n", 1), resp("", 127))
	if env.Status != result.StatusFailed || env.ExitCode != 1 {
		t.Errorf("Status/ExitCode = %q/%d, want failed/1", env.Status, env.ExitCode)
	}
	want := map[string]string{"manager": "rpm", "installed": "false"}
	if !reflect.DeepEqual(env.Data, want) {
		t.Errorf("Data = %v, want %v", env.Data, want)
	}
}

// TestPackageNotInstalledOnBothManagers: both answered no; dpkg answered last.
func TestPackageNotInstalledOnBothManagers(t *testing.T) {
	env, _ := runPackage(t, resp("", 1), resp("", 1))
	want := map[string]string{"manager": "dpkg", "installed": "false"}
	if !reflect.DeepEqual(env.Data, want) {
		t.Errorf("Data = %v, want %v", env.Data, want)
	}
	if env.Status != result.StatusFailed || env.ExitCode != 1 {
		t.Errorf("Status/ExitCode = %q/%d, want failed/1", env.Status, env.ExitCode)
	}
}

// TestPackageDpkgRemovedButConfigured: dpkg-query -s exits 0 for a package
// that was removed and kept its configuration; the Status line says it is not
// installed, and the Envelope must too.
func TestPackageDpkgRemovedButConfigured(t *testing.T) {
	env, _ := runPackage(t, resp("", 127), resp(dpkgRemovedSample, 0))
	if env.Data["installed"] != "false" || env.Data["manager"] != "dpkg" {
		t.Errorf("Data = %v, want dpkg / installed=false", env.Data)
	}
	if _, ok := env.Data["version"]; ok {
		t.Errorf("Data = %v: a package that is not installed has no installed version", env.Data)
	}
	if env.Status != result.StatusFailed || env.ExitCode != 0 {
		t.Errorf("Status/ExitCode = %q/%d, want failed/0: the exit status is dpkg-query's own, not a synthesized one", env.Status, env.ExitCode)
	}
	if env.Error == nil || env.Error.Kind != result.KindExecution || env.Error.Code != "package_not_installed" {
		t.Errorf("Error = %+v, want execution package_not_installed", env.Error)
	} else if strings.Contains(env.Error.Message, "cron") || strings.Contains(env.Error.Message, "config-files") {
		t.Errorf("Error.Message %q carries remote output", env.Error.Message)
	}
	if env.Parts[1].Status != result.StatusOK || env.Parts[1].ExitCode != 0 {
		t.Errorf("dpkg Part = %q/%d, want the command's own ok/0", env.Parts[1].Status, env.Parts[1].ExitCode)
	}
}

func TestPackageDpkgHeldIsInstalled(t *testing.T) {
	held := strings.Replace(dpkgInstalledSample, "install ok installed\nPriority", "hold ok installed\nPriority", 1)
	env, _ := runPackage(t, resp("", 127), resp(held, 0))
	if env.Data["installed"] != "true" {
		t.Errorf("Data = %v, want installed=true for hold ok installed", env.Data)
	}
}

// TestPackageDpkgStatusIsReadFromFieldsOnly: a continuation line of the
// description that happens to read "Status: install ok installed" is not the
// package's status.
func TestPackageDpkgStatusIsReadFromFieldsOnly(t *testing.T) {
	spoof := "Package: x\nStatus: deinstall ok config-files\nDescription: x\n Status: install ok installed\n Version: 9.9\n"
	env, _ := runPackage(t, resp("", 127), resp(spoof, 0))
	if env.Data["installed"] != "false" {
		t.Errorf("Data = %v, want installed=false", env.Data)
	}
}

func TestPackageDpkgInstalledWithoutVersionLine(t *testing.T) {
	env, _ := runPackage(t, resp("", 127), resp("Package: x\nStatus: install ok installed\n", 0))
	if env.Data["installed"] != "true" {
		t.Errorf("Data = %v", env.Data)
	}
	if _, ok := env.Data["version"]; ok {
		t.Errorf("Data = %v: version is optional and must be absent when not reported", env.Data)
	}
}

func TestPackageSSHFailureStopsTheRun(t *testing.T) {
	timeout := result.ExecutionError("timeout", "command timed out after 30s")
	cases := []struct {
		name       string
		first      sshtest.Response
		second     *sshtest.Response
		wantCalls  int
		wantStatus result.Status
		wantExit   int
		wantParts  []string
		wantErr    bool
	}{
		{"rpm execErr", sshtest.Response{Err: errors.New("boom")}, nil, 1, result.StatusFailed, -1, []string{"rpm"}, true},
		{"rpm exit 255", resp("", 255), nil, 1, result.StatusFailed, 255, []string{"rpm"}, false},
		{"rpm timeout", sshtest.Response{Err: timeout}, nil, 1, result.StatusTimeout, -1, []string{"rpm"}, true},
		{"dpkg execErr", resp("", 1), &sshtest.Response{Err: errors.New("boom")}, 2, result.StatusFailed, -1, []string{"rpm", "dpkg"}, true},
		{"dpkg exit 255", resp("", 127), ptr(resp("", 255)), 2, result.StatusFailed, 255, []string{"rpm", "dpkg"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			responses := []sshtest.Response{tc.first}
			if tc.second != nil {
				responses = append(responses, *tc.second)
			}
			env, rec := runPackage(t, responses...)
			if len(rec.Calls()) != tc.wantCalls {
				t.Errorf("got %d calls, want %d", len(rec.Calls()), tc.wantCalls)
			}
			if env.Status != tc.wantStatus || env.ExitCode != tc.wantExit {
				t.Errorf("Status/ExitCode = %q/%d, want %q/%d", env.Status, env.ExitCode, tc.wantStatus, tc.wantExit)
			}
			if (env.Error != nil) != tc.wantErr {
				t.Errorf("Error = %+v, wantErr %v", env.Error, tc.wantErr)
			}
			if got := partNames(env); !reflect.DeepEqual(got, tc.wantParts) {
				t.Errorf("Parts = %v, want %v", got, tc.wantParts)
			}
			if len(env.Data) != 0 {
				t.Errorf("Data = %v: nothing was learned about the package", env.Data)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// TestPackageDecisionIgnoresOutputText: the only thing that decides whether
// dpkg-query runs is rpm's exit status; what rpm printed does not.
func TestPackageDecisionIgnoresOutputText(t *testing.T) {
	for _, text := range []string{"bash: rpm: command not found\n", "package procps is not installed\n", "", "anything at all\n"} {
		rec := &sshtest.Recorder{}
		rec.RespondSeq(
			sshtest.Response{Output: ssh.Output{Stderr: text, ExitCode: 1}},
			resp("", 1),
		)
		if _, err := packageMustNew(t, "procps").Execute(context.Background(), rec, packageTarget(t), packagePolicy(t)); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if len(rec.Calls()) != 2 {
			t.Errorf("rpm exit 1 with stderr %q: %d calls, want 2", text, len(rec.Calls()))
		}
	}
	// Exit 0 with text that reads like a failure still ends the run.
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{Stdout: "package procps is not installed\n", ExitCode: 0}, nil)
	env, _ := packageMustNew(t, "procps").Execute(context.Background(), rec, packageTarget(t), packagePolicy(t))
	if len(rec.Calls()) != 1 || env.Data["installed"] != "true" {
		t.Errorf("exit 0 must mean installed regardless of text: calls=%d Data=%v", len(rec.Calls()), env.Data)
	}
}

// TestPackageRPMVersionOnlyForASingleVersion: rpm -q prints one line per
// installed version and exits 0, in whatever order its database holds them, so
// with more than one line no version is singled out. The two lines are the
// shape observed on Amazon Linux 2023 for gpg-pubkey with two keys imported
// (test/e2e/al2023_test.go asserts it against the real container).
func TestPackageRPMVersionOnlyForASingleVersion(t *testing.T) {
	env, _ := runPackage(t, resp("gpg-pubkey-d832c631-6515c85e\ngpg-pubkey-2df94969-6b067363\n", 0))
	if _, ok := env.Data["version"]; ok {
		t.Errorf("version = %q: several versions are installed, so none is the version", env.Data["version"])
	}
	if env.Data["installed"] != "true" || env.Data["manager"] != "rpm" {
		t.Errorf("Data = %v, want rpm / installed=true", env.Data)
	}
	if got := env.Parts[0].Stdout; !strings.Contains(got, "6515c85e") || !strings.Contains(got, "6b067363") {
		t.Errorf("rpm Part stdout = %q, want every version kept", got)
	}

	// Blank lines are not versions.
	env, _ = runPackage(t, resp("\nglibc-2.34-1.x86_64\n\n", 0))
	if env.Data["version"] != "glibc-2.34-1.x86_64" {
		t.Errorf("version = %q, want the only non-blank line", env.Data["version"])
	}

	env, _ = runPackage(t, resp(strings.Repeat("a", 300)+"\n", 0))
	if _, ok := env.Data["version"]; ok {
		t.Errorf("a 300-byte line was promoted to Data[version]")
	}
}
