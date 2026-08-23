package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/youyo/focal/internal/operation"
)

// This file is the CLI's whole knowledge of what an operation takes. Every
// operation-specific input — the positional SERVICE of `focal HOST logs
// nginx`, the --since of `--since 30m` — is declared as a row here and read by
// one generic parser, so root.go dispatches without knowing that logs has a
// unit name and processes has a pid.
//
// Nothing here interprets a value. A row's only job is to say which of
// operation.Params a token lands in; internal/operation's registry is what
// routes it through internal/vo's Parse*, and that is still the only path from
// a raw string to an operation.

// argSpec declares one operation-specific input. A row with Flag set is a long
// flag; a row with Flag empty is the operation's positional argument, matched
// in declaration order. Usage names the argument in help and in the error a
// caller sees when they pass the wrong number of them.
type argSpec struct {
	Flag  string
	Usage string
	Field func(*operation.Params) *string
}

// The Params fields a row can target. They are named functions rather than
// closures written inline in the table so that the table stays a list of
// declarations and `go vet` sees each accessor once.
func serviceNameField(p *operation.Params) *string { return &p.ServiceName }
func processNameField(p *operation.Params) *string { return &p.ProcessName }
func pidField(p *operation.Params) *string         { return &p.PID }
func sinceField(p *operation.Params) *string       { return &p.Since }
func linesField(p *operation.Params) *string       { return &p.Lines }

// operationSpecs is the CLI's dispatch table: exactly the operation names
// focal offers, each with the inputs it takes. A name absent from this table is
// not an operation focal has — that is what makes `focal HOST exec` an unknown
// operation rather than an escape hatch, and it is why the table lists the
// operations that take nothing (system, cpu, …) explicitly instead of falling
// through to a default row.
//
// operations_test.go fails if a name here cannot be built by
// internal/operation's registry, and if the key set drifts from focal's
// declared operations.
var operationSpecs = map[string][]argSpec{
	"system":  {},
	"cpu":     {},
	"memory":  {},
	"storage": {},
	"network": {},
	"inspect": {},
	"service": {
		{Usage: "SERVICE", Field: serviceNameField},
	},
	"processes": {
		{Flag: "name", Usage: "only processes whose command name contains this text", Field: processNameField},
		{Flag: "pid", Usage: "only the process with this pid", Field: pidField},
	},
	"logs": {
		{Usage: "SERVICE", Field: serviceNameField},
		{Flag: "since", Usage: "how far back to read, e.g. 30m or 24h", Field: sinceField},
		{Flag: "lines", Usage: "how many lines to return, e.g. 200", Field: linesField},
	},
	"kernel": {
		{Flag: "since", Usage: "how far back to read, e.g. 30m or 24h", Field: sinceField},
		{Flag: "lines", Usage: "how many lines to return, e.g. 200", Field: linesField},
	},
}

// operationNames returns the operation names focal accepts, sorted, for the
// allowed list of an unknown-operation error.
func operationNames() []string {
	names := make([]string, 0, len(operationSpecs))
	for name := range operationSpecs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseOperationArgs reads one operation's argv tail into Params.
//
// Flags are parsed by pflag with output discarded, so an unknown flag becomes a
// usage error in focal's own schema rather than a usage dump on stderr. The
// values are carried across as the raw strings the caller typed: this function
// never decides whether "30m" is a duration, only that it was written after
// --since.
func parseOperationArgs(name string, args []argSpec, argv []string) (operation.Params, *cliError) {
	// The flag set is taken from a throwaway command rather than built
	// directly, so an operation's own flags are parsed by exactly the same
	// flag package, with the same error handling, as the global flags in
	// root.go — and cobra stays the one flag dependency this module names.
	fs := (&cobra.Command{Use: name}).Flags()
	fs.SetOutput(io.Discard)

	values := make(map[string]*string, len(args))
	var positional []argSpec
	for _, arg := range args {
		if arg.Flag == "" {
			positional = append(positional, arg)
			continue
		}
		values[arg.Flag] = fs.String(arg.Flag, "", arg.Usage)
	}

	if err := fs.Parse(argv); err != nil {
		return operation.Params{}, usageError(
			"invalid_operation_arguments",
			fmt.Sprintf("%s: %s", name, err.Error()),
			name,
			[]string{operationUsage(name, args)},
		)
	}

	rest := fs.Args()
	if len(rest) != len(positional) {
		return operation.Params{}, usageError(
			"wrong_argument_count",
			fmt.Sprintf("%s takes %d positional argument(s), got %d", name, len(positional), len(rest)),
			name,
			[]string{operationUsage(name, args)},
		)
	}

	var params operation.Params
	for i, arg := range positional {
		*arg.Field(&params) = rest[i]
	}
	for _, arg := range args {
		if arg.Flag != "" {
			*arg.Field(&params) = *values[arg.Flag]
		}
	}
	return params, nil
}

// operationUsage renders one operation's accepted form, e.g.
// "focal HOST logs SERVICE [--since VALUE] [--lines VALUE]".
func operationUsage(name string, args []argSpec) string {
	parts := []string{"focal HOST", name}
	for _, arg := range args {
		if arg.Flag == "" {
			parts = append(parts, arg.Usage)
		}
	}
	for _, arg := range args {
		if arg.Flag != "" {
			parts = append(parts, "[--"+arg.Flag+" VALUE]")
		}
	}
	return strings.Join(parts, " ")
}
