// Package config loads Focal's YAML configuration and validates it at startup.
//
// The config file decides only *what is allowed*: which operations exist for
// this installation and, within the envelope internal/policy declares for each
// one, which sudo mode applies. How an operation is carried out — the actual
// remote command — is fixed in Focal's own code and cannot be influenced from
// here. Anything the file asks for that Focal cannot grant is a startup error,
// never a silent downgrade to something weaker, so an administrator learns that
// a setting did not take effect before relying on it.
//
// With no config file at all, Load returns the safe-side defaults: every
// read-only inspection enabled, kernel details disabled, and sudo nowhere.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// Config is a validated configuration. Its fields are unexported and Load is
// the only constructor, so a Config in hand has already passed every bound and
// capability check. The zero value is not usable.
type Config struct {
	timeout      time.Duration
	maxOutput    int64
	port         int
	identityFile string
	operations   map[string]Operation
}

// Timeout is the ceiling on a single remote command.
func (c Config) Timeout() time.Duration { return c.timeout }

// MaxOutput is the byte ceiling on what one command may return.
func (c Config) MaxOutput() int64 { return c.maxOutput }

// Port is the SSH port, or 0 when the choice is left to OpenSSH.
func (c Config) Port() int { return c.port }

// IdentityFile is the private key path with a leading ~/ already expanded, or
// the empty string when the choice is left to OpenSSH.
func (c Config) IdentityFile() string { return c.identityFile }

// Operation returns the settings for one operation. The second result is false
// for a name Focal does not implement.
func (c Config) Operation(name string) (Operation, bool) {
	op, ok := c.operations[name]
	return op, ok
}

// Operation is one operation's validated settings. Its Policy comes from
// policy.Resolve, so it can only carry a sudo mode the operation's capability
// approved. The zero value is disabled and powerless.
type Operation struct {
	enabled  bool
	policy   policy.Policy
	maxLines vo.LineLimit
	maxSince vo.Duration
}

// Enabled reports whether this installation offers the operation at all.
func (o Operation) Enabled() bool { return o.enabled }

// Policy is the resolved capability-and-configuration intersection.
func (o Operation) Policy() policy.Policy { return o.policy }

// MaxLines caps how many lines the operation may return. It is the zero value
// for operations that do not read a line stream.
func (o Operation) MaxLines() vo.LineLimit { return o.maxLines }

// MaxSince caps how far back the operation may look. It is the zero value for
// operations that do not read a line stream.
func (o Operation) MaxSince() vo.Duration { return o.maxSince }

// rawConfig mirrors the YAML document. Every field is a pointer so that
// "absent" and "set to the zero value" stay distinguishable: a file that omits
// a key keeps the default, and a file that writes `port: 0` is rejected.
type rawConfig struct {
	Execution  *rawExecution            `yaml:"execution"`
	Operations map[string]*rawOperation `yaml:"operations"`
}

type rawExecution struct {
	Timeout      *string `yaml:"timeout"`
	MaxOutput    *string `yaml:"max_output"`
	Port         *int    `yaml:"port"`
	IdentityFile *string `yaml:"identity_file"`
}

type rawOperation struct {
	Enabled  *bool   `yaml:"enabled"`
	Sudo     *string `yaml:"sudo"`
	MaxLines *int    `yaml:"max_lines"`
	MaxSince *string `yaml:"max_since"`
}

// Load reads the config file and validates it. A missing file is not an error:
// Focal runs on the safe-side defaults. Everything else that is wrong with the
// file — a key Focal does not know, a value out of range, a sudo mode outside
// an operation's capability, permissions that let another local user rewrite
// it — stops startup with a structured error.
func Load() (Config, *result.Error) {
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}
	return loadFrom(path, false)
}

// LoadFrom reads and validates the config file at an explicitly named path,
// such as --config. Every check besides how the path was found — size,
// permissions, YAML well-formedness, the operation and capability checks — is
// identical to Load. Unlike Load's default-location lookup, a path named here
// is a promise: nothing at it is a config_not_found error rather than a silent
// fall-back to defaults, so a typo in --config is reported instead of running
// on an unintended policy.
func LoadFrom(path string) (Config, *result.Error) {
	return loadFrom(path, true)
}

func loadFrom(path string, requireExists bool) (Config, *result.Error) {
	data, err := readConfigFile(path, requireExists)
	if err != nil {
		return Config{}, err
	}
	var raw rawConfig
	if decErr := decode(data, &raw); decErr != nil {
		return Config{}, decErr
	}
	return build(raw)
}

// configPath follows the XDG base directory spec. XDG_CONFIG_HOME is honoured
// exactly: when it is set, that is the only place Focal looks, because falling
// back to ~/.config would let a file the administrator meant to override take
// effect anyway.
func configPath() (string, *result.Error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "focal", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", result.ValidationError(
			"no_home_directory",
			"cannot locate the home directory holding ~/.config/focal/config.yaml: "+err.Error(),
			"", nil,
		)
	}
	return filepath.Join(home, ".config", "focal", "config.yaml"), nil
}

