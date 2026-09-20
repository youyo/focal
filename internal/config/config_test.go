package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// isolate points Load at an empty, private config root and home directory so a
// test never reads the developer's real ~/.config/focal/config.yaml. It returns
// the XDG root and the home directory for tests that need to place files.
func isolate(t *testing.T) (xdg, home string) {
	t.Helper()
	xdg = t.TempDir()
	home = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", home)
	return xdg, home
}

// writeConfigAt writes body as the config file under the given config root,
// with the permissions Focal expects of a config file.
func writeConfigAt(t *testing.T, root, body string) string {
	t.Helper()
	dir := filepath.Join(root, "focal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// withConfig isolates the environment and installs body as the config file.
func withConfig(t *testing.T, body string) string {
	t.Helper()
	xdg, _ := isolate(t)
	return writeConfigAt(t, xdg, body)
}

func mustLoad(t *testing.T) Config {
	t.Helper()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	return cfg
}

func wantError(t *testing.T, err *result.Error, kind result.Kind, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s/%s error, got none", kind, code)
	}
	if err.Kind != kind || err.Code != code {
		t.Fatalf("expected %s/%s, got %s/%s (%s)", kind, code, err.Kind, err.Code, err.Message)
	}
}

func mustOperation(t *testing.T, cfg Config, name string) Operation {
	t.Helper()
	op, ok := cfg.Operation(name)
	if !ok {
		t.Fatalf("operation %q missing from config", name)
	}
	return op
}

// TestLoadWithoutConfigFileUsesSafeDefaults pins the behaviour Focal must have
// on a machine that has never been configured: it starts, every read-only
// operation is available, kernel is off, and nothing may use sudo.
func TestLoadWithoutConfigFileUsesSafeDefaults(t *testing.T) {
	isolate(t)
	cfg := mustLoad(t)

	if got := cfg.Timeout(); got != 30*time.Second {
		t.Errorf("Timeout() = %v, want 30s", got)
	}
	if got := cfg.MaxOutput(); got != 10*1024*1024 {
		t.Errorf("MaxOutput() = %d, want 10MiB", got)
	}
	if got := cfg.Port(); got != 0 {
		t.Errorf("Port() = %d, want 0 (delegated to OpenSSH)", got)
	}
	if got := cfg.IdentityFile(); got != "" {
		t.Errorf("IdentityFile() = %q, want empty", got)
	}
	if !cfg.TransportEnabled(vo.TransportSSH) {
		t.Error("TransportEnabled(ssh) = false, want true (the default)")
	}
	if cfg.TransportEnabled(vo.TransportSSM) {
		t.Error("TransportEnabled(ssm) = true, want false: an administrator must opt in")
	}

	readOnly := []string{"system", "cpu", "memory", "storage", "network", "processes", "service", "logs", "inspect"}
	for _, name := range readOnly {
		op := mustOperation(t, cfg, name)
		if !op.Enabled() {
			t.Errorf("operation %q: Enabled() = false, want true", name)
		}
		if got := op.Policy().Sudo(); got != policy.SudoNever {
			t.Errorf("operation %q: Sudo() = %v, want never", name, got)
		}
		if got := op.Policy().Operation(); got != name {
			t.Errorf("operation %q: Policy().Operation() = %q", name, got)
		}
	}

	kernel := mustOperation(t, cfg, "kernel")
	if kernel.Enabled() {
		t.Error("kernel: Enabled() = true, want false")
	}
	if got := kernel.Policy().Sudo(); got != policy.SudoNever {
		t.Errorf("kernel: Sudo() = %v, want never", got)
	}

	// logs and kernel are the two journal readers, and both carry the same
	// default ceilings on how much journal one call may pull.
	for _, name := range []string{"logs", "kernel"} {
		op := mustOperation(t, cfg, name)
		if got := op.MaxLines().String(); got != "1000" {
			t.Errorf("%s MaxLines() = %q, want 1000", name, got)
		}
		if got := op.MaxSince().String(); got != "24h" {
			t.Errorf("%s MaxSince() = %q, want 24h", name, got)
		}
	}

	// Every other operation reads no line stream and carries no such limit.
	for _, name := range []string{"system", "cpu", "memory", "storage", "network", "processes", "service", "inspect"} {
		op := mustOperation(t, cfg, name)
		if got := op.MaxLines(); got != (vo.LineLimit{}) {
			t.Errorf("%s MaxLines() = %q, want the zero value", name, got.String())
		}
		if got := op.MaxSince(); got != (vo.Duration{}) {
			t.Errorf("%s MaxSince() = %q, want the zero value", name, got.String())
		}
	}
}

// TestLoadWithoutConfigFileOffersEveryDeclaredOperation is the drift check on
// the row count: an unconfigured Focal offers exactly the ten operations it
// implements, no more and no fewer.
func TestLoadWithoutConfigFileOffersEveryDeclaredOperation(t *testing.T) {
	isolate(t)
	cfg := mustLoad(t)

	want := []string{
		"system", "cpu", "memory", "storage", "network",
		"processes", "service", "logs", "kernel", "inspect",
	}
	if got := len(cfg.operations); got != len(want) {
		t.Errorf("Load() returned %d operations, want %d", got, len(want))
	}
	for _, name := range want {
		if _, ok := cfg.Operation(name); !ok {
			t.Errorf("Operation(%q) missing from a default config", name)
		}
	}
	if _, ok := cfg.Operation("shell"); ok {
		t.Error("Operation(\"shell\") reported present; only declared operations may exist")
	}
}

// TestLoadRejectsSudoAlwaysForEveryOperation carries issue #11's startup rule
// through the administrator-facing path: writing `sudo: always` under any of
// the ten operations stops startup rather than quietly running unprivileged.
func TestLoadRejectsSudoAlwaysForEveryOperation(t *testing.T) {
	for _, d := range defaultOperations {
		t.Run(d.name, func(t *testing.T) {
			withConfig(t, "operations:\n  "+d.name+":\n    sudo: always\n")

			_, err := Load()
			wantError(t, err, result.KindPolicy, "sudo_not_allowed")
			if err.Field != "operations."+d.name+".sudo" {
				t.Errorf("Field = %q, want operations.%s.sudo", err.Field, d.name)
			}
		})
	}
}

// TestLoadAcceptsSudoAutoForJournalOperationsOnly pins the other half of the
// capability table as an administrator experiences it.
func TestLoadAcceptsSudoAutoForJournalOperationsOnly(t *testing.T) {
	journal := []string{"logs", "kernel"}
	for _, d := range defaultOperations {
		t.Run(d.name, func(t *testing.T) {
			withConfig(t, "operations:\n  "+d.name+":\n    sudo: auto\n")

			cfg, err := Load()
			if !slices.Contains(journal, d.name) {
				wantError(t, err, result.KindPolicy, "sudo_not_allowed")
				return
			}
			if err != nil {
				t.Fatalf("Load() returned error: %v", err)
			}
			if got := mustOperation(t, cfg, d.name).Policy().Sudo(); got != policy.SudoAuto {
				t.Errorf("%s Sudo() = %v, want auto", d.name, got)
			}
		})
	}
}

// TestLoadValidTestdata checks the fully populated example decodes and that
// every value reaches the accessor a caller reads.
func TestLoadValidTestdata(t *testing.T) {
	withConfig(t, readTestdata(t, "valid.yaml"))
	cfg := mustLoad(t)

	if got := cfg.Timeout(); got != 45*time.Second {
		t.Errorf("Timeout() = %v, want 45s", got)
	}
	if got := cfg.MaxOutput(); got != 4*1024*1024 {
		t.Errorf("MaxOutput() = %d, want 4MiB", got)
	}
	if got := cfg.Port(); got != 2222 {
		t.Errorf("Port() = %d, want 2222", got)
	}
	if op := mustOperation(t, cfg, "cpu"); op.Enabled() {
		t.Error("cpu: Enabled() = true, want false (disabled by the file)")
	}
	if op := mustOperation(t, cfg, "kernel"); !op.Enabled() {
		t.Error("kernel: Enabled() = false, want true (enabled by the file)")
	}
	// memory is absent from the file and keeps its default.
	if op := mustOperation(t, cfg, "memory"); !op.Enabled() {
		t.Error("memory: Enabled() = false, want the default true")
	}

	if op := mustOperation(t, cfg, "inspect"); op.Enabled() {
		t.Error("inspect: Enabled() = true, want false (disabled by the file)")
	}

	logs := mustOperation(t, cfg, "logs")
	if got := logs.Policy().Sudo(); got != policy.SudoAuto {
		t.Errorf("logs Sudo() = %v, want auto", got)
	}
	if got := logs.MaxLines().String(); got != "500" {
		t.Errorf("logs MaxLines() = %q, want 500", got)
	}
	if got := logs.MaxSince().String(); got != "12h" {
		t.Errorf("logs MaxSince() = %q, want 12h", got)
	}

	kernel := mustOperation(t, cfg, "kernel")
	if got := kernel.Policy().Sudo(); got != policy.SudoAuto {
		t.Errorf("kernel Sudo() = %v, want auto", got)
	}
	if got := kernel.MaxLines().String(); got != "300" {
		t.Errorf("kernel MaxLines() = %q, want 300", got)
	}
	if got := kernel.MaxSince().String(); got != "6h" {
		t.Errorf("kernel MaxSince() = %q, want 6h", got)
	}
}

// TestLoadCapabilityViolationTestdata is the central guarantee of Issue #3: a
// config file cannot widen an operation's privilege envelope, and the mismatch
// is a startup error rather than a silent downgrade.
func TestLoadCapabilityViolationTestdata(t *testing.T) {
	withConfig(t, readTestdata(t, "capability_violation.yaml"))

	_, err := Load()
	wantError(t, err, result.KindPolicy, "sudo_not_allowed")
	if err.Field != "operations.logs.sudo" {
		t.Errorf("Field = %q, want operations.logs.sudo", err.Field)
	}
	if len(err.Allowed) == 0 {
		t.Error("Allowed is empty; the error must say which modes logs accepts")
	}
}

// TestLoadUnknownFieldTestdata pins strict decoding: a misspelled key is
// rejected instead of leaving the default silently in place.
func TestLoadUnknownFieldTestdata(t *testing.T) {
	withConfig(t, readTestdata(t, "unknown_field.yaml"))

	_, err := Load()
	wantError(t, err, result.KindValidation, "invalid_yaml")
	if !strings.Contains(err.Message, "max_outputs") {
		t.Errorf("Message = %q, want it to name the unknown field", err.Message)
	}
	// The message must not carry the offending lines: it is forwarded as a
	// single-line JSON error, and config content does not belong in it.
	if strings.Contains(err.Message, "\n") {
		t.Errorf("Message = %q, want a single line without the YAML source", err.Message)
	}
}

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	// #nosec G304 -- name is a fixture literal from this file.
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	return string(b)
}

