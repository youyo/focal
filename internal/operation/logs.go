package operation

import (
	"context"
	"fmt"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// This file holds the logs operation and the pieces both journal readers —
// logs here and kernel in kernel.go — share: the defaults they fall back to,
// the ceiling rules they resolve a request against, and the Envelope shape a
// single-Command operation reports through. Everything shared carries a
// journal prefix, since the two files are the only members of that pair.

// The spans and line counts a journal read uses when the caller named none.
// journalctl's own default of ten lines is never relied on: -n is always
// passed, so a host that configured a different default cannot change what a
// request returns.
var (
	logsDefaultSince    = journalMustDuration("30m")
	journalDefaultLines = journalMustLineLimit("200")
)

// journalMustDuration and journalMustLineLimit parse this package's own
// default literals. Their arguments are string literals in this file and in
// kernel.go, so a rejection is a programming error caught on the first run
// rather than anything a caller can cause.

func journalMustDuration(v string) vo.Duration {
	d, err := vo.ParseDuration(v)
	if err != nil {
		panic("operation: invalid default duration " + v + ": " + err.Error())
	}
	return d
}

func journalMustLineLimit(v string) vo.LineLimit {
	l, err := vo.ParseLineLimit(v)
	if err != nil {
		panic("operation: invalid default line limit " + v + ": " + err.Error())
	}
	return l
}

// journalSince resolves the span a journal read looks back over.
//
// An unspecified request — the zero Duration, which reports 0 seconds — takes
// def, narrowed to ceiling when the administrator allows less than the default:
// a caller who asked for nothing is given whatever is permitted rather than an
// error about a value they never chose. A request that names a span above
// ceiling is refused instead of rounded down, because a silent narrowing would
// answer a question the caller did not ask and label it as the one they did.
//
// Both comparisons read Seconds() rather than the rendered form: "30d" sorts
// before "7d" as text, so a string comparison would let the wider span through.
// A zero ceiling means the config file set no max_since, leaving
// vo.ParseDuration's own 30d bound as the only cap.
func journalSince(requested, def, ceiling vo.Duration) (vo.Duration, *result.Error) {
	capped := ceiling.Seconds() > 0
	if requested.Seconds() == 0 {
		if capped && def.Seconds() > ceiling.Seconds() {
			return ceiling, nil
		}
		return def, nil
	}
	if capped && requested.Seconds() > ceiling.Seconds() {
		return vo.Duration{}, result.ValidationError(
			"since_above_max",
			fmt.Sprintf("since %s reaches further back than the configured max_since of %s", requested, ceiling),
			"since",
			[]string{"at most " + ceiling.String()},
		)
	}
	return requested, nil
}

// journalLines resolves how many lines a journal read returns, by the same
// rules journalSince applies to the span: an unspecified request takes the
// default narrowed to the ceiling, and a request above the ceiling is refused
// rather than reduced. The comparison reads Int(), since "1000" sorts before
// "999" as text.
func journalLines(requested, def, ceiling vo.LineLimit) (vo.LineLimit, *result.Error) {
	capped := ceiling.Int() > 0
	if requested.Int() == 0 {
		if capped && def.Int() > ceiling.Int() {
			return ceiling, nil
		}
		return def, nil
	}
	if capped && requested.Int() > ceiling.Int() {
		return vo.LineLimit{}, result.ValidationError(
			"lines_above_max",
			fmt.Sprintf("lines %s is above the configured max_lines of %s", requested, ceiling),
			"lines",
			[]string{"at most " + ceiling.String()},
		)
	}
	return requested, nil
}

// journalEnvelope folds the one Part a journal read produces into an Envelope.
// buildPart owns the rule for which Status an Output means, so a single-Command
// operation reads it from there rather than restating it; there is nothing to
// aggregate, so Parts stays empty and the fields carry the answer directly.
//
// Both readers pass --reverse, so stdout arrives newest first; it is put back
// oldest first here, the one place both of them go through. Stderr is left as
// the remote side wrote it.
func journalEnvelope(name string, t vo.Target, out ssh.Output, execErr error) result.Envelope {
	out.Stdout = journalOldestFirst(out.Stdout, out.Truncated)
	part := buildPart(name, out, execErr)
	return result.Envelope{
		Operation:  name,
		Host:       t.String(),
		Status:     part.Status,
		ExitCode:   part.ExitCode,
		DurationMs: part.DurationMs,
		Truncated:  part.Truncated,
		Stdout:     part.Stdout,
		Stderr:     part.Stderr,
		Error:      part.Error,
	}
}

// Logs reads one systemd unit's journal. It holds only the typed parameters a
// request named — never a Command — so the argv is built in internal/ssh at
// execution time and nowhere else.
//
// The window is held in one of two forms, never both. A window whose bounds
// are both relative keeps them as spans on the remote clock, in since and
// until; a window with an absolute bound has both bounds as instants on
// Focal's clock, in sinceAt and untilAt, and since and until stay zero
// (ADR 0011). A zero until or untilAt means the window is open at its end.
// grep, grepRegex and countBy are applied by Focal to what comes back and are
// never part of the argv.
type Logs struct {
	service   vo.ServiceName
	since     vo.Duration
	lines     vo.LineLimit
	until     vo.Duration
	sinceAt   vo.Timestamp
	untilAt   vo.Timestamp
	grep      vo.LogPattern
	grepRegex vo.LogRegex
	countBy   vo.CountBy
}

// NewLogs builds a logs request. service is required; since and lines may be
// zero values, meaning the caller named neither and the defaults apply.
//
// maxSince and maxLines are the administrator's ceilings, passed in by the
// caller that read the config file: this package takes them as arguments so
// that the operation stays a plain typed request and internal/config keeps a
// single reader. Either may be a zero value, meaning no ceiling was set.
//
// NewLogs builds the request every caller made before logs could filter: a
// relative span and a line count. The registry's logs row builds the rest
// through newLogs, with the same rules for these three.
func NewLogs(service vo.ServiceName, since vo.Duration, lines vo.LineLimit, maxSince vo.Duration, maxLines vo.LineLimit) (Logs, *result.Error) {
	return newLogs(service, logsQuery{since: journalBound{span: since}}, lines, maxSince, maxLines, time.Time{})
}

// Name returns "logs".
func (o Logs) Name() string { return "logs" }

// Execute runs journalctl for the unit. The unit name is normalized through
// exec.go's normalizeUnitName — Focal's only normalization point — and parsed
// again on the way out of it, so the name that reaches the factory is still a
// vo.ServiceName rather than a string this file assembled.
//
// A request that uses none of what ADR 0011 added is answered exactly as
// before, through journalEnvelope. Any other request is answered through
// logsFilteredEnvelope, which also reports how much of the window was read.
func (o Logs) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	unit, err := vo.ParseServiceName(normalizeUnitName(o.service.String()))
	if err != nil {
		return result.Envelope{}, err
	}
	limit := o.lines
	if o.scans() {
		limit = logsScanLimit
	}
	cmd, err := o.command(p, unit, limit)
	if err != nil {
		return result.Envelope{}, err
	}
	out, execErr := ex.Execute(ctx, t, cmd)
	if !o.extended() {
		return journalEnvelope(o.Name(), t, out, execErr), nil
	}
	return o.filteredEnvelope(t, out, execErr, limit), nil
}

// command picks the one factory whose argv has the shape of this window.
func (o Logs) command(p policy.Policy, unit vo.ServiceName, limit vo.LineLimit) (ssh.Command, *result.Error) {
	switch {
	case o.sinceAt.Unix() > 0 && o.untilAt.Unix() > 0:
		return ssh.LogsEpochWindowCommand(p, unit, o.sinceAt, o.untilAt, limit)
	case o.sinceAt.Unix() > 0:
		return ssh.LogsSinceEpochCommand(p, unit, o.sinceAt, limit)
	case o.until.Seconds() > 0:
		return ssh.LogsRelativeWindowCommand(p, unit, o.since, o.until, limit)
	default:
		return ssh.LogsCommand(p, unit, o.since, limit)
	}
}

// scans reports whether the request filters or counts entries, which makes
// journalctl read up to logsScanLimit entries instead of the caller's lines.
func (o Logs) scans() bool {
	return o.grep.String() != "" || o.grepRegex.String() != "" || o.countBy.String() != ""
}

// extended reports whether the request uses anything ADR 0011 added: a
// filter, a count, an until, or an absolute since.
func (o Logs) extended() bool {
	return o.scans() || o.until.Seconds() > 0 || o.sinceAt.Unix() > 0
}
