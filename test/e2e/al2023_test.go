//go:build e2e

package e2e

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// The tests in this file run focal against the second fixture: an Amazon Linux
// 2023 container with systemd as PID 1 (test/e2e/systemd). They exercise the
// operations the ubuntu container can only show failing — service and logs —
// on a real systemd and journald, and read AL2023's own userland. The fixture
// is started alongside the ubuntu one but its failure is reported only here,
// through requireAL2023, so the ubuntu tests never fail because of it.

// The marker lines focal-e2e-logger.service writes to the journal when it
// starts. They are fixed text in test/e2e/systemd/focal-e2e-logger, and the
// logs test asserts focal returns every one of them: an empty but "ok" answer —
// what an account outside systemd-journal gets — is a failure here.
var al2023LoggerLines = []string{
	"focal-e2e-logger: marker line 1 of 3",
	"focal-e2e-logger: marker line 2 of 3",
	"focal-e2e-logger: marker line 3 of 3",
}

// al2023MultilineParts are the three lines of the single multi-line journal
// entry focal-e2e-logger sends through the native journal protocol.
var al2023MultilineParts = []string{
	"focal-e2e-logger: multiline entry first line",
	"multiline entry second line",
	"multiline entry third line",
}

// tokyoOffset matches a timestamp journalctl's short-iso or date
// --iso-8601=seconds prints in Asia/Tokyo, in either offset spelling.
var tokyoOffset = regexp.MustCompile(`T\d{2}:\d{2}:\d{2}\+09:?00\b`)

func TestAL2023Service(t *testing.T) {
	h := requireAL2023(t)

	for _, tc := range []struct {
		service  string
		subState string
	}{
		{"focal-e2e-logger", "running"},
		{"sshd", "running"},
	} {
		t.Run(tc.service, func(t *testing.T) {
			stdout, stderr, code := h.cli(context.Background(), "service", tc.service)
			if code != 0 {
				t.Fatalf("exit %d, stderr: %s", code, stderr)
			}
			env := decodeEnvelope(t, stdout)
			if env.Status != "ok" {
				t.Fatalf("status = %q, want ok\n%s", env.Status, stdout)
			}
			want := map[string]string{
				"Id":          tc.service + ".service",
				"LoadState":   "loaded",
				"ActiveState": "active",
				"SubState":    tc.subState,
			}
			for k, v := range want {
				if got := env.Data[k]; got != v {
					t.Errorf("Data[%s] = %q, want %q\n%s", k, got, v, stdout)
				}
			}
		})
	}
}

func TestAL2023Logs(t *testing.T) {
	h := requireAL2023(t)

	stdout, stderr, code := h.cli(context.Background(), "logs", "focal-e2e-logger")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if env.Status != "ok" {
		t.Fatalf("status = %q, want ok\n%s", env.Status, stdout)
	}
	for _, line := range append(append([]string{}, al2023LoggerLines...), al2023MultilineParts...) {
		if !strings.Contains(env.Stdout, line) {
			t.Errorf("logs output is missing %q\n%s", line, env.Stdout)
		}
	}
	// Every marker line is a journal entry of its own, stamped in the
	// container's Asia/Tokyo zone rather than UTC.
	for _, row := range strings.Split(env.Stdout, "\n") {
		if strings.Contains(row, "marker line") && !tokyoOffset.MatchString(row) {
			t.Errorf("entry not stamped in +09:00: %q", row)
		}
	}
}

