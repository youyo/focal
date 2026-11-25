//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
)

// TestCLIInspections runs each read-only operation against the real container
// and asserts the envelope it produces. The operations split three ways on this
// host: the ones that read generic Linux facts succeed, the two that need
// systemd fail because the container has none, and kernel is refused because it
// is off by default.
func TestCLIInspections(t *testing.T) {
	h := requireHarness(t)

	t.Run("ok operations", func(t *testing.T) {
		for _, op := range []string{"system", "cpu", "memory", "storage", "network", "inspect"} {
			t.Run(op, func(t *testing.T) {
				stdout, stderr, code := h.cli(context.Background(), op)
				if code != 0 {
					t.Fatalf("exit %d, stderr: %s", code, stderr)
				}
				env := decodeEnvelope(t, stdout)
				if env.Operation != op {
					t.Errorf("operation = %q, want %q", env.Operation, op)
				}
				if env.Status != "ok" {
					t.Errorf("status = %q, want ok\n%s", env.Status, stdout)
				}
			})
		}
	})

	t.Run("processes filtered to sshd", func(t *testing.T) {
		stdout, stderr, code := h.cli(context.Background(), "processes", "--name", "sshd")
		if code != 0 {
			t.Fatalf("exit %d, stderr: %s", code, stderr)
		}
		env := decodeEnvelope(t, stdout)
		if env.Status != "ok" {
			t.Fatalf("status = %q, want ok\n%s", env.Status, stdout)
		}
		lines := nonHeaderRows(env.Stdout)
		if len(lines) == 0 {
			t.Fatalf("no process rows survived the sshd filter\n%s", env.Stdout)
		}
		for _, line := range lines {
			if !strings.Contains(line, "sshd") {
				t.Errorf("row does not match the sshd filter: %q", line)
			}
		}
	})

	// service and logs need systemd, which the container does not run, so both
	// are a completed inspection that failed rather than a refusal.
	for _, tc := range []struct {
		op   string
		args []string
	}{
		{"service", []string{"nginx"}},
		{"logs", []string{"nginx"}},
	} {
		t.Run(tc.op+" fails without systemd", func(t *testing.T) {
			stdout, _, _ := h.cli(context.Background(), tc.op, tc.args...)
			env := decodeEnvelope(t, stdout)
			if env.Status != "failed" {
				t.Errorf("status = %q, want failed\n%s", env.Status, stdout)
			}
		})
	}

	// rpm is not on the container, so the package operation falls through to
	// dpkg-query. An installed package is ok; one that is not is a completed
	// inspection that failed; one dpkg still has on record after removal (nano,
	// see the Dockerfile) is not installed either.
	t.Run("package installed", func(t *testing.T) {
		stdout, stderr, code := h.cli(context.Background(), "package", "procps")
		if code != 0 {
			t.Fatalf("exit %d, stderr: %s", code, stderr)
		}
		env := decodeEnvelope(t, stdout)
		if env.Operation != "package" || env.Status != "ok" {
			t.Fatalf("operation/status = %q/%q, want package/ok\n%s", env.Operation, env.Status, stdout)
		}
		if env.Data["manager"] != "dpkg" || env.Data["installed"] != "true" || env.Data["version"] == "" {
			t.Errorf("data = %v, want manager=dpkg installed=true and a version", env.Data)
		}
		if got := len(env.Parts); got != 2 {
			t.Errorf("parts = %d, want 2 (rpm, then dpkg)\n%s", got, stdout)
		}
	})
	for _, tc := range []struct {
		name      string
		errorCode string
	}{
		// dpkg has no record: dpkg-query's own exit 1, no Error.
		{"definitely-not-a-package", ""},
		// dpkg has a record in the rc state: dpkg-query exits 0, and the
		// Envelope says not installed through an Error instead.
		{"nano", "package_not_installed"},
	} {
		t.Run("package not installed: "+tc.name, func(t *testing.T) {
			stdout, stderr, code := h.cli(context.Background(), "package", tc.name)
			if code != 0 {
				t.Fatalf("exit %d, stderr: %s", code, stderr)
			}
			env := decodeEnvelope(t, stdout)
			if env.Status != "failed" {
				t.Fatalf("status = %q, want failed\n%s", env.Status, stdout)
			}
			if env.Data["manager"] != "dpkg" || env.Data["installed"] != "false" {
				t.Errorf("data = %v, want manager=dpkg installed=false", env.Data)
			}
			switch {
			case tc.errorCode == "" && env.Error != nil:
				t.Errorf("error = %+v, want none: an absent package is an answer, not a failure to ask", env.Error)
			case tc.errorCode != "" && (env.Error == nil || env.Error.Code != tc.errorCode):
				t.Errorf("error = %+v, want code %s\n%s", env.Error, tc.errorCode, stdout)
			}
		})
	}

	t.Run("kernel is disabled by default", func(t *testing.T) {
		stdout, stderr, code := h.cli(context.Background(), "kernel")
		if code == 0 {
			t.Fatalf("kernel unexpectedly succeeded\n%s", stdout)
		}
		e := decodeError(t, stderr)
		if e.Kind != "policy" {
			t.Errorf("kind = %q, want policy\n%s", e.Kind, stderr)
		}
		if e.Code != "operation_disabled" {
			t.Errorf("code = %q, want operation_disabled\n%s", e.Code, stderr)
		}
	})
}

// TestCLIInjectionRejected confirms that a shell metacharacter smuggled into an
// operation argument is refused as a validation error with a non-zero exit,
// never composed into a remote command. focal has no exec path; these all fail
// at the value object.
func TestCLIInjectionRejected(t *testing.T) {
	h := requireHarness(t)

	for _, name := range []string{
		"nginx;id",
		"$(id)",
		"nginx|cat",
		"../../etc/passwd",
	} {
		for _, op := range []string{"service", "package"} {
			t.Run(op+" "+name, func(t *testing.T) {
				stdout, stderr, code := h.cli(context.Background(), op, name)
				if code == 0 {
					t.Fatalf("injection accepted\nstdout: %s", stdout)
				}
				e := decodeError(t, stderr)
				if e.Kind != "validation" {
					t.Errorf("kind = %q, want validation\n%s", e.Kind, stderr)
				}
			})
		}
	}
}

// TestCLIFlagLikeTargetRejected confirms focal never passes an ssh option
// through: a token that looks like a flag is parsed as one and refused, rather
// than reaching ssh(1) as a destination or an -o option.
func TestCLIFlagLikeTargetRejected(t *testing.T) {
	h := requireHarness(t)

	for name, args := range map[string][]string{
		"leading hyphen target": {"-target", "system"},
		"ssh -o option":         {"-o", "ProxyCommand=touch /tmp/pwned", targetHost, "system"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := h.run(context.Background(), nil, args...)
			if code == 0 {
				t.Fatalf("flag-like argument accepted\nstdout: %s", stdout)
			}
			e := decodeError(t, stderr)
			if e.Kind != "validation" {
				t.Errorf("kind = %q, want validation\n%s", e.Kind, stderr)
			}
		})
	}
}

// nonHeaderRows returns the non-empty rows of ps(1) output below the header,
// which processesFilter always keeps as the first line.
func nonHeaderRows(stdout string) []string {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var rows []string
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		rows = append(rows, line)
	}
	return rows
}