// TestLoadUnknownOperationName rejects operations Focal does not implement,
// so a stale or invented name cannot look configured.
func TestLoadUnknownOperationName(t *testing.T) {
	withConfig(t, "operations:\n  shell:\n    enabled: true\n")

	_, err := Load()
	wantError(t, err, result.KindPolicy, "unknown_operation")
}

// TestConfigPathHonoursXDG covers the three location cases, including the one
// that must not fall back: XDG_CONFIG_HOME set but holding no config file.
func TestConfigPathHonoursXDG(t *testing.T) {
	const homeBody = "execution:\n  timeout: 90s\n"

	t.Run("xdg unset falls back to home", func(t *testing.T) {
		_, home := isolate(t)
		os.Unsetenv("XDG_CONFIG_HOME")
		writeConfigAt(t, filepath.Join(home, ".config"), homeBody)

		if got := mustLoad(t).Timeout(); got != 90*time.Second {
			t.Errorf("Timeout() = %v, want the ~/.config file's 90s", got)
		}
	})

	t.Run("xdg empty falls back to home", func(t *testing.T) {
		_, home := isolate(t)
		t.Setenv("XDG_CONFIG_HOME", "")
		writeConfigAt(t, filepath.Join(home, ".config"), homeBody)

		if got := mustLoad(t).Timeout(); got != 90*time.Second {
			t.Errorf("Timeout() = %v, want the ~/.config file's 90s", got)
		}
	})

	t.Run("xdg set does not fall back to home", func(t *testing.T) {
		_, home := isolate(t)
		writeConfigAt(t, filepath.Join(home, ".config"), homeBody)

		if got := mustLoad(t).Timeout(); got != 30*time.Second {
			t.Errorf("Timeout() = %v, want the 30s default; ~/.config must not be consulted", got)
		}
	})
}

