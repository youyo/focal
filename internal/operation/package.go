package operation

import (
	"context"
	"strings"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// Package is the package operation: whether one OS package is installed, asked
// of whichever package manager the host has. It holds only the package name,
// already parsed.
//
// Focal does not know in advance whether a host is RPM- or dpkg-based, so it
// asks rpm first and dpkg-query second. The choice between the two is made on
// exit statuses alone — never on what either program printed — for the same
// reason the executor's sudo retry reads an exit-status table rather than
// stderr: remote output and the remote locale must not decide what Focal runs
// next.
type Package struct {
	name vo.PackageName
}

var _ Operation = Package{}

// NewPackage builds the operation for one already-validated package name.
func NewPackage(name vo.PackageName) Package { return Package{name: name} }

// Name returns the operation name.
func (o Package) Name() string { return "package" }

// Package manager names as they appear in Part names and in Data["manager"].
const (
	managerRPM  = "rpm"
	managerDpkg = "dpkg"
)

// exitCommandNotFound is the status a shell gives for a program that is not on
// the host, and the only status that means "this manager did not answer".
const exitCommandNotFound = 127

// exitSSHFailure is ssh(1)'s own status for a connection that could not be
// made or kept. Nothing the remote host would have answered can follow it.
const exitSSHFailure = 255

// packageVersionMaxLen bounds what is promoted to Data["version"]. The text
// comes from the remote host, and a line longer than any real version string
// is left in the Part's stdout rather than lifted into structured data.
const packageVersionMaxLen = 256

// Execute asks rpm, and asks dpkg-query only if rpm did not report the package
// installed. It stops at the first ssh failure (an error from the executor, or
// exit status 255), keeps every attempt as a Part, and takes the Envelope's
// Status and ExitCode from the manager whose answer decides the result — not
// from the worst of the Parts, because rpm failing on a dpkg host is the
// expected way for the first attempt to go.
func (o Package) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	rpmCmd, cmdErr := ssh.RPMQueryCommand(p, o.name)
	if cmdErr != nil {
		return result.Envelope{}, cmdErr
	}
	rpmOut, rpmErr := ex.Execute(ctx, t, rpmCmd)
	rpmPart := buildPart(managerRPM, rpmOut, rpmErr)
	parts := []result.Part{rpmPart}

	switch {
	case sshFailed(rpmOut, rpmErr):
		return o.envelope(t, parts, rpmPart, nil, nil), nil
	case rpmErr == nil && rpmOut.ExitCode == 0:
		return o.envelope(t, parts, rpmPart, packageData(managerRPM, true, soleLine(rpmOut.Stdout)), nil), nil
	}

	dpkgCmd, cmdErr := ssh.DpkgStatusCommand(p, o.name)
	if cmdErr != nil {
		return result.Envelope{}, cmdErr
	}
	dpkgOut, dpkgErr := ex.Execute(ctx, t, dpkgCmd)
	dpkgPart := buildPart(managerDpkg, dpkgOut, dpkgErr)
	parts = append(parts, dpkgPart)

	rpmAnswered := rpmOut.ExitCode != exitCommandNotFound
	switch {
	case sshFailed(dpkgOut, dpkgErr):
		return o.envelope(t, parts, dpkgPart, nil, nil), nil

	case dpkgOut.ExitCode == exitCommandNotFound && !rpmAnswered:
		notFound := result.ExecutionError("package_manager_not_found",
			"neither rpm nor dpkg-query is available on the host")
		return o.envelope(t, parts, dpkgPart, nil, notFound), nil

	case dpkgOut.ExitCode == exitCommandNotFound:
		// dpkg-query is absent, so rpm is the last manager that answered.
		return o.envelope(t, parts, rpmPart, packageData(managerRPM, false, ""), nil), nil

	case dpkgOut.ExitCode == 0:
		installed, version := dpkgParse(dpkgOut.Stdout)
		if installed {
			return o.envelope(t, parts, dpkgPart, packageData(managerDpkg, true, version), nil), nil
		}
		// dpkg knows the package (a removed one that kept its configuration)
		// but it is not installed. The command itself succeeded, so the exit
		// status stays dpkg-query's own 0 rather than a made-up non-zero one;
		// the failed Status is carried by an Error that says why.
		notInstalled := dpkgPart
		notInstalled.Status = result.StatusFailed
		notInstalled.Error = result.ExecutionError("package_not_installed",
			"dpkg-query reports the package as known but not installed")
		return o.envelope(t, parts, notInstalled, packageData(managerDpkg, false, ""), nil), nil

	default:
		return o.envelope(t, parts, dpkgPart, packageData(managerDpkg, false, ""), nil), nil
	}
}

// envelope assembles the Envelope from every attempted Part. decisive is the
// Part the Status, ExitCode and Error are read from; the duration and the
// truncation flag cover all of them. errOverride replaces decisive's Error,
// for the one outcome — no manager at all — that no single command reports.
func (o Package) envelope(t vo.Target, parts []result.Part, decisive result.Part, data map[string]string, errOverride *result.Error) result.Envelope {
	env := result.Envelope{
		Operation: o.Name(),
		Host:      t.String(),
		Status:    decisive.Status,
		ExitCode:  decisive.ExitCode,
		Data:      data,
		Error:     decisive.Error,
		Parts:     parts,
	}
	if errOverride != nil {
		env.Error = errOverride
	}
	for _, part := range parts {
		env.DurationMs += part.DurationMs
		env.Truncated = env.Truncated || part.Truncated
	}
	return env
}

// sshFailed reports whether the run could not reach an answer from the host at
// all: the executor returned an error, or ssh itself exited 255.
func sshFailed(out ssh.Output, err error) bool {
	return err != nil || out.ExitCode == exitSSHFailure
}

func packageData(manager string, installed bool, version string) map[string]string {
	data := map[string]string{"manager": manager, "installed": "false"}
	if installed {
		data["installed"] = "true"
	}
	if version != "" && len(version) <= packageVersionMaxLen {
		data["version"] = version
	}
	return data
}

// soleLine returns the one non-blank line of s, trimmed, or "" when s has none
// or more than one. rpm -q prints a line per installed version, in the order of
// its database, so a package with several versions (a kernel, typically) has no
// single version to report and none is picked.
func soleLine(s string) string {
	var found string
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if found != "" {
			return ""
		}
		found = line
	}
	return found
}

// dpkgParse reads dpkg-query -s output, a sequence of RFC 822 style records.
// A package is installed when a record's Status field names "installed" as its
// third word ("install ok installed", "hold ok installed"); the Version field
// of that same record is the installed version.
//
// Fields are recognised only at the start of a line. A continuation line of a
// multi-line field such as Description begins with a space, so text a package
// author put there cannot pose as the package's own status.
func dpkgParse(stdout string) (installed bool, version string) {
	for record := range strings.SplitSeq(stdout, "\n\n") {
		var status, ver string
		for line := range strings.SplitSeq(record, "\n") {
			line = strings.TrimSuffix(line, "\r")
			if rest, ok := strings.CutPrefix(line, "Status: "); ok && status == "" {
				status = rest
			} else if rest, ok := strings.CutPrefix(line, "Version: "); ok && ver == "" {
				ver = strings.TrimSpace(rest)
			}
		}
		if fields := strings.Fields(status); len(fields) == 3 && fields[2] == "installed" {
			return true, ver
		}
	}
	return false, ""
}
