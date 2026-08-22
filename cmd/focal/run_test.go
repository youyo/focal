package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withXDGConfigHome points XDG_CONFIG_HOME at a fresh directory and, when
// source names a fixture under internal/config/testdata, copies it in as
// focal/config.yaml so config.Load finds it without this package
// duplicating fixture files.
func withXDGConfigHome(t *testing.T, source string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if source == "" {
		return
	}
	// #nosec G304 -- source is always a fixed string literal passed by a
	// test in this file, never external input.
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read fixture %s: %v", source, err)
	}
	focalDir := filepath.Join(dir, "focal")
	if err := os.MkdirAll(focalDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", focalDir, err)
	}
	target := filepath.Join(focalDir, "config.yaml")
	// #nosec G703 -- target is built from t.TempDir(), not external input.
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}
}

func TestRun_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("stdout is empty, want a version string")
	}
}

func TestRun_NoConfigFile_UsesSafeDefaults(t *testing.T) {
	withXDGConfigHome(t, "")

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRun_ValidConfig_Succeeds(t *testing.T) {
	withXDGConfigHome(t, "../../internal/config/testdata/valid.yaml")

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRun_CapabilityViolation_FailsWithStructuredError(t *testing.T) {
	withXDGConfigHome(t, "../../internal/config/testdata/capability_violation.yaml")

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	line := strings.TrimSpace(stderr.String())
	var payload struct {
		Kind    string `json:"kind"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("stderr is not a single JSON line: %v (stderr=%q)", err, line)
	}
	if payload.Kind != "policy" {
		t.Fatalf("kind = %q, want %q", payload.Kind, "policy")
	}
	if strings.Contains(stderr.String(), "\n") && !strings.HasSuffix(stderr.String(), "\n") {
		t.Fatalf("stderr has more than one line: %q", stderr.String())
	}
}

func TestRun_UnknownField_FailsWithStructuredError(t *testing.T) {
	withXDGConfigHome(t, "../../internal/config/testdata/unknown_field.yaml")

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}

	line := strings.TrimSpace(stderr.String())
	var payload struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("stderr is not a single JSON line: %v (stderr=%q)", err, line)
	}
	if payload.Kind != "validation" {
		t.Fatalf("kind = %q, want %q", payload.Kind, "validation")
	}
}

func TestRun_UnknownFlag_FailsWithUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-bogus"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
}