// TestLoadRejectsWritableConfigFile refuses a config file other local users can
// rewrite, since that file decides which operations and sudo modes are allowed.
func TestLoadRejectsWritableConfigFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o620, 0o602, 0o666} {
		path := withConfig(t, "execution:\n  timeout: 30s\n")
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}

		_, err := Load()
		wantError(t, err, result.KindValidation, "insecure_config_permissions")
	}
}

// TestLoadRejectsOversizedConfigFile bounds the input before the YAML parser
// sees it, so anchor/alias expansion cannot be fed a huge document.
func TestLoadRejectsOversizedConfigFile(t *testing.T) {
	body := "# " + strings.Repeat("x", maxConfigBytes) + "\n"
	withConfig(t, body)

	_, err := Load()
	wantError(t, err, result.KindValidation, "config_too_large")
}

// TestLoadFromReadsTheGivenPathInsteadOfTheDefaultLocation pins the one thing
// that differs from Load: where the file is found. Everything the default
// location's file would trigger, an explicit path triggers identically.
func TestLoadFromReadsTheGivenPathInsteadOfTheDefaultLocation(t *testing.T) {
	isolate(t) // no file at the default location at all

	dir := t.TempDir()
	path := filepath.Join(dir, "explicit.yaml")
	if err := os.WriteFile(path, []byte("operations:\n  kernel:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() returned error: %v", err)
	}
	op := mustOperation(t, cfg, "kernel")
	if !op.Enabled() {
		t.Fatal("kernel is not enabled; LoadFrom did not read the given path")
	}
}

