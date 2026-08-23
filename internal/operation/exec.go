package operation

import (
	"errors"
	"strings"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
)

// buildPart turns one ssh.Executor.Execute call's outcome — the Output it
// produced and the error it returned, if any — into a result.Part named
// name. It is the one place an M3 operation's Execute translates ssh.Output
// into the Status enum, so system/cpu/.../inspect do not each restate the
// rule for when a run counts as ok, failed, timeout or truncated.
//
// execErr is nil for everything except an execution failure: a non-zero
// remote exit code is not an error (Execute says so explicitly), so it is
// read from out.ExitCode and reported as StatusFailed without an Error
// attached. execErr is only ever non-nil when Execute could not produce an
// exit status at all — a timeout (ssh.TimedOut) or an outright failure to
// run the command (e.g. the ssh connection itself failing) — and in both
// cases ExitCode is reported as -1, matching Output's own doctrine for "no
// status reached".
func buildPart(name string, out ssh.Output, execErr error) result.Part {
	part := result.Part{
		Name:       name,
		DurationMs: out.Duration.Milliseconds(),
		Truncated:  out.Truncated,
		Stdout:     out.Stdout,
		Stderr:     out.Stderr,
		ExitCode:   out.ExitCode,
	}

	switch {
	case execErr != nil:
		var rerr *result.Error
		if !errors.As(execErr, &rerr) {
			rerr = result.ExecutionError("ssh_failed", execErr.Error())
		}
		part.Error = rerr
		part.ExitCode = -1
		if ssh.TimedOut(execErr) {
			part.Status = result.StatusTimeout
		} else {
			part.Status = result.StatusFailed
		}
	case out.Truncated:
		part.Status = result.StatusTruncated
	case out.ExitCode != 0:
		part.Status = result.StatusFailed
	default:
		part.Status = result.StatusOK
	}

	return part
}

// statusPriority orders Status for aggregate: the worst outcome among a set
// of Parts is the one an Envelope reports, and "worst" is timeout, then
// failed, then truncated, then ok. A Part whose Status is not one of these
// four (which cannot happen through buildPart, but this stays total rather
// than panicking on a future addition) sorts as ok.
var statusPriority = map[result.Status]int{
	result.StatusOK:        0,
	result.StatusTruncated: 1,
	result.StatusFailed:    2,
	result.StatusTimeout:   3,
}

// aggregate folds parts into the Envelope-level Status, ExitCode, DurationMs
// and Truncated an operation built from more than one Command reports.
//
//   - Status is the worst Status among parts, worst meaning timeout > failed
//     > truncated > ok; an empty parts reports StatusOK, since an operation
//     that produced no parts at all has nothing to have failed.
//   - ExitCode is 0 if every part's ExitCode is 0, otherwise the first
//     non-zero ExitCode in part order.
//   - DurationMs is the sum of every part's DurationMs.
//   - Truncated is true if any part's Truncated is true.
func aggregate(parts []result.Part) (status result.Status, exitCode int, durationMs int64, truncated bool) {
	status = result.StatusOK
	best := statusPriority[result.StatusOK]

	for _, p := range parts {
		durationMs += p.DurationMs
		if p.Truncated {
			truncated = true
		}
		if pr, ok := statusPriority[p.Status]; ok && pr > best {
			best, status = pr, p.Status
		}
		if exitCode == 0 && p.ExitCode != 0 {
			exitCode = p.ExitCode
		}
	}

	return status, exitCode, durationMs, truncated
}

// knownUnitSuffixes are the systemd unit types Focal recognizes. A unit name
// already ending in one of these is left alone; anything else is assumed to
// name a service and gets ".service" appended. This mirrors systemctl's own
// convention for a bare unit name.
var knownUnitSuffixes = []string{
	".service", ".socket", ".timer", ".target", ".mount", ".path",
	".slice", ".scope", ".device", ".swap", ".automount",
}

// normalizeUnitName is Focal's single unit-name normalization point: every
// factory and operation that turns a vo.ServiceName into a systemd unit
// argument (LogsCommand, service.go's ServiceStatusCommand caller) calls this
// instead of appending ".service" itself, so the rule lives in exactly one
// place. A name already carrying a known unit-type suffix is returned
// unchanged; anything else gets ".service" appended.
func normalizeUnitName(name string) string {
	for _, suffix := range knownUnitSuffixes {
		if strings.HasSuffix(name, suffix) {
			return name
		}
	}
	return name + ".service"
}