// readConfigFile returns nil, nil when there is no config file to read and
// requireExists is false. When requireExists is true — an explicitly named
// path such as --config — a missing file is instead a config_not_found error,
// since a path the caller typed is a promise that something is there.
func readConfigFile(path string, requireExists bool) ([]byte, *result.Error) {
	// The file is opened before it is judged, and judged through that open
	// descriptor. Stat-then-read is two separate resolutions of the same
	// path, and what was measured and permission-checked need not be what is
	// read if anything can move at the path in between; a descriptor refers
	// to one file for as long as it is held.
	//
	// #nosec G304 -- path is Focal's own config location, derived from
	// XDG_CONFIG_HOME or the home directory, not from a request.
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		if requireExists {
			return nil, result.ValidationError(
				"config_not_found",
				fmt.Sprintf("no config file at %s", path),
				"", nil,
			)
		}
		return nil, nil
	}
	if err != nil {
		return nil, result.ValidationError("unreadable_config", "cannot read the config file: "+err.Error(), "", nil)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, result.ValidationError("unreadable_config", "cannot read the config file: "+err.Error(), "", nil)
	}
	if !info.Mode().IsRegular() {
		return nil, result.ValidationError("unreadable_config", fmt.Sprintf("%s is not a regular file", path), "", nil)
	}
	if info.Size() > maxConfigBytes {
		return nil, result.ValidationError(
			"config_too_large",
			fmt.Sprintf("config file is larger than %d bytes", maxConfigBytes),
			"", nil,
		)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return nil, result.ValidationError(
			"insecure_config_permissions",
			fmt.Sprintf("config file mode %#o is writable by group or other", perm),
			"", []string{"0600", "0644"},
		)
	}
	// A mode of 0644 says only that the owner alone may write it. Whose file
	// it is has to be asked separately: a config placed by another user in a
	// directory focal can reach is theirs to change, and it decides which
	// operations are enabled, whether they escalate with sudo, and which key
	// focal connects with.
	if permErr := checkConfigOwner(path, info); permErr != nil {
		return nil, permErr
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes))
	if err != nil {
		return nil, result.ValidationError("unreadable_config", "cannot read the config file: "+err.Error(), "", nil)
	}
	return data, nil
}

// decode parses the document with unknown fields rejected. An absent or empty
// file leaves raw at its zero value, which means "no overrides".
func decode(data []byte, raw *rawConfig) *result.Error {
	if len(data) == 0 {
		return nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data), yaml.DisallowUnknownField())
	if err := dec.Decode(raw); err != nil && !errors.Is(err, io.EOF) {
		// FormatError without the source snippet keeps the message to the
		// position and the reason. Echoing the offending lines back would
		// put config file content into an error a caller may forward.
		return result.ValidationError("invalid_yaml", strings.TrimSpace(yaml.FormatError(err, false, false)), "", nil)
	}
	return nil
}

// build layers the file's overrides onto the safe-side defaults.
func build(raw rawConfig) (Config, *result.Error) {
	cfg := Config{
		timeout:    defaultTimeout,
		maxOutput:  defaultMaxOutput,
		operations: make(map[string]Operation, len(defaultOperations)),
	}
	if err := cfg.applyExecution(raw.Execution); err != nil {
		return Config{}, err
	}
	for _, d := range defaultOperations {
		op, err := buildOperation(d.name, d.enabled, nil)
		if err != nil {
			return Config{}, err
		}
		cfg.operations[d.name] = op
	}
	// Sorted so that a file with several problems always reports the same
	// one first.
	for _, name := range slices.Sorted(maps.Keys(raw.Operations)) {
		op, err := buildOperation(name, cfg.operations[name].enabled, raw.Operations[name])
		if err != nil {
			return Config{}, err
		}
		cfg.operations[name] = op
	}
	return cfg, nil
}

func (c *Config) applyExecution(raw *rawExecution) *result.Error {
	if raw == nil {
		return nil
	}
	if raw.Timeout != nil {
		d, err := time.ParseDuration(*raw.Timeout)
		if err != nil {
			return result.ValidationError(
				"invalid_timeout",
				"timeout must be a duration such as 30s or 2m",
				"execution.timeout",
				[]string{"a Go duration between 1s and 10m"},
			)
		}
		if d < minTimeout || d > maxTimeout {
			return result.ValidationError(
				"timeout_out_of_range",
				fmt.Sprintf("timeout must be between %s and %s", minTimeout, maxTimeout),
				"execution.timeout", nil,
			)
		}
		c.timeout = d
	}
	if raw.MaxOutput != nil {
		n, err := parseByteSize(*raw.MaxOutput, "execution.max_output")
		if err != nil {
			return err
		}
		if n < minMaxOutput || n > maxMaxOutput {
			return result.ValidationError(
				"max_output_out_of_range",
				"max_output must be between 1KiB and 1GiB",
				"execution.max_output", nil,
			)
		}
		c.maxOutput = n
	}
	if raw.Port != nil {
		if *raw.Port < minPort || *raw.Port > maxPort {
			return result.ValidationError(
				"port_out_of_range",
				fmt.Sprintf("port must be between %d and %d", minPort, maxPort),
				"execution.port", nil,
			)
		}
		c.port = *raw.Port
	}
	if raw.IdentityFile != nil {
		path, err := resolveIdentityFile(*raw.IdentityFile)
		if err != nil {
			return err
		}
		c.identityFile = path
	}
	return nil
}