// TestLoadFromWithNoFileAtThePathIsAStructuredError pins that an explicitly
// named path is a promise: if nothing is there, that is a mistake worth
// surfacing (most often a typo) rather than a silent fall-back to defaults.
// This is unlike Load, whose default-location lookup treats "nothing here" as
// the normal, unconfigured case.
func TestLoadFromWithNoFileAtThePathIsAStructuredError(t *testing.T) {
	isolate(t)

	path := filepath.Join(t.TempDir(), "missing.yaml")
	_, err := LoadFrom(path)
	wantError(t, err, result.KindValidation, "config_not_found")
	if !strings.Contains(err.Message, path) {
		t.Errorf("error message %q does not mention the missing path %q", err.Message, path)
	}
}

// TestLoadFromAppliesTheSameChecksAsLoad pins that a bad path (a directory,
// not a file) and invalid YAML fail LoadFrom exactly as they would fail Load
// against the default location.
func TestLoadFromAppliesTheSameChecksAsLoad(t *testing.T) {
	isolate(t)

	t.Run("not a regular file", func(t *testing.T) {
		_, err := LoadFrom(t.TempDir())
		wantError(t, err, result.KindValidation, "unreadable_config")
	})

	t.Run("invalid yaml", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.yaml")
		if err := os.WriteFile(path, []byte("operations:\n  kernel: [not-a-map]\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := LoadFrom(path)
		wantError(t, err, result.KindValidation, "invalid_yaml")
	})

	t.Run("writable by others", func(t *testing.T) {
		path := writeConfigAt(t, t.TempDir(), "execution:\n  timeout: 30s\n")
		mode := os.FileMode(0o666)
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		_, err := LoadFrom(path)
		wantError(t, err, result.KindValidation, "insecure_config_permissions")
	})
}

// TestIdentityFilePermissions accepts a private key only when it is private,
// and expands a leading ~/ the way OpenSSH does so the check sees the real file.
func TestIdentityFilePermissions(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
		code string
	}{
		{name: "owner only", mode: 0o600},
		{name: "group readable", mode: 0o640, code: "insecure_identity_file_permissions"},
		{name: "world readable", mode: 0o604, code: "insecure_identity_file_permissions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xdg, home := isolate(t)
			key := filepath.Join(home, "id_ed25519")
			if err := os.WriteFile(key, []byte("key"), tt.mode); err != nil {
				t.Fatalf("write key: %v", err)
			}
			writeConfigAt(t, xdg, "execution:\n  identity_file: \"~/id_ed25519\"\n")

			cfg, err := Load()
			if tt.code != "" {
				wantError(t, err, result.KindValidation, tt.code)
				return
			}
			if err != nil {
				t.Fatalf("Load() returned error: %v", err)
			}
			if cfg.IdentityFile() != key {
				t.Errorf("IdentityFile() = %q, want the expanded %q", cfg.IdentityFile(), key)
			}
		})
	}
}

