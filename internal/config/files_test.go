package config

import (
	"slices"
	"testing"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

func TestFilesIsDisabledByDefaultWithNoList(t *testing.T) {
	isolate(t)
	files := mustOperation(t, mustLoad(t), "files")
	if files.Enabled() {
		t.Error("files is enabled by default; an administrator must turn it on and name its paths")
	}
	if got := files.FilesAllowList().Entries(); len(got) != 0 {
		t.Errorf("default files list = %q, want none", got)
	}
	if got := files.Policy().Sudo(); got != policy.SudoNever {
		t.Errorf("files Sudo() = %v, want never", got)
	}
}

func TestFilesConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		code    string
		field   string
		enabled bool
		paths   []string
	}{
		{
			name:    "enabled with paths",
			body:    "operations:\n  files:\n    enabled: true\n    paths:\n      - /var/log\n      - /etc/nginx\n",
			enabled: true,
			paths:   []string{"/var/log", "/etc/nginx"},
		},
		{
			name:  "paths alone do not enable it",
			body:  "operations:\n  files:\n    paths:\n      - /var/log\n",
			paths: []string{"/var/log"},
		},
		{
			name: "disabled with an empty list",
			body: "operations:\n  files:\n    enabled: false\n    paths: []\n",
		},
		{
			name:  "enabled without paths",
			body:  "operations:\n  files:\n    enabled: true\n",
			code:  "files_paths_required",
			field: "operations.files.paths",
		},
		{
			name:  "enabled with an empty list",
			body:  "operations:\n  files:\n    enabled: true\n    paths: []\n",
			code:  "files_paths_required",
			field: "operations.files.paths",
		},
		{
			name:  "a bad entry is an error even while disabled",
			body:  "operations:\n  files:\n    enabled: false\n    paths:\n      - var/log\n",
			code:  "invalid_files_path_entry",
			field: "operations.files.paths",
		},
		{
			name:  "root alone",
			body:  "operations:\n  files:\n    enabled: true\n    paths:\n      - /\n",
			code:  "invalid_files_path_entry",
			field: "operations.files.paths",
		},
		{
			name:  "trailing slash",
			body:  "operations:\n  files:\n    enabled: true\n    paths:\n      - /var/log/\n",
			code:  "invalid_files_path_entry",
			field: "operations.files.paths",
		},
		{
			name:  "proc",
			body:  "operations:\n  files:\n    enabled: true\n    paths:\n      - /proc/1\n",
			code:  "forbidden_files_path_entry",
			field: "operations.files.paths",
		},
		{
			name:  "duplicate",
			body:  "operations:\n  files:\n    enabled: true\n    paths:\n      - /var/log\n      - /var/log\n",
			code:  "duplicate_files_path_entry",
			field: "operations.files.paths",
		},
		{
			name:  "paths on another operation",
			body:  "operations:\n  logs:\n    paths:\n      - /var/log\n",
			code:  "unsupported_operation_field",
			field: "operations.logs.paths",
		},
		{
			name:  "paths on a no-argument operation",
			body:  "operations:\n  system:\n    paths:\n      - /var/log\n",
			code:  "unsupported_operation_field",
			field: "operations.system.paths",
		},
		{
			name:  "line limits on files",
			body:  "operations:\n  files:\n    max_lines: 10\n",
			code:  "unsupported_operation_field",
			field: "operations.files.max_lines",
		},
		{
			name:  "sudo auto on files",
			body:  "operations:\n  files:\n    sudo: auto\n",
			code:  "sudo_not_allowed",
			field: "operations.files.sudo",
		},
		{
			name:  "an unknown key under files",
			body:  "operations:\n  files:\n    allow_ssm: true\n",
			code:  "invalid_yaml",
			field: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.body)
			cfg, err := Load()
			if tt.code != "" {
				kind := result.KindValidation
				if tt.code == "sudo_not_allowed" {
					kind = result.KindPolicy
				}
				wantError(t, err, kind, tt.code)
				if tt.field != "" && err.Field != tt.field {
					t.Errorf("Field = %q, want %q", err.Field, tt.field)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() returned error: %v", err)
			}
			files := mustOperation(t, cfg, "files")
			if files.Enabled() != tt.enabled {
				t.Errorf("Enabled() = %t, want %t", files.Enabled(), tt.enabled)
			}
			if got := files.FilesAllowList().Entries(); !slices.Equal(got, tt.paths) {
				t.Errorf("FilesAllowList() = %q, want %q", got, tt.paths)
			}
		})
	}
}

// TestUnsupportedFieldNamesTheKeysTheOperationTakes keeps the allowed list of
// an unsupported_operation_field error true for each kind of operation.
func TestUnsupportedFieldNamesTheKeysTheOperationTakes(t *testing.T) {
	for _, tc := range []struct {
		body    string
		allowed []string
	}{
		{"operations:\n  cpu:\n    max_lines: 10\n", []string{"enabled", "sudo"}},
		{"operations:\n  files:\n    max_since: 1h\n", []string{"enabled", "sudo", "paths"}},
		{"operations:\n  kernel:\n    paths: [/var/log]\n", []string{"enabled", "sudo", "max_lines", "max_since"}},
	} {
		withConfig(t, tc.body)
		_, err := Load()
		wantError(t, err, result.KindValidation, "unsupported_operation_field")
		if !slices.Equal(err.Allowed, tc.allowed) {
			t.Errorf("%q: Allowed = %q, want %q", tc.body, err.Allowed, tc.allowed)
		}
	}
}

// TestFilesAllowListAccessorIsACopy pins that what Config hands out cannot be
// used to change what the next caller gets.
func TestFilesAllowListAccessorIsACopy(t *testing.T) {
	withConfig(t, "operations:\n  files:\n    enabled: true\n    paths:\n      - /var/log\n")
	cfg := mustLoad(t)

	first := mustOperation(t, cfg, "files").FilesAllowList()
	entries := first.Entries()
	entries[0] = "/"
	if _, err := vo.ParseFilesPath("/etc", first); err == nil {
		t.Error("rewriting Entries() widened the list it came from")
	}
	again := mustOperation(t, cfg, "files").FilesAllowList()
	if got := again.Entries(); !slices.Equal(got, []string{"/var/log"}) {
		t.Errorf("the next accessor call returned %q", got)
	}
	if _, err := vo.ParseFilesPath("/var/log/nginx", again); err != nil {
		t.Errorf("the configured entry stopped matching: %v", err)
	}
}

// TestNonFilesOperationsCarryNoList pins that the list belongs to files alone.
func TestNonFilesOperationsCarryNoList(t *testing.T) {
	withConfig(t, "operations:\n  files:\n    enabled: true\n    paths:\n      - /var/log\n")
	cfg := mustLoad(t)
	for _, d := range defaultOperations {
		if d.name == "files" {
			continue
		}
		if got := mustOperation(t, cfg, d.name).FilesAllowList().Entries(); len(got) != 0 {
			t.Errorf("%s carries a files list %q", d.name, got)
		}
	}
}
