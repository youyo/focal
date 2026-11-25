package operation

import (
	"slices"
	"strconv"
	"strings"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// This file is the half of a logs request that runs on Focal's side of the
// connection: matching entries against grep or grep-regex, counting them into
// buckets, and reporting how much of the window the answer covers (ADR 0011).
// It reads entries through journalSplit, the same rule journalOldestFirst
// uses, so an entry here is an entry there.

// filteredEnvelope answers a request that uses anything ADR 0011 added.
//
// The entries arrive newest first. When the output was truncated the last of
// them — the oldest, the only one the cut can have gone through — is dropped
// before anything is counted, as journalOldestFirst drops it. Lines that are
// not part of an entry, such as "-- No entries --", are neither counted nor
// matched.
//
// What stdout holds depends on what was asked: nothing for a count, which is
// reported in Items; the newest lines matching entries, oldest first, for a
// filter; and for a request that only named a window, exactly what a plain
// request returns. Data is always there, so a caller can tell an answer that
// covers the whole window from one that stopped short of it.
func (o Logs) filteredEnvelope(t vo.Target, out ssh.Output, execErr error, limit vo.LineLimit) result.Envelope {
	_, entries := journalSplit(out.Stdout)
	returned := len(entries)
	if out.Truncated && returned > 0 {
		entries = entries[:returned-1]
	}

	matched := entries
	if o.filters() {
		matched = slices.DeleteFunc(slices.Clone(entries), func(e []string) bool {
			return !o.matches(strings.Join(e, "\n"))
		})
	}

	complete := !out.Truncated && execErr == nil && out.ExitCode == 0 && returned < limit.Int()
	data := map[string]string{
		"complete":        strconv.FormatBool(complete),
		"scanned_entries": strconv.Itoa(len(entries)),
		"matched_entries": strconv.Itoa(len(matched)),
	}
	if len(entries) > 0 {
		data["covered_from"] = journalEntryTimestamp(entries[len(entries)-1])
		data["covered_to"] = journalEntryTimestamp(entries[0])
	}

	var items []map[string]string
	switch {
	case o.countBy.String() != "":
		out.Stdout = ""
		items = logsCount(matched, o.countBy)
	case o.filters():
		out.Stdout = logsNewestOldestFirst(matched, o.lines.Int())
	default:
		out.Stdout = journalOldestFirst(out.Stdout, out.Truncated)
	}

	part := buildPart(o.Name(), out, execErr)
	return result.Envelope{
		Operation:  o.Name(),
		Host:       t.String(),
		Status:     part.Status,
		ExitCode:   part.ExitCode,
		DurationMs: part.DurationMs,
		Truncated:  part.Truncated,
		Stdout:     part.Stdout,
		Stderr:     part.Stderr,
		Data:       data,
		Items:      items,
		Error:      part.Error,
	}
}

// filters reports whether the request narrows entries by text.
func (o Logs) filters() bool { return o.grep.String() != "" || o.grepRegex.String() != "" }

// matches applies whichever of grep and grep-regex the request named to the
// full text of one entry: its first line, timestamp and identifier included,
// and its continuation lines, joined by newlines.
func (o Logs) matches(entry string) bool {
	if o.grep.String() != "" {
		return o.grep.Matches(entry)
	}
	return o.grepRegex.Matches(entry)
}

// logsNewestOldestFirst keeps the newest n of entries, which arrive newest
// first, and returns them in the order they were written, each entry's lines
// kept together, ending in a newline. No entries is the empty string.
func logsNewestOldestFirst(entries [][]string, n int) string {
	kept := slices.Clone(entries[:min(n, len(entries))])
	slices.Reverse(kept)
	var b strings.Builder
	for _, e := range kept {
		for _, line := range e {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// logsCount counts entries, which arrive newest first, into buckets of the
// hour or the day their timestamp falls in, in the offset the remote host
// printed. The rows come back oldest first and only for buckets that hold an
// entry. A bucket is the timestamp cut down to the hour (2026-11-25T13+0900)
// or the day (2026-11-25+0900), with the offset always written without a
// colon, so +0900 and +09:00 count as the same zone.
func logsCount(entries [][]string, by vo.CountBy) []map[string]string {
	var order []string
	counts := map[string]int{}
	for i := len(entries) - 1; i >= 0; i-- {
		bucket := logsBucket(journalEntryTimestamp(entries[i]), by)
		if _, seen := counts[bucket]; !seen {
			order = append(order, bucket)
		}
		counts[bucket]++
	}
	if len(order) == 0 {
		return nil
	}
	items := make([]map[string]string, 0, len(order))
	for _, bucket := range order {
		items = append(items, map[string]string{"bucket": bucket, "count": strconv.Itoa(counts[bucket])})
	}
	return items
}

// logsBucket cuts a short-iso timestamp — YYYY-MM-DDTHH:MM:SS and an offset,
// as journalEntryStart matched it — down to its bucket.
func logsBucket(ts string, by vo.CountBy) string {
	const offsetAt = len("2006-01-02T15:04:05")
	offset := strings.ReplaceAll(ts[offsetAt:], ":", "")
	if by.IsDay() {
		return ts[:len("2006-01-02")] + offset
	}
	return ts[:len("2006-01-02T15")] + offset
}
