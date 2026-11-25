package operation

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
)

// logsFilterNewestFirst is shaped like journalctl --reverse --output=short-iso
// on systemd 252: newest entry first, offsets spelled +0900, the continuation
// line of a multi-line entry indented to the message column. One entry is
// spelled +09:00 so that both offset forms reach the bucket rule.
const logsFilterNewestFirst = "2026-11-25T14:05:00+0900 web01 app[2]: request ok\n" +
	"2026-11-25T13:59:59+0900 web01 app[2]: connection refused by upstream\n" +
	"                                       retrying in 5s\n" +
	"2026-11-25T13:10:00+09:00 web01 app[2]: request ok\n" +
	"2026-11-25T13:00:00+0900 web01 app[2]: Connection refused\n" +
	"2026-11-24T23:30:00+0900 web01 app[2]: connection refused\n"

// logsFilterRun builds params through the clock-pinned registry and runs the
// operation against a Recorder that answers with out and execErr.
func logsFilterRun(t *testing.T, params Params, out ssh.Output, execErr error) result.Envelope {
	t.Helper()
	params.ServiceName = "app"
	params.MaxSince = journalTestMaxSince(t)
	params.MaxLines = journalTestMaxLines(t)
	op, err := logsTestRegistry().Build("logs", params)
	if err != nil {
		t.Fatalf("Build(logs, %+v) = %v", params, err)
	}
	rec := &sshtest.Recorder{}
	rec.Respond(out, execErr)
	env, runErr := op.Execute(context.Background(), rec, journalTestTarget(t), policy.Policy{})
	if runErr != nil {
		t.Fatalf("Execute() = %v", runErr)
	}
	return env
}

func assertLogsData(t *testing.T, env result.Envelope, want map[string]string) {
	t.Helper()
	if !maps.Equal(env.Data, want) {
		t.Errorf("Data\n got: %v\nwant: %v", env.Data, want)
	}
}

func TestLogsGrepReturnsMatchingEntriesOldestFirst(t *testing.T) {
	env := logsFilterRun(t, Params{Grep: "connection refused"}, ssh.Output{Stdout: logsFilterNewestFirst}, nil)

	want := "2026-11-24T23:30:00+0900 web01 app[2]: connection refused\n" +
		"2026-11-25T13:59:59+0900 web01 app[2]: connection refused by upstream\n" +
		"                                       retrying in 5s\n"
	if env.Status != result.StatusOK || env.Stdout != want {
		t.Errorf("Status = %q, Stdout =\n%s\nwant ok and\n%s", env.Status, env.Stdout, want)
	}
	if env.Items != nil {
		t.Errorf("Items = %v, want none without count-by", env.Items)
	}
	assertLogsData(t, env, map[string]string{
		"complete":        "true",
		"scanned_entries": "5",
		"matched_entries": "2",
		"covered_from":    "2026-11-24T23:30:00+0900",
		"covered_to":      "2026-11-25T14:05:00+0900",
	})
}

// TestLogsGrepMatchesTheWholeEntry pins what the text is matched against: the
// entry's full text, its first line's timestamp, host and identifier prefix
// included, and its continuation lines.
func TestLogsGrepMatchesTheWholeEntry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		grep    string
		matched string
	}{
		{"a continuation line matches its entry", "retrying in 5s", "1"},
		{"the identifier prefix is part of the entry", "web01 app[2]:", "5"},
		{"the timestamp is part of the entry", "2026-11-25T13:10:00+09:00", "1"},
		{"matching is case-sensitive", "Connection refused", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := logsFilterRun(t, Params{Grep: tc.grep}, ssh.Output{Stdout: logsFilterNewestFirst}, nil)
			if got := env.Data["matched_entries"]; got != tc.matched {
				t.Errorf("matched_entries = %q, want %q\n%s", got, tc.matched, env.Stdout)
			}
		})
	}
	env := logsFilterRun(t, Params{Grep: "retrying in 5s"}, ssh.Output{Stdout: logsFilterNewestFirst}, nil)
	want := "2026-11-25T13:59:59+0900 web01 app[2]: connection refused by upstream\n" +
		"                                       retrying in 5s\n"
	if env.Stdout != want {
		t.Errorf("Stdout = %q, want the whole entry %q", env.Stdout, want)
	}
}

func TestLogsGrepKeepsTheNewestLinesMatches(t *testing.T) {
	env := logsFilterRun(t, Params{Grep: "refused", Lines: "2"}, ssh.Output{Stdout: logsFilterNewestFirst}, nil)
	want := "2026-11-25T13:00:00+0900 web01 app[2]: Connection refused\n" +
		"2026-11-25T13:59:59+0900 web01 app[2]: connection refused by upstream\n" +
		"                                       retrying in 5s\n"
	if env.Stdout != want {
		t.Errorf("Stdout = %q, want %q", env.Stdout, want)
	}
	if got := env.Data["matched_entries"]; got != "3" {
		t.Errorf("matched_entries = %q, want every match counted, 3", got)
	}
}

