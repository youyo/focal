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
