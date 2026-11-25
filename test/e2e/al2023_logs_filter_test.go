//go:build e2e

package e2e

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tests in this file exercise what ADR 0011 added to logs — grep,
// grep-regex, count-by, an absolute since and an until — against
// focal-e2e-logger.service's six known journal entries on the AL2023 fixture.
// Every instant they use is taken from those entries' own timestamps, read
// back through a plain logs call, rather than from the host clock: the
// container's clock and the host's can differ by a little, and the entries
// were written when the container booted, not when the test runs.

// al2023LoggerEntries is how many entries focal-e2e-logger.service's journal
// holds: systemd's "Starting", three marker lines, the multi-line entry and
// systemd's "Started".
const al2023LoggerEntries = 6

// al2023LoggerTimes reads the logger's entries with a plain logs call and
// returns the first-line timestamp of each, oldest first, as journalctl
// printed it.
func al2023LoggerTimes(t *testing.T, h *harness) []string {
	t.Helper()
	stdout, stderr, code := h.cli(context.Background(), "logs", "focal-e2e-logger")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	var stamps []string
	for _, e := range splitJournalEntries(env.Stdout) {
		stamps = append(stamps, strings.TrimSuffix(journalEntryStart.FindString(e[0]), " "))
	}
	if len(stamps) != al2023LoggerEntries {
		t.Fatalf("plain logs returned %d entries, want %d\n%s", len(stamps), al2023LoggerEntries, env.Stdout)
	}
	return stamps
}

// parseShortISO reads a short-iso timestamp (offset +0900 or +09:00).
func parseShortISO(t *testing.T, s string) time.Time {
	t.Helper()
	for _, layout := range []string{"2006-01-02T15:04:05-0700", "2006-01-02T15:04:05-07:00"} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts
		}
	}
	t.Fatalf("cannot parse journal timestamp %q", s)
	return time.Time{}
}

// rfc3339 writes an instant the way a caller passes it to --since/--until.
func rfc3339(ts time.Time) string { return ts.Format(time.RFC3339) }

