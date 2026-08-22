// Package policy decides the sudo mode that actually applies to an operation.
// The effective mode is the intersection of the operation's capability (fixed
// in Focal's own code, see capability.go) and the administrator's configured
// policy. A configured mode outside the capability is a startup error, never a
// silent fallback to a weaker mode.
package policy

import "github.com/youyo/focal/internal/result"

// SudoMode says how a command may acquire root privileges.
//
// The zero value is SudoNever so that any Policy or Command that was not built
// through Resolve carries no privilege at all.
type SudoMode uint8

const (
	// SudoNever forbids sudo entirely.
	SudoNever SudoMode = iota
	// SudoAuto runs the command unprivileged first and retries it once with
	// sudo -n only if the unprivileged attempt failed for lack of privilege.
	SudoAuto
	// SudoAlways runs the command with sudo -n from the start.
	SudoAlways
)

// sudoModes is the declarative mapping between SudoMode and its wire form in
// the YAML config. String, ParseSudoMode and the error reporting all read this
// one table, so the directions cannot drift apart. A SudoMode that has no
// entry here is undeclared and is rejected everywhere it is accepted as input.
var sudoModes = []struct {
	mode SudoMode
	name string
}{
	{SudoNever, "never"},
	{SudoAuto, "auto"},
	{SudoAlways, "always"},
}

// String returns the config wire form ("never", "auto", "always").
func (m SudoMode) String() string {
	for _, sm := range sudoModes {
		if sm.mode == m {
			return sm.name
		}
	}
	return "invalid"
}

// sudoModeNames returns the accepted wire forms, for error reporting.
func sudoModeNames() []string {
	names := make([]string, 0, len(sudoModes))
	for _, sm := range sudoModes {
		names = append(names, sm.name)
	}
	return names
}

// ParseSudoMode converts the config wire form to a SudoMode. Matching is
// exact: no case folding and no surrounding whitespace, so a typo in the
// config is reported rather than guessed at. On error it returns SudoNever
// together with a Kind: validation error.
func ParseSudoMode(s string) (SudoMode, *result.Error) {
	for _, sm := range sudoModes {
		if sm.name == s {
			return sm.mode, nil
		}
	}
	return SudoNever, result.ValidationError(
		"invalid_sudo_mode",
		"sudo mode must be one of never, auto, always",
		"sudo",
		sudoModeNames(),
	)
}