func TestIdentityFileMustExist(t *testing.T) {
	xdg, home := isolate(t)
	writeConfigAt(t, xdg, "execution:\n  identity_file: \""+filepath.Join(home, "absent")+"\"\n")

	_, err := Load()
	wantError(t, err, result.KindValidation, "unreadable_identity_file")
}

// TestExecutionValueRanges is the table of bounds an administrator can trip.
func TestExecutionValueRanges(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		code  string
		field string
	}{
		{name: "timeout at lower bound", body: "execution:\n  timeout: 1s\n"},
		{name: "timeout at upper bound", body: "execution:\n  timeout: 10m\n"},
		{name: "timeout too small", body: "execution:\n  timeout: 999ms\n", code: "timeout_out_of_range", field: "execution.timeout"},
		{name: "timeout too large", body: "execution:\n  timeout: 11m\n", code: "timeout_out_of_range", field: "execution.timeout"},
		{name: "timeout negative", body: "execution:\n  timeout: -5s\n", code: "timeout_out_of_range", field: "execution.timeout"},
		{name: "timeout unparsable", body: "execution:\n  timeout: soon\n", code: "invalid_timeout", field: "execution.timeout"},
		{name: "max_output at lower bound", body: "execution:\n  max_output: 1KiB\n"},
		{name: "max_output at upper bound", body: "execution:\n  max_output: 1GiB\n"},
		{name: "max_output too small", body: "execution:\n  max_output: 1023B\n", code: "max_output_out_of_range", field: "execution.max_output"},
		{name: "max_output too large", body: "execution:\n  max_output: 2GiB\n", code: "max_output_out_of_range", field: "execution.max_output"},
		{name: "max_output unparsable", body: "execution:\n  max_output: plenty\n", code: "invalid_byte_size", field: "execution.max_output"},
		{name: "port at lower bound", body: "execution:\n  port: 1\n"},
		{name: "port at upper bound", body: "execution:\n  port: 65535\n"},
		{name: "port zero", body: "execution:\n  port: 0\n", code: "port_out_of_range", field: "execution.port"},
		{name: "port too large", body: "execution:\n  port: 65536\n", code: "port_out_of_range", field: "execution.port"},
		{name: "port negative", body: "execution:\n  port: -1\n", code: "port_out_of_range", field: "execution.port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.body)

			_, err := Load()
			if tt.code == "" {
				if err != nil {
					t.Fatalf("Load() returned error: %v", err)
				}
				return
			}
			wantError(t, err, result.KindValidation, tt.code)
			if err.Field != tt.field {
				t.Errorf("Field = %q, want %q", err.Field, tt.field)
			}
		})
	}
}