// resolveIdentityFile expands a leading ~/ the way OpenSSH does and refuses a
// key other local users can read.
func resolveIdentityFile(raw string) (string, *result.Error) {
	path := raw
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", result.ValidationError(
				"no_home_directory",
				"cannot expand ~ in the identity file path: "+err.Error(),
				"execution.identity_file", nil,
			)
		}
		path = filepath.Join(home, rest)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", result.ValidationError(
			"unreadable_identity_file",
			"cannot read the identity file: "+err.Error(),
			"execution.identity_file", nil,
		)
	}
	if !info.Mode().IsRegular() {
		return "", result.ValidationError(
			"unreadable_identity_file",
			"identity file is not a regular file",
			"execution.identity_file", nil,
		)
	}
	if perm := info.Mode().Perm(); perm&0o044 != 0 {
		return "", result.ValidationError(
			"insecure_identity_file_permissions",
			fmt.Sprintf("identity file mode %#o is readable by group or other", perm),
			"execution.identity_file",
			[]string{"0600", "0400"},
		)
	}
	return path, nil
}

// buildOperation resolves one operation. The sudo mode always goes through
// policy.Resolve, including for an operation the file does not mention, so a
// name outside the capability table is reported here rather than reaching the
// SSH layer.
func buildOperation(name string, enabled bool, raw *rawOperation) (Operation, *result.Error) {
	op := Operation{enabled: enabled}
	if limits, ok := lineStreamLimits[name]; ok {
		var err *result.Error
		if op.maxLines, err = parseMaxLines(name, limits.maxLines); err != nil {
			return Operation{}, err
		}
		if op.maxSince, err = parseMaxSince(name, limits.maxSince); err != nil {
			return Operation{}, err
		}
	}
	configured := policy.SudoNever
	if raw != nil {
		if raw.Enabled != nil {
			op.enabled = *raw.Enabled
		}
		if raw.Sudo != nil {
			mode, err := policy.ParseSudoMode(*raw.Sudo)
			if err != nil {
				return Operation{}, atField(err, opField(name, "sudo"))
			}
			configured = mode
		}
		if err := applyLogLimits(&op, name, raw); err != nil {
			return Operation{}, err
		}
	}
	resolved, err := policy.Resolve(name, configured)
	if err != nil {
		return Operation{}, err
	}
	op.policy = resolved
	return op, nil
}

// applyLogLimits refuses log limits on an operation that reads no line stream,
// so a setting placed under the wrong key is corrected rather than ignored.
func applyLogLimits(op *Operation, name string, raw *rawOperation) *result.Error {
	if _, ok := lineStreamLimits[name]; !ok {
		for _, unsupported := range []struct {
			key string
			set bool
		}{
			{"max_lines", raw.MaxLines != nil},
			{"max_since", raw.MaxSince != nil},
		} {
			if unsupported.set {
				return result.ValidationError(
					"unsupported_operation_field",
					fmt.Sprintf("operation %q takes no %s", name, unsupported.key),
					opField(name, unsupported.key),
					[]string{"enabled", "sudo"},
				)
			}
		}
		return nil
	}
	var err *result.Error
	if raw.MaxLines != nil {
		if op.maxLines, err = parseMaxLines(name, strconv.Itoa(*raw.MaxLines)); err != nil {
			return err
		}
	}
	if raw.MaxSince != nil {
		if op.maxSince, err = parseMaxSince(name, *raw.MaxSince); err != nil {
			return err
		}
	}
	return nil
}

func parseMaxLines(name, v string) (vo.LineLimit, *result.Error) {
	limit, err := vo.ParseLineLimit(v)
	if err != nil {
		return vo.LineLimit{}, atField(err, opField(name, "max_lines"))
	}
	return limit, nil
}

func parseMaxSince(name, v string) (vo.Duration, *result.Error) {
	since, err := vo.ParseDuration(v)
	if err != nil {
		return vo.Duration{}, atField(err, opField(name, "max_since"))
	}
	return since, nil
}

func opField(name, key string) string { return "operations." + name + "." + key }

// atField restates an error from a value object against the config key that
// carried the value, so an administrator is pointed at the line to edit rather
// than at the internal name of the type that rejected it.
func atField(err *result.Error, field string) *result.Error {
	return &result.Error{
		Kind:    err.Kind,
		Code:    err.Code,
		Message: err.Message,
		Field:   field,
		Allowed: err.Allowed,
	}
}