func TestLogsGrepRegex(t *testing.T) {
	env := logsFilterRun(t, Params{GrepRegex: `[Cc]onnection refused$`}, ssh.Output{Stdout: logsFilterNewestFirst}, nil)
	want := "2026-11-24T23:30:00+0900 web01 app[2]: connection refused\n" +
		"2026-11-25T13:00:00+0900 web01 app[2]: Connection refused\n"
	if env.Stdout != want {
		t.Errorf("Stdout = %q, want %q", env.Stdout, want)
	}
	if got := env.Data["matched_entries"]; got != "2" {
		t.Errorf("matched_entries = %q, want 2", got)
	}
}

func TestLogsCountByHour(t *testing.T) {
	env := logsFilterRun(t, Params{CountBy: "hour"}, ssh.Output{Stdout: logsFilterNewestFirst}, nil)
	if env.Stdout != "" {
		t.Errorf("Stdout = %q, want empty: a count returns no entries", env.Stdout)
	}
	want := []map[string]string{
		{"bucket": "2026-11-24T23+0900", "count": "1"},
		{"bucket": "2026-11-25T13+0900", "count": "3"},
		{"bucket": "2026-11-25T14+0900", "count": "1"},
	}
	if !reflect.DeepEqual(env.Items, want) {
		t.Errorf("Items\n got: %v\nwant: %v", env.Items, want)
	}
	assertLogsData(t, env, map[string]string{
		"complete":        "true",
		"scanned_entries": "5",
		"matched_entries": "5",
		"covered_from":    "2026-11-24T23:30:00+0900",
		"covered_to":      "2026-11-25T14:05:00+0900",
	})
}

func TestLogsCountByDayCountsOnlyMatches(t *testing.T) {
	env := logsFilterRun(t, Params{CountBy: "day", Grep: "refused", Lines: "1"}, ssh.Output{Stdout: logsFilterNewestFirst}, nil)
	want := []map[string]string{
		{"bucket": "2026-11-24+0900", "count": "1"},
		{"bucket": "2026-11-25+0900", "count": "2"},
	}
	if !reflect.DeepEqual(env.Items, want) {
		t.Errorf("Items\n got: %v\nwant: %v (lines does not bound a count)", env.Items, want)
	}
	if env.Stdout != "" {
		t.Errorf("Stdout = %q, want empty", env.Stdout)
	}
}

func TestLogsCountByWithNoEntries(t *testing.T) {
	env := logsFilterRun(t, Params{CountBy: "hour"}, ssh.Output{Stdout: "-- No entries --\n"}, nil)
	if env.Items != nil || env.Stdout != "" {
		t.Errorf("Items = %v, Stdout = %q, want neither", env.Items, env.Stdout)
	}
	assertLogsData(t, env, map[string]string{
		"complete":        "true",
		"scanned_entries": "0",
		"matched_entries": "0",
	})
}

// TestLogsLinesThatAreNotEntriesAreNeitherCountedNorMatched covers the marker
// lines journalctl prints that do not start with a timestamp.
func TestLogsLinesThatAreNotEntriesAreNeitherCountedNorMatched(t *testing.T) {
	env := logsFilterRun(t, Params{Grep: "--"}, ssh.Output{Stdout: "-- No entries --\n"}, nil)
	if env.Stdout != "" {
		t.Errorf("Stdout = %q, want empty", env.Stdout)
	}
	assertLogsData(t, env, map[string]string{"complete": "true", "scanned_entries": "0", "matched_entries": "0"})
}

// TestLogsWindowOnlyRequestReportsData covers a request that uses no filter
// but a new window form: the entries come back as for a plain request, and
// the data block is there all the same.
func TestLogsWindowOnlyRequestReportsData(t *testing.T) {
	env := logsFilterRun(t, Params{Until: "1m"}, ssh.Output{Stdout: journalTestNewestFirst}, nil)
	if env.Stdout != journalTestOldestFirst {
		t.Errorf("Stdout = %q, want %q", env.Stdout, journalTestOldestFirst)
	}
	assertLogsData(t, env, map[string]string{
		"complete":        "true",
		"scanned_entries": "3",
		"matched_entries": "3",
		"covered_from":    "2026-11-25T13:04:05+0900",
		"covered_to":      "2026-11-25T13:04:07+0900",
	})

	empty := logsFilterRun(t, Params{Since: "2026-11-25T09:00:00+09:00"}, ssh.Output{Stdout: "-- No entries --\n"}, nil)
	if empty.Stdout != "-- No entries --\n" {
		t.Errorf("Stdout = %q, want journalctl's own marker kept as a plain request keeps it", empty.Stdout)
	}
	assertLogsData(t, empty, map[string]string{"complete": "true", "scanned_entries": "0", "matched_entries": "0"})
}

