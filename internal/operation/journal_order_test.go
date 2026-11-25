package operation

import "testing"

// The entries in the fixtures below are shaped like journalctl
// --output=short-iso --reverse on systemd 252: newest entry first, the offset
// spelled +0900, and the continuation lines of a multi-line message indented
// to the column where the message text starts. The "-- Journal begins at" and
// "-- Boot" lines are synthetic: systemd 252 printed neither in the e2e
// container, and they stand for any line that is not an entry, before the
// first entry or between two.

func TestJournalOldestFirst(t *testing.T) {
	for _, tc := range []struct {
		name      string
		in        string
		truncated bool
		want      string
	}{
		{
			name: "empty output stays empty",
			in:   "",
			want: "",
		},
		{
			name: "a single entry without a trailing newline is returned byte for byte",
			in:   "2026-08-23T09:00:00+0900 web01 nginx[1]: started",
			want: "2026-08-23T09:00:00+0900 web01 nginx[1]: started",
		},
		{
			name: "entries come back oldest first",
			in: "2026-11-25T13:04:07+0900 web01 nginx[1]: third\n" +
				"2026-11-25T13:04:06+0900 web01 nginx[1]: second\n" +
				"2026-11-25T13:04:05+0900 web01 nginx[1]: first\n",
			want: "2026-11-25T13:04:05+0900 web01 nginx[1]: first\n" +
				"2026-11-25T13:04:06+0900 web01 nginx[1]: second\n" +
				"2026-11-25T13:04:07+0900 web01 nginx[1]: third\n",
		},
		{
			name: "the offset with a colon is a timestamp too",
			in: "2026-11-25T13:04:06+09:00 web01 nginx[1]: second\n" +
				"2026-11-25T13:04:05+09:00 web01 nginx[1]: first\n",
			want: "2026-11-25T13:04:05+09:00 web01 nginx[1]: first\n" +
				"2026-11-25T13:04:06+09:00 web01 nginx[1]: second\n",
		},
		{
			name: "a negative offset is a timestamp too",
			in: "2026-11-25T13:04:06-0500 web01 nginx[1]: second\n" +
				"2026-11-25T13:04:05-0500 web01 nginx[1]: first\n",
			want: "2026-11-25T13:04:05-0500 web01 nginx[1]: first\n" +
				"2026-11-25T13:04:06-0500 web01 nginx[1]: second\n",
		},
		{
			name: "continuation lines stay under the line they belong to",
			in: "2026-11-25T13:04:07+0900 web01 app[2]: after\n" +
				"2026-11-25T13:04:06+0900 web01 app[2]: multi first\n" +
				"                                       multi second\n" +
				"                                       multi third\n" +
				"2026-11-25T13:04:05+0900 web01 app[2]: before\n",
			want: "2026-11-25T13:04:05+0900 web01 app[2]: before\n" +
				"2026-11-25T13:04:06+0900 web01 app[2]: multi first\n" +
				"                                       multi second\n" +
				"                                       multi third\n" +
				"2026-11-25T13:04:07+0900 web01 app[2]: after\n",
		},
		{
			name: "a continuation line does not need to be indented",
			in: "2026-11-25T13:04:06+0900 web01 app[2]: multi first\n" +
				"multi second\n" +
				"2026-11-25T13:04:05+0900 web01 app[2]: before\n",
			want: "2026-11-25T13:04:05+0900 web01 app[2]: before\n" +
				"2026-11-25T13:04:06+0900 web01 app[2]: multi first\n" +
				"multi second\n",
		},
		{
			name: "a line between entries that is not a timestamp travels with the entry above it",
			in: "2026-11-25T13:04:06+0900 web01 app[2]: newer\n" +
				"-- Boot 0123456789abcdef0123456789abcdef --\n" +
				"2026-11-25T13:04:05+0900 web01 app[2]: older\n",
			want: "2026-11-25T13:04:05+0900 web01 app[2]: older\n" +
				"2026-11-25T13:04:06+0900 web01 app[2]: newer\n" +
				"-- Boot 0123456789abcdef0123456789abcdef --\n",
		},
		{
			name: "no entries is returned as journalctl wrote it",
			in:   "-- No entries --\n",
			want: "-- No entries --\n",
		},
		{
			name: "output without a single timestamp is returned as it is",
			in:   "Failed to add match 'x': Invalid argument\nsecond line\n",
			want: "Failed to add match 'x': Invalid argument\nsecond line\n",
		},
		{
			name: "lines before the first entry stay at the top",
			in: "-- Journal begins at Tue 2026-11-24 09:00:00 JST, ends at Wed 2026-11-25 13:04:06 JST. --\n" +
				"2026-11-25T13:04:06+0900 web01 nginx[1]: second\n" +
				"2026-11-25T13:04:05+0900 web01 nginx[1]: first\n",
			want: "-- Journal begins at Tue 2026-11-24 09:00:00 JST, ends at Wed 2026-11-25 13:04:06 JST. --\n" +
				"2026-11-25T13:04:05+0900 web01 nginx[1]: first\n" +
				"2026-11-25T13:04:06+0900 web01 nginx[1]: second\n",
		},
		{
			name: "a line that only resembles a timestamp is a continuation line",
			in: "2026-11-25T13:04:06+0900 web01 nginx[1]: second\n" +
				"  2026-11-25T13:04:05+0900 indented\n" +
				"2026-11-25T13:04:05 no offset\n" +
				"2026-11-25T13:04:05+0900 web01 nginx[1]: first\n",
			want: "2026-11-25T13:04:05+0900 web01 nginx[1]: first\n" +
				"2026-11-25T13:04:06+0900 web01 nginx[1]: second\n" +
				"  2026-11-25T13:04:05+0900 indented\n" +
				"2026-11-25T13:04:05 no offset\n",
		},
		{
			name:      "truncated output drops the oldest entry, which may have been cut",
			truncated: true,
			in: "2026-11-25T13:04:07+0900 web01 nginx[1]: third\n" +
				"2026-11-25T13:04:06+0900 web01 nginx[1]: second\n" +
				"2026-11-25T13:04:05+0900 web01 nginx[1]: fir",
			want: "2026-11-25T13:04:06+0900 web01 nginx[1]: second\n" +
				"2026-11-25T13:04:07+0900 web01 nginx[1]: third\n",
		},
		{
			name:      "truncated output drops the oldest entry with its continuation lines",
			truncated: true,
			in: "2026-11-25T13:04:07+0900 web01 app[2]: newest\n" +
				"2026-11-25T13:04:06+0900 web01 app[2]: multi first\n" +
				"                                       multi sec",
			want: "2026-11-25T13:04:07+0900 web01 app[2]: newest\n",
		},
		{
			name:      "truncated output that ends on a whole line still drops the oldest entry",
			truncated: true,
			in: "2026-11-25T13:04:07+0900 web01 nginx[1]: second\n" +
				"2026-11-25T13:04:05+0900 web01 nginx[1]: first\n",
			want: "2026-11-25T13:04:07+0900 web01 nginx[1]: second\n",
		},
		{
			name:      "truncated output with one entry leaves nothing",
			truncated: true,
			in:        "2026-11-25T13:04:05+0900 web01 nginx[1]: only, and cut",
			want:      "",
		},
		{
			name:      "truncated output keeps the lines before the first entry",
			truncated: true,
			in: "-- Journal begins at Tue 2026-11-24 09:00:00 JST, ends at Wed 2026-11-25 13:04:06 JST. --\n" +
				"2026-11-25T13:04:05+0900 web01 nginx[1]: only, and cut",
			want: "-- Journal begins at Tue 2026-11-24 09:00:00 JST, ends at Wed 2026-11-25 13:04:06 JST. --\n",
		},
		{
			name:      "truncated output without a timestamp is returned as it is",
			truncated: true,
			in:        "partial",
			want:      "partial",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := journalOldestFirst(tc.in, tc.truncated); got != tc.want {
				t.Errorf("journalOldestFirst(%q, %v)\n got %q\nwant %q", tc.in, tc.truncated, got, tc.want)
			}
		})
	}
}