// logsCall runs logs focal-e2e-logger with args and decodes an ok envelope.
func logsCall(t *testing.T, h *harness, args ...string) envelope {
	t.Helper()
	stdout, stderr, code := h.cli(context.Background(), "logs", append([]string{"focal-e2e-logger"}, args...)...)
	if code != 0 {
		t.Fatalf("logs %v: exit %d, stderr: %s", args, code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if env.Status != "ok" {
		t.Fatalf("logs %v: status = %q, want ok\n%s", args, env.Status, stdout)
	}
	if env.Data == nil {
		t.Fatalf("logs %v: no data block\n%s", args, stdout)
	}
	return env
}

func assertData(t *testing.T, env envelope, key, want string) {
	t.Helper()
	if got := env.Data[key]; got != want {
		t.Errorf("data[%s] = %q, want %q (data %v)", key, got, want, env.Data)
	}
}

func TestAL2023LogsGrep(t *testing.T) {
	h := requireAL2023(t)
	stamps := al2023LoggerTimes(t, h)

	t.Run("fixed text returns only the matching entry", func(t *testing.T) {
		env := logsCall(t, h, "--grep", "marker line 2 of 3")
		entries := splitJournalEntries(env.Stdout)
		if len(entries) != 1 || !strings.Contains(entries[0][0], al2023LoggerLines[1]) {
			t.Fatalf("got entries %q, want only %q", entries, al2023LoggerLines[1])
		}
		assertData(t, env, "complete", "true")
		assertData(t, env, "scanned_entries", strconv.Itoa(al2023LoggerEntries))
		assertData(t, env, "matched_entries", "1")
		assertData(t, env, "covered_from", stamps[0])
		assertData(t, env, "covered_to", stamps[len(stamps)-1])
		if !tokyoOffset.MatchString(env.Data["covered_from"]) {
			t.Errorf("covered_from %q is not in the container's +09:00", env.Data["covered_from"])
		}
	})

	t.Run("a continuation line returns its whole entry", func(t *testing.T) {
		env := logsCall(t, h, "--grep", al2023MultilineParts[1])
		entries := splitJournalEntries(env.Stdout)
		if len(entries) != 1 || len(entries[0]) != len(al2023MultilineParts) {
			t.Fatalf("got entries %q, want the one three-line entry", entries)
		}
		for i, part := range al2023MultilineParts {
			if !strings.Contains(entries[0][i], part) {
				t.Errorf("line %d = %q, want it to contain %q", i, entries[0][i], part)
			}
		}
	})

	t.Run("grep-regex returns the matches oldest first", func(t *testing.T) {
		env := logsCall(t, h, "--grep-regex", `marker line [13] of 3$`)
		entries := splitJournalEntries(env.Stdout)
		if len(entries) != 2 {
			t.Fatalf("got %d entries, want 2\n%s", len(entries), env.Stdout)
		}
		for i, want := range []string{al2023LoggerLines[0], al2023LoggerLines[2]} {
			if !strings.Contains(entries[i][0], want) {
				t.Errorf("entry %d = %q, want %q", i, entries[i][0], want)
			}
		}
		assertData(t, env, "matched_entries", "2")
	})
}

var hourBucket = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}\+0900$`)

func TestAL2023LogsCountByHour(t *testing.T) {
	h := requireAL2023(t)
	stamps := al2023LoggerTimes(t, h)

	// The expected buckets come from the entries' own timestamps: each one
	// cut to its hour, in the offset journalctl printed.
	want := map[string]int{}
	for _, s := range stamps {
		want[s[:len("2006-01-02T15")]+strings.ReplaceAll(s[len("2006-01-02T15:04:05"):], ":", "")]++
	}

	env := logsCall(t, h, "--count-by", "hour")
	if env.Stdout != "" {
		t.Errorf("stdout = %q, want empty for a count", env.Stdout)
	}
	if len(env.Items) != len(want) {
		t.Fatalf("items = %v, want %d bucket(s) %v", env.Items, len(want), want)
	}
	total := 0
	for _, item := range env.Items {
		if !hourBucket.MatchString(item["bucket"]) {
			t.Errorf("bucket %q is not an hour in +0900", item["bucket"])
		}
		n, err := strconv.Atoi(item["count"])
		if err != nil || n != want[item["bucket"]] {
			t.Errorf("bucket %s count = %q, want %d", item["bucket"], item["count"], want[item["bucket"]])
		}
		total += n
	}
	if total != al2023LoggerEntries {
		t.Errorf("counts add up to %d, want %d", total, al2023LoggerEntries)
	}
	assertData(t, env, "complete", "true")
	assertData(t, env, "matched_entries", strconv.Itoa(al2023LoggerEntries))
}

func TestAL2023LogsAbsoluteWindow(t *testing.T) {
	h := requireAL2023(t)
	stamps := al2023LoggerTimes(t, h)
	first := parseShortISO(t, stamps[0])
	last := parseShortISO(t, stamps[len(stamps)-1])
	// The since is a minute before the first entry so that a host clock a
	// little behind the container's still sees it in the past.
	since := rfc3339(first.Add(-time.Minute))

	t.Run("since and until around the entries return all of them", func(t *testing.T) {
		env := logsCall(t, h, "--since", since, "--until", rfc3339(last.Add(time.Second)))
		for _, line := range al2023LoggerLines {
			if !strings.Contains(env.Stdout, line) {
				t.Errorf("stdout is missing %q\n%s", line, env.Stdout)
			}
		}
		assertData(t, env, "scanned_entries", strconv.Itoa(al2023LoggerEntries))
		assertData(t, env, "covered_from", stamps[0])
		assertData(t, env, "covered_to", stamps[len(stamps)-1])
		assertData(t, env, "complete", "true")
	})

	t.Run("an until at the first entry's whole second excludes every entry", func(t *testing.T) {
		// journalctl prints whole seconds, and every entry was written a
		// fraction of a second into the second it shows, so an until at
		// that whole second falls just before all of them.
		env := logsCall(t, h, "--since", since, "--until", rfc3339(first))
		if strings.Contains(env.Stdout, "marker line") {
			t.Errorf("stdout holds entries after until\n%s", env.Stdout)
		}
		assertData(t, env, "scanned_entries", "0")
		if _, ok := env.Data["covered_from"]; ok {
			t.Errorf("covered_from present with no entries: %v", env.Data)
		}
	})

	t.Run("an absolute since alone is open-ended", func(t *testing.T) {
		env := logsCall(t, h, "--since", since)
		assertData(t, env, "scanned_entries", strconv.Itoa(al2023LoggerEntries))
	})
}

func TestAL2023LogsRelativeUntil(t *testing.T) {
	h := requireAL2023(t)
	stamps := al2023LoggerTimes(t, h)
	last := parseShortISO(t, stamps[len(stamps)-1])
	// The relative until below is a few seconds back on the container's
	// clock; wait until the newest entry is older than that, should this run
	// right after the container booted.
	if wait := time.Until(last.Add(5 * time.Second)); wait > 0 {
		time.Sleep(wait)
	}

	env := logsCall(t, h, "--since", "1h", "--until", "3s")
	assertData(t, env, "scanned_entries", strconv.Itoa(al2023LoggerEntries))
	assertData(t, env, "complete", "true")

	// An until an hour back, with since left to its default, is a window
	// that ends before the container existed.
	env = logsCall(t, h, "--until", "1h")
	assertData(t, env, "scanned_entries", "0")
	if strings.Contains(env.Stdout, "marker line") {
		t.Errorf("stdout holds entries newer than until\n%s", env.Stdout)
	}
}

// TestAL2023MCPLogsFilter drives the same arguments through focal serve and
// the protocol's own client: the names an agent passes are the CLI's.
func TestAL2023MCPLogsFilter(t *testing.T) {
	h := requireAL2023(t)
	url := h.startServe(t, upstreamToken)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session := connectMCP(t, ctx, url, upstreamToken)
	defer session.Close()

	res, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "inspect_logs",
		Arguments: map[string]any{
			"host": targetHost, "service": "focal-e2e-logger",
			"grep-regex": `marker line \d of 3`, "count-by": "hour",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("inspect_logs reported an error: %s", toolText(t, res))
	}
	env := decodeEnvelope(t, toolText(t, res))
	total := 0
	for _, item := range env.Items {
		n, _ := strconv.Atoi(item["count"])
		total += n
	}
	if total != len(al2023LoggerLines) {
		t.Errorf("items %v add up to %d, want %d", env.Items, total, len(al2023LoggerLines))
	}
	if env.Data["matched_entries"] != strconv.Itoa(len(al2023LoggerLines)) || env.Data["complete"] != "true" {
		t.Errorf("data = %v, want %d matched and complete", env.Data, len(al2023LoggerLines))
	}
}