// TestExecutionTransports covers execution.transports: which sets are
// accepted, which are refused, and that TransportEnabled reports exactly the
// set the file named.
func TestExecutionTransports(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		code  string
		field string
		want  []vo.Transport // checked only when code == ""
	}{
		{
			name: "absent keeps the ssh-only default",
			body: "",
			want: []vo.Transport{vo.TransportSSH},
		},
		{
			name: "ssh alone, written explicitly",
			body: "execution:\n  transports: [ssh]\n",
			want: []vo.Transport{vo.TransportSSH},
		},
		{
			name: "ssh and ssm both opted in",
			body: "execution:\n  transports: [ssh, ssm]\n",
			want: []vo.Transport{vo.TransportSSH, vo.TransportSSM},
		},
		{
			name: "ssm alone, without ssh",
			body: "execution:\n  transports: [ssm]\n",
			want: []vo.Transport{vo.TransportSSM},
		},
		{
			name:  "empty list is rejected rather than disabling every transport",
			body:  "execution:\n  transports: []\n",
			code:  "empty_transports",
			field: "execution.transports",
		},
		{
			name:  "unknown value is rejected",
			body:  "execution:\n  transports: [ssh, telnet]\n",
			code:  "invalid_transport",
			field: "execution.transports",
		},
		{
			name:  "duplicate value is rejected",
			body:  "execution:\n  transports: [ssh, ssh]\n",
			code:  "duplicate_transport",
			field: "execution.transports",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.body)
			cfg, err := Load()
			if tt.code == "" {
				if err != nil {
					t.Fatalf("Load() returned error: %v", err)
				}
				for _, transport := range []vo.Transport{vo.TransportSSH, vo.TransportSSM} {
					want := slices.Contains(tt.want, transport)
					if got := cfg.TransportEnabled(transport); got != want {
						t.Errorf("TransportEnabled(%s) = %t, want %t", transport, got, want)
					}
				}
				return
			}
			wantError(t, err, result.KindValidation, tt.code)
			if err.Field != tt.field {
				t.Errorf("Field = %q, want %q", err.Field, tt.field)
			}
		})
	}
}

func TestLoadTransportsValidTestdata(t *testing.T) {
	withConfig(t, readTestdata(t, "transports_valid.yaml"))
	cfg := mustLoad(t)
	if !cfg.TransportEnabled(vo.TransportSSH) || !cfg.TransportEnabled(vo.TransportSSM) {
		t.Errorf("transports_valid.yaml should enable both ssh and ssm")
	}
}

func TestLoadTransportsInvalidTestdata(t *testing.T) {
	withConfig(t, readTestdata(t, "transports_invalid.yaml"))
	_, err := Load()
	wantError(t, err, result.KindValidation, "invalid_transport")
	if err.Field != "execution.transports" {
		t.Errorf("Field = %q, want execution.transports", err.Field)
	}
}

