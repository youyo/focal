package operation

import (
	"regexp"
	"slices"
	"strings"
)

// journalEntryStart matches the first line of an entry in journalctl's
// short-iso output: the timestamp, then a space, at the very start of the line.
// The offset is accepted with or without a colon; short-iso prints +0900.
//
// Nothing else about a line is read. A line that does not match is a
// continuation of the entry above it, whatever its indentation, so the rule
// does not depend on how far journalctl indents a multi-line message.
var journalEntryStart = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{2}:?\d{2} `)

// journalSplit cuts journalctl's short-iso output into the lines that come
// before the first entry and the entries themselves, each a first line
// followed by its continuation lines, in the order journalctl printed them.
// This is the one place the entry rule is applied: reordering here and
// filtering and counting in logs_filter.go read entries through it, so the
// two cannot disagree about where an entry ends.
//
// Output with no entry at all comes back as preamble only.
func journalSplit(stdout string) (preamble []string, entries [][]string) {
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	first := slices.IndexFunc(lines, journalEntryStart.MatchString)
	if first < 0 {
		return lines, nil
	}
	for _, line := range lines[first:] {
		if journalEntryStart.MatchString(line) {
			entries = append(entries, []string{line})
			continue
		}
		entries[len(entries)-1] = append(entries[len(entries)-1], line)
	}
	return lines[:first], entries
}

// journalEntryTimestamp returns the timestamp an entry's first line starts
// with, without the space after it.
func journalEntryTimestamp(entry []string) string {
	return strings.TrimSuffix(journalEntryStart.FindString(entry[0]), " ")
}

// journalOldestFirst turns the newest-first answer of journalctl --reverse
// into the order the entries were written, moving whole entries — a first line
// and its continuation lines — rather than single lines (ADR 0009).
//
// Lines before the first entry are not part of any entry and stay at the top.
// Output with no entry at all, such as "-- No entries --" or an error message,
// comes back unchanged.
//
// When the output was truncated, its last entry is the oldest one and the
// only one the cut can have gone through, so it is dropped before the rest
// are reordered. What remains then ends with a newline, since every line in
// it was followed by another line in the original.
func journalOldestFirst(stdout string, truncated bool) string {
	trailingNewline := strings.HasSuffix(stdout, "\n")
	preamble, entries := journalSplit(stdout)
	if entries == nil {
		return stdout
	}
	if truncated {
		entries = entries[:len(entries)-1]
		trailingNewline = true
	}
	slices.Reverse(entries)

	out := slices.Clone(preamble)
	for _, e := range entries {
		out = append(out, e...)
	}
	if len(out) == 0 {
		return ""
	}
	joined := strings.Join(out, "\n")
	if trailingNewline {
		joined += "\n"
	}
	return joined
}
