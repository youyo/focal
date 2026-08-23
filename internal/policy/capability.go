package policy

import "sort"

// capabilities declares, for every operation Focal knows, the sudo modes an
// administrator is allowed to select. This table is Focal's own code, not
// configuration: a config file can only narrow what is declared here, never
// widen it, so no setting can grant an operation a privilege it was not built
// to need.
//
// The table intentionally lists operations that M1/M2 do not implement yet.
// It is the single place an operation's privilege envelope is declared, and
// adding an operation later means adding its row here first.
var capabilities = map[string][]SudoMode{
	// Read-only inspection of unprivileged system state.
	"system":    {SudoNever},
	"cpu":       {SudoNever},
	"memory":    {SudoNever},
	"storage":   {SudoNever},
	"network":   {SudoNever},
	"processes": {SudoNever},
	"service":   {SudoNever},

	// journalctl may need privilege to read the system journal, so an
	// administrator may opt into the escalate-only-on-denial mode for either
	// journal reader. Starting every read as root (always) is not offered.
	"logs":   {SudoNever, SudoAuto},
	"kernel": {SudoNever, SudoAuto},

	// inspect runs no remote command of its own: it composes the read-only
	// operations above, and each of those is resolved through its own row, so
	// the composite never carries privilege itself.
	"inspect": {SudoNever},
}

// operationNames returns the declared operation names in sorted order, for
// error reporting.
func operationNames() []string {
	names := make([]string, 0, len(capabilities))
	for name := range capabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// modeNames renders modes as their config wire forms, for error reporting.
func modeNames(modes []SudoMode) []string {
	names := make([]string, 0, len(modes))
	for _, m := range modes {
		names = append(names, m.String())
	}
	return names
}