// journalEntryStart matches the first line of a short-iso journal entry: the
// timestamp, in either offset spelling, followed by a space. Every other line
// belongs to the entry above it. The test keeps its own copy of the rule so
// that it checks focal's grouping rather than reusing it.
var journalEntryStart = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{2}:?\d{2} `)

// splitJournalEntries groups stdout into entries, each one its first line
// followed by its continuation lines.
func splitJournalEntries(stdout string) [][]string {
	var entries [][]string
	for _, row := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		if journalEntryStart.MatchString(row) || len(entries) == 0 {
			entries = append(entries, []string{row})
			continue
		}
		entries[len(entries)-1] = append(entries[len(entries)-1], row)
	}
	return entries
}

// TestAL2023LogsReturnsTheNewestEntriesOldestFirst asks for fewer lines than
// the span holds. focal-e2e-logger.service's journal is, oldest first:
// systemd's "Starting", the three marker lines, the multi-line entry and
// systemd's "Started". The answer must be the newest entries of that span, in
// the order they were written, with the multi-line entry kept whole.
func TestAL2023LogsReturnsTheNewestEntriesOldestFirst(t *testing.T) {
	h := requireAL2023(t)

	for _, tc := range []struct {
		lines string
		want  []string // a substring of each entry's first line, oldest first
	}{
		{"2", []string{al2023MultilineParts[0], "Started focal-e2e-logger.service"}},
		{"3", []string{al2023LoggerLines[2], al2023MultilineParts[0], "Started focal-e2e-logger.service"}},
	} {
		t.Run("lines "+tc.lines, func(t *testing.T) {
			stdout, stderr, code := h.cli(context.Background(), "logs", "focal-e2e-logger", "--lines", tc.lines)
			if code != 0 {
				t.Fatalf("exit %d, stderr: %s", code, stderr)
			}
			env := decodeEnvelope(t, stdout)
			if env.Status != "ok" {
				t.Fatalf("status = %q, want ok\n%s", env.Status, stdout)
			}
			entries := splitJournalEntries(env.Stdout)
			if len(entries) != len(tc.want) {
				t.Fatalf("got %d entries, want %d\n%s", len(entries), len(tc.want), env.Stdout)
			}
			for i, want := range tc.want {
				if !strings.Contains(entries[i][0], want) {
					t.Errorf("entry %d starts %q, want it to contain %q\n%s", i, entries[i][0], want, env.Stdout)
				}
			}
			// The multi-line entry is one entry: its first line directly
			// followed by its two continuation lines, nothing in between.
			multiline := entries[len(entries)-2]
			if len(multiline) != len(al2023MultilineParts) {
				t.Fatalf("multi-line entry has %d lines, want %d\n%s", len(multiline), len(al2023MultilineParts), env.Stdout)
			}
			for i, part := range al2023MultilineParts {
				if !strings.Contains(multiline[i], part) {
					t.Errorf("multi-line entry line %d = %q, want it to contain %q\n%s", i, multiline[i], part, env.Stdout)
				}
			}
		})
	}
}

func TestAL2023System(t *testing.T) {
	h := requireAL2023(t)

	stdout, stderr, code := h.cli(context.Background(), "system")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if env.Status != "ok" {
		t.Fatalf("status = %q, want ok\n%s", env.Status, stdout)
	}
	osRelease := partStdout(t, env, "os-release")
	for _, want := range []string{`ID="amzn"`, `VERSION_ID="2023"`} {
		if !strings.Contains(osRelease, want) {
			t.Errorf("os-release is missing %s\n%s", want, osRelease)
		}
	}
	if date := partStdout(t, env, "date"); !tokyoOffset.MatchString(date) {
		t.Errorf("date is not in Asia/Tokyo: %q", date)
	}
}

// TestAL2023Inspections runs the operations that read generic Linux facts
// against AL2023's own userland, where each must come back ok just as it does
// on ubuntu: a tool an operation shells out to that AL2023 lacks shows up here.
func TestAL2023Inspections(t *testing.T) {
	h := requireAL2023(t)

	for _, op := range []string{"cpu", "memory", "storage", "network", "inspect"} {
		t.Run(op, func(t *testing.T) {
			stdout, stderr, code := h.cli(context.Background(), op)
			if code != 0 {
				t.Fatalf("exit %d, stderr: %s", code, stderr)
			}
			env := decodeEnvelope(t, stdout)
			if env.Status != "ok" {
				t.Errorf("status = %q, want ok\n%s", env.Status, stdout)
			}
		})
	}

	t.Run("processes filtered to sshd", func(t *testing.T) {
		stdout, stderr, code := h.cli(context.Background(), "processes", "--name", "sshd")
		if code != 0 {
			t.Fatalf("exit %d, stderr: %s", code, stderr)
		}
		env := decodeEnvelope(t, stdout)
		if env.Status != "ok" {
			t.Fatalf("status = %q, want ok\n%s", env.Status, stdout)
		}
		if len(nonHeaderRows(env.Stdout)) == 0 {
			t.Errorf("no process rows survived the sshd filter\n%s", env.Stdout)
		}
	})
}

// nevra matches an rpm -q answer for an installed package: name, version,
// release and architecture in one token.
var nevra = regexp.MustCompile(`^[A-Za-z0-9._+-]+-[0-9][^ ]*\.(x86_64|aarch64|noarch)$`)

// TestAL2023Package runs the package operation against a host that has rpm and
// no dpkg-query, the case the ubuntu container cannot show.
func TestAL2023Package(t *testing.T) {
	h := requireAL2023(t)

	run := func(t *testing.T, name string) (string, envelope) {
		t.Helper()
		stdout, stderr, code := h.cli(context.Background(), "package", name)
		if code != 0 {
			t.Fatalf("exit %d, stderr: %s", code, stderr)
		}
		return stdout, decodeEnvelope(t, stdout)
	}

	t.Run("installed", func(t *testing.T) {
		stdout, env := run(t, "bash")
		if env.Status != "ok" || env.ExitCode != 0 || env.Error != nil {
			t.Fatalf("status/exit/error = %q/%d/%v, want ok/0/none\n%s", env.Status, env.ExitCode, env.Error, stdout)
		}
		if env.Data["manager"] != "rpm" || env.Data["installed"] != "true" {
			t.Errorf("data = %v, want manager=rpm installed=true", env.Data)
		}
		if got := env.Data["version"]; !nevra.MatchString(got) || !strings.HasPrefix(got, "bash-") {
			t.Errorf("version = %q, want bash's name-version-release.arch from rpm -q", got)
		}
		if got := partStdout(t, env, "rpm"); strings.TrimSpace(got) != env.Data["version"] {
			t.Errorf("version %q is not rpm -q's own line %q", env.Data["version"], got)
		}
		if len(env.Parts) != 1 || env.Parts[0].Name != "rpm" {
			t.Errorf("parts = %+v, want only rpm: dpkg-query must not run once rpm answered\n%s", env.Parts, stdout)
		}
	})

	// rpm -q exits 1 for an unknown package and AL2023 has no dpkg-query, so
	// the shell reports 127 for the second command. rpm is then the last
	// manager that answered, and its exit status is the envelope's.
	t.Run("not installed", func(t *testing.T) {
		stdout, env := run(t, "definitely-not-a-package")
		if env.Status != "failed" || env.ExitCode != 1 {
			t.Fatalf("status/exit = %q/%d, want failed/1\n%s", env.Status, env.ExitCode, stdout)
		}
		if env.Data["manager"] != "rpm" || env.Data["installed"] != "false" {
			t.Errorf("data = %v, want manager=rpm installed=false", env.Data)
		}
		if env.Error != nil {
			t.Errorf("error = %+v, want none: an absent package is an answer", env.Error)
		}
		if len(env.Parts) != 2 || env.Parts[0].Name != "rpm" || env.Parts[0].ExitCode != 1 ||
			env.Parts[1].Name != "dpkg" || env.Parts[1].ExitCode != 127 {
			t.Errorf("parts = %+v, want rpm exit 1 then dpkg exit 127\n%s", env.Parts, stdout)
		}
		if got := partStdout(t, env, "rpm"); !strings.Contains(got, "is not installed") {
			t.Errorf("rpm stdout = %q, want rpm's own not-installed line", got)
		}
	})

	// gpg-pubkey is installed twice under different versions (see the
	// Dockerfile), so rpm -q prints two lines and still exits 0. Both stay in
	// the Part's stdout; no single one is reported as Data["version"].
	t.Run("several versions installed", func(t *testing.T) {
		stdout, env := run(t, "gpg-pubkey")
		if env.Status != "ok" || env.ExitCode != 0 {
			t.Fatalf("status/exit = %q/%d, want ok/0\n%s", env.Status, env.ExitCode, stdout)
		}
		if env.Data["manager"] != "rpm" || env.Data["installed"] != "true" {
			t.Errorf("data = %v, want manager=rpm installed=true", env.Data)
		}
		lines := strings.Split(strings.TrimSpace(partStdout(t, env, "rpm")), "\n")
		if len(lines) != 2 {
			t.Fatalf("rpm -q printed %d lines, want 2\n%s", len(lines), stdout)
		}
		for _, line := range lines {
			if !strings.HasPrefix(line, "gpg-pubkey-") {
				t.Errorf("line %q is not a gpg-pubkey version", line)
			}
		}
		if v, ok := env.Data["version"]; ok {
			t.Errorf("version = %q, want none: more than one version is installed", v)
		}
		if len(env.Parts) != 1 {
			t.Errorf("parts = %d, want 1 (rpm only)", len(env.Parts))
		}
	})
}

// partStdout returns the stdout of the part of env named name, failing the test
// when env has no such part.
func partStdout(t *testing.T, env envelope, name string) string {
	t.Helper()
	for _, p := range env.Parts {
		if p.Name == name {
			return p.Stdout
		}
	}
	t.Fatalf("envelope has no %q part", name)
	return ""
}
