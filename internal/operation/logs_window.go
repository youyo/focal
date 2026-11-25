package operation

import (
	"fmt"
	"strings"
	"time"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// This file resolves the window and the filter a logs request names into the
// typed fields of Logs (ADR 0011). Nothing here reads the time: the instant a
// request is resolved against is handed in by the registry, which reads its
// clock once per Build.

// logsScanLimit is how many entries a request that filters or counts asks
// journalctl for. Such a request is answered from what Focal reads back, so
// the caller's lines cannot be what bounds the read; this constant is, and an
// answer that reaches it is reported as incomplete.
var logsScanLimit = journalMustLineLimit("100000")

// journalBound is one end of a window as a caller wrote it: a span back from
// now, an absolute instant, or — both zero — not named at all.
type journalBound struct {
	span vo.Duration
	at   vo.Timestamp
}

func (b journalBound) named() bool    { return b.span.Seconds() > 0 || b.absolute() }
func (b journalBound) absolute() bool { return b.at.Unix() > 0 }

// parseJournalBound reads one raw bound. The empty string is "not named". A
// value with a "T" in it can only be meant as an instant, since no span has
// one, so it goes to vo.ParseTimestamp and is refused there if it is not one;
// anything else goes to vo.ParseDuration.
func parseJournalBound(v string) (journalBound, *result.Error) {
	switch {
	case v == "":
		return journalBound{}, nil
	case strings.Contains(v, "T"):
		at, err := vo.ParseTimestamp(v)
		return journalBound{at: at}, err
	default:
		span, err := vo.ParseDuration(v)
		return journalBound{span: span}, err
	}
}

// logsQuery is what a logs request names beyond its service and line count.
type logsQuery struct {
	since     journalBound
	until     journalBound
	grep      vo.LogPattern
	grepRegex vo.LogRegex
	countBy   vo.CountBy
}

// newLogs resolves a request against the ceilings and against now, the
// instant on Focal's clock this request is answered at. now is read only when
// a bound is absolute; a request whose bounds are both relative is resolved
// exactly as NewLogs always resolved it, and keeps the remote clock.
func newLogs(service vo.ServiceName, q logsQuery, lines vo.LineLimit, maxSince vo.Duration, maxLines vo.LineLimit, now time.Time) (Logs, *result.Error) {
	if service.String() == "" {
		return Logs{}, result.ValidationError(
			"missing_service_name",
			"logs requires a service name",
			"service",
			[]string{"a systemd unit name, for example nginx or docker.socket"},
		)
	}
	if q.grep.String() != "" && q.grepRegex.String() != "" {
		return Logs{}, result.ValidationError(
			"conflicting_arguments",
			"grep and grep-regex cannot be given together",
			"grep-regex",
			[]string{"either grep (fixed text) or grep-regex (an RE2 expression), not both"},
		)
	}
	resolvedLines, err := journalLines(lines, journalDefaultLines, maxLines)
	if err != nil {
		return Logs{}, err
	}
	logs := Logs{
		service:   service,
		lines:     resolvedLines,
		grep:      q.grep,
		grepRegex: q.grepRegex,
		countBy:   q.countBy,
	}
	if q.since.absolute() || q.until.absolute() {
		logs.sinceAt, logs.untilAt, err = logsAbsoluteWindow(q.since, q.until, maxSince, now)
	} else {
		logs.since, logs.until, err = logsRelativeWindow(q.since.span, q.until.span, maxSince)
	}
	if err != nil {
		return Logs{}, err
	}
	return logs, nil
}

// logsRelativeWindow resolves a window whose bounds are both spans on the
// remote clock. A since that was named, or none at all, follows journalSince.
// A since left out when until was named is the default span before until —
// until plus 30m back from now — and is held to the ceiling as if the caller
// had named it: narrowing it would shorten a window whose end the caller
// chose and answer a question they did not ask.
func logsRelativeWindow(since, until, ceiling vo.Duration) (vo.Duration, vo.Duration, *result.Error) {
	if until.Seconds() == 0 {
		resolved, err := journalSince(since, logsDefaultSince, ceiling)
		return resolved, vo.Duration{}, err
	}
	if since.Seconds() == 0 {
		derived := until.Seconds() + logsDefaultSince.Seconds()
		if ceiling.Seconds() > 0 && derived > ceiling.Seconds() {
			return vo.Duration{}, vo.Duration{}, logsDerivedSinceAboveMax(until.String(), ceiling)
		}
		resolved, err := vo.DurationOfSeconds(derived)
		return resolved, until, err
	}
	resolved, err := journalSince(since, logsDefaultSince, ceiling)
	if err != nil {
		return vo.Duration{}, vo.Duration{}, err
	}
	if until.Seconds() > resolved.Seconds() {
		return vo.Duration{}, vo.Duration{}, logsUntilBeforeSince(until.String()+" ago", resolved.String()+" ago")
	}
	return resolved, until, nil
}

// logsAbsoluteWindow resolves a window with at least one absolute bound. Both
// bounds become instants on Focal's clock: a span is subtracted from now, and
// a since left out is the default span before until or now, whichever is
// earlier, so it is never in the future even when until is. The checks are
// then made on instants, to the second: since may not be after now, until may not
// be before since (it may be after now), and since may not reach further back
// than the ceiling allows — or 30d, vo.ParseDuration's own bound, when no
// ceiling is configured.
func logsAbsoluteWindow(since, until journalBound, ceiling vo.Duration, now time.Time) (vo.Timestamp, vo.Timestamp, *result.Error) {
	nowUnix := now.Unix()

	var untilAt vo.Timestamp
	var err *result.Error
	switch {
	case until.absolute():
		untilAt = until.at
	case until.named():
		if untilAt, err = vo.TimestampFromUnix(nowUnix - until.span.Seconds()); err != nil {
			return vo.Timestamp{}, vo.Timestamp{}, err
		}
	}

	var sinceAt vo.Timestamp
	derived := false
	switch {
	case since.absolute():
		sinceAt = since.at
	case since.named():
		span, spanErr := journalSince(since.span, logsDefaultSince, ceiling)
		if spanErr != nil {
			return vo.Timestamp{}, vo.Timestamp{}, spanErr
		}
		if sinceAt, err = vo.TimestampFromUnix(nowUnix - span.Seconds()); err != nil {
			return vo.Timestamp{}, vo.Timestamp{}, err
		}
	default:
		// Only an absolute until brings a request here without a since. The
		// default span ends at until or at now, whichever is earlier: an
		// until in the future still reads the latest 30m rather than a
		// window that has not begun.
		derived = true
		if sinceAt, err = vo.TimestampFromUnix(min(untilAt.Unix(), nowUnix) - logsDefaultSince.Seconds()); err != nil {
			return vo.Timestamp{}, vo.Timestamp{}, err
		}
	}

	if sinceAt.Unix() > nowUnix {
		return vo.Timestamp{}, vo.Timestamp{}, result.ValidationError(
			"since_in_future",
			fmt.Sprintf("since %s is later than now", sinceAt),
			"since",
			[]string{"an instant no later than now"},
		)
	}
	if untilAt.Unix() > 0 && untilAt.Unix() < sinceAt.Unix() {
		return vo.Timestamp{}, vo.Timestamp{}, logsUntilBeforeSince(untilAt.String(), sinceAt.String())
	}
	limit := ceiling
	if limit.Seconds() == 0 {
		limit = logsUncappedSince
	}
	if sinceAt.Unix() < nowUnix-limit.Seconds() {
		if derived {
			return vo.Timestamp{}, vo.Timestamp{}, logsDerivedSinceAboveMax(untilAt.String(), limit)
		}
		return vo.Timestamp{}, vo.Timestamp{}, result.ValidationError(
			"since_above_max",
			fmt.Sprintf("since %s reaches further back than the configured max_since of %s", sinceAt, limit),
			"since",
			[]string{"at most " + limit.String()},
		)
	}
	return sinceAt, untilAt, nil
}

// logsUncappedSince is the bound an absolute since is held to when no
// max_since is configured: the same 30d vo.ParseDuration holds a span to.
var logsUncappedSince = journalMustDuration("30d")

func logsUntilBeforeSince(until, since string) *result.Error {
	return result.ValidationError(
		"until_before_since",
		fmt.Sprintf("until %s is earlier than since %s", until, since),
		"until",
		[]string{"an until no earlier than since"},
	)
}

// logsDerivedSinceAboveMax is since_above_max for a since the caller did not
// name: it says where the since came from, so the caller can see which of
// their values to change.
func logsDerivedSinceAboveMax(until string, ceiling vo.Duration) *result.Error {
	return result.ValidationError(
		"since_above_max",
		fmt.Sprintf("since, taken as the default %s before until %s (or before now, if that is earlier), reaches further back than the configured max_since of %s",
			logsDefaultSince, until, ceiling),
		"since",
		[]string{"at most " + ceiling.String(), "or name since explicitly"},
	)
}