// TestOperationValueValidation covers the per-operation keys, including the
// two that are delegated to the value objects.
func TestOperationValueValidation(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		code  string
		field string
	}{
		{name: "logs auto is within capability", body: "operations:\n  logs:\n    sudo: auto\n"},
		{name: "kernel always is outside capability", body: "operations:\n  kernel:\n    sudo: always\n", code: "sudo_not_allowed", field: "operations.kernel.sudo"},
		{name: "unparsable sudo mode", body: "operations:\n  logs:\n    sudo: Auto\n", code: "invalid_sudo_mode", field: "operations.logs.sudo"},
		{name: "max_lines above the value object bound", body: "operations:\n  logs:\n    max_lines: 100001\n", code: "invalid_line_limit", field: "operations.logs.max_lines"},
		{name: "max_lines zero", body: "operations:\n  logs:\n    max_lines: 0\n", code: "invalid_line_limit", field: "operations.logs.max_lines"},
		{name: "max_since above the value object bound", body: "operations:\n  logs:\n    max_since: 31d\n", code: "invalid_duration", field: "operations.logs.max_since"},
		{name: "max_since in Go syntax the value object rejects", body: "operations:\n  logs:\n    max_since: 1h30m\n", code: "invalid_duration", field: "operations.logs.max_since"},
		{name: "kernel max_lines is accepted", body: "operations:\n  kernel:\n    max_lines: 300\n"},
		{name: "kernel max_since is accepted", body: "operations:\n  kernel:\n    max_since: 6h\n"},
		{name: "kernel max_lines above the value object bound", body: "operations:\n  kernel:\n    max_lines: 100001\n", code: "invalid_line_limit", field: "operations.kernel.max_lines"},
		{name: "kernel max_since above the value object bound", body: "operations:\n  kernel:\n    max_since: 31d\n", code: "invalid_duration", field: "operations.kernel.max_since"},
		{name: "log limits on a non-log operation", body: "operations:\n  cpu:\n    max_lines: 10\n", code: "unsupported_operation_field", field: "operations.cpu.max_lines"},
		{name: "max_since on a non-log operation", body: "operations:\n  system:\n    max_since: 1h\n", code: "unsupported_operation_field", field: "operations.system.max_since"},
		{name: "log limits on the composite operation", body: "operations:\n  inspect:\n    max_lines: 10\n", code: "unsupported_operation_field", field: "operations.inspect.max_lines"},
		{name: "log limits on service", body: "operations:\n  service:\n    max_since: 2h\n", code: "unsupported_operation_field", field: "operations.service.max_since"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.body)

			_, err := Load()
			if tt.code == "" {
				if err != nil {
					t.Fatalf("Load() returned error: %v", err)
				}
				return
			}
			if tt.code == "sudo_not_allowed" {
				wantError(t, err, result.KindPolicy, tt.code)
			} else {
				wantError(t, err, result.KindValidation, tt.code)
			}
			if err.Field != tt.field {
				t.Errorf("Field = %q, want %q", err.Field, tt.field)
			}
		})
	}
}

// TestLoadEmptyConfigFile treats a file with no content as no overrides.
func TestLoadEmptyConfigFile(t *testing.T) {
	withConfig(t, "")

	if got := mustLoad(t).Timeout(); got != 30*time.Second {
		t.Errorf("Timeout() = %v, want the 30s default", got)
	}
}

// TestLoadRejectsMalformedYAML keeps a syntax error a startup error.
func TestLoadRejectsMalformedYAML(t *testing.T) {
	withConfig(t, "execution: [unclosed\n")

	_, err := Load()
	wantError(t, err, result.KindValidation, "invalid_yaml")
}

// TestOperationZeroValue documents that an unconfigured Operation carries no
// privilege, matching the fail-safe zero value of policy.Policy.
func TestOperationZeroValue(t *testing.T) {
	var op Operation
	if op.Enabled() {
		t.Error("zero Operation is enabled")
	}
	if got := op.Policy().Sudo(); got != policy.SudoNever {
		t.Errorf("zero Operation Sudo() = %v, want never", got)
	}
}

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		bad  bool
	}{
		{in: "1024", want: 1024},
		{in: "512B", want: 512},
		{in: "1KiB", want: 1024},
		{in: "10MiB", want: 10 * 1024 * 1024},
		{in: "1GiB", want: 1024 * 1024 * 1024},
		{in: "", bad: true},
		{in: "B", bad: true},
		{in: "0", bad: true},
		{in: "010MiB", bad: true},
		{in: "10 MiB", bad: true},
		{in: "10mib", bad: true},
		{in: "10MB", bad: true},
		{in: "-1KiB", bad: true},
		{in: "1.5MiB", bad: true},
		{in: "99999999999999999999GiB", bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseByteSize(tt.in, "execution.max_output")
			if tt.bad {
				if err == nil {
					t.Fatalf("parseByteSize(%q) = %d, want an error", tt.in, got)
				}
				if err.Kind != result.KindValidation {
					t.Errorf("Kind = %s, want validation", err.Kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseByteSize(%q) returned error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseByteSize(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestDefaultOperationsMatchPolicy fails if the default table names an
// operation policy does not declare, which is the drift this package cannot
// detect at runtime.
func TestDefaultOperationsMatchPolicy(t *testing.T) {
	for _, d := range defaultOperations {
		if _, err := policy.Resolve(d.name, policy.SudoNever); err != nil {
			t.Errorf("default operation %q is not declared in internal/policy: %v", d.name, err)
		}
	}
}