func TestLogsWindowOnlyRequestAtItsLineCountIsIncomplete(t *testing.T) {
	env := logsFilterRun(t, Params{Until: "1m", Lines: "3"}, ssh.Output{Stdout: journalTestNewestFirst}, nil)
	if got := env.Data["complete"]; got != "false" {
		t.Errorf("complete = %q, want false: -n 3 returned 3 entries, so the window may hold more", got)
	}
	env = logsFilterRun(t, Params{Until: "1m", Lines: "4"}, ssh.Output{Stdout: journalTestNewestFirst}, nil)
	if got := env.Data["complete"]; got != "true" {
		t.Errorf("complete = %q, want true below the line count", got)
	}
}

func TestLogsScanLimitReachedIsIncomplete(t *testing.T) {
	var b strings.Builder
	for i := range logsScanLimit.Int() {
		fmt.Fprintf(&b, "2026-11-25T13:%02d:%02d+0900 web01 app[2]: line %d\n", 59-i/60%60, 59-i%60, i)
	}
	env := logsFilterRun(t, Params{Grep: "line 1"}, ssh.Output{Stdout: b.String()}, nil)
	if got := env.Data["complete"]; got != "false" {
		t.Errorf("complete = %q, want false at the scan limit", got)
	}
	if got := env.Data["scanned_entries"]; got != "100000" {
		t.Errorf("scanned_entries = %q, want 100000", got)
	}
}

func TestLogsFilterTruncatedDropsTheOldestEntryFirst(t *testing.T) {
	out := ssh.Output{Stdout: logsFilterNewestFirst + "2026-11-24T23:00:00+0900 web01 app[2]: connection ref", Truncated: true, ExitCode: -1}
	env := logsFilterRun(t, Params{Grep: "connection refused"}, out, nil)
	if env.Status != result.StatusTruncated || !env.Truncated {
		t.Errorf("Status = %q, Truncated = %t, want truncated", env.Status, env.Truncated)
	}
	assertLogsData(t, env, map[string]string{
		"complete":        "false",
		"scanned_entries": "5",
		"matched_entries": "2",
		"covered_from":    "2026-11-24T23:30:00+0900",
		"covered_to":      "2026-11-25T14:05:00+0900",
	})
}

func TestLogsFilterFailuresAreIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name    string
		out     ssh.Output
		execErr error
		status  result.Status
	}{
		{"timeout", ssh.Output{Stdout: logsFilterNewestFirst}, result.ExecutionError("timeout", "command timed out"), result.StatusTimeout},
		{"ssh failure", ssh.Output{}, errors.New("connection reset by peer"), result.StatusFailed},
		{"non-zero exit", ssh.Output{Stderr: "--since= must be before --until=.\n", ExitCode: 1}, nil, result.StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := logsFilterRun(t, Params{CountBy: "hour"}, tc.out, tc.execErr)
			if env.Status != tc.status {
				t.Errorf("Status = %q, want %q", env.Status, tc.status)
			}
			if got := env.Data["complete"]; got != "false" {
				t.Errorf("complete = %q, want false; Data = %v", got, env.Data)
			}
			for _, key := range []string{"scanned_entries", "matched_entries"} {
				if _, ok := env.Data[key]; !ok {
					t.Errorf("Data has no %s: %v", key, env.Data)
				}
			}
		})
	}
}

// TestLogsLegacyRequestCarriesNoDataOrItems pins that a request using none of
// the new arguments answers exactly as it did before them.
func TestLogsLegacyRequestCarriesNoDataOrItems(t *testing.T) {
	env := logsFilterRun(t, Params{Since: "1h", Lines: "50"}, ssh.Output{Stdout: journalTestNewestFirst}, nil)
	if env.Data != nil || env.Items != nil {
		t.Errorf("Data = %v, Items = %v, want neither on a plain request", env.Data, env.Items)
	}
	if env.Stdout != journalTestOldestFirst {
		t.Errorf("Stdout = %q, want %q", env.Stdout, journalTestOldestFirst)
	}
}

func TestLogsCountByEncodesItems(t *testing.T) {
	env := logsFilterRun(t, Params{CountBy: "day"}, ssh.Output{Stdout: logsFilterNewestFirst}, nil)
	body, err := env.Encode()
	if err != nil {
		t.Fatalf("Encode() = %v", err)
	}
	want := `"items":[{"bucket":"2026-11-24+0900","count":"1"},{"bucket":"2026-11-25+0900","count":"4"}]`
	if !strings.Contains(string(body), want) {
		t.Errorf("Encode() = %s, want it to contain %s", body, want)
	}
	if strings.Contains(string(body), `"stdout"`) {
		t.Errorf("Encode() = %s, want no stdout for a count", body)
	}
}

// TestLogsTruncatedAloneIsIncomplete isolates the max_output cut from every
// other reason an answer can be incomplete: the command exited 0, nothing
// failed, and the scan limit is far away, yet the cut means older entries in
// the window were never read.
func TestLogsTruncatedAloneIsIncomplete(t *testing.T) {
	env := logsFilterRun(t, Params{Grep: "refused"}, ssh.Output{Stdout: logsFilterNewestFirst, Truncated: true, ExitCode: 0}, nil)
	if got := env.Data["complete"]; got != "false" {
		t.Errorf("complete = %q, want false for a truncated answer; Data = %v", got, env.Data)
	}
}
