package operation

import (
	"fmt"
	"sort"
	"time"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// EnabledFunc reports whether name's operation is turned on for this
// installation — the same answer config.Config.Operation(name).Enabled()
// gives. Registry takes it as a plain func rather than a config.Config so
// that this package stays free of a dependency on internal/config, the same
// reason NewLogs and NewKernel take their ceilings as vo values instead of
// reading the config file for themselves.
type EnabledFunc func(name string) bool

// Registry is Focal's single declarative table from an operation's name to
// how to build it. CLI, MCP and inspect all resolve a name through a
// Registry's Build and nothing else: there is no second table, and no
// operation outside this file is ever constructed by naming its Go type
// directly from another package.
//
// A Registry is a small value (two func fields), passed by value rather than
// by pointer: inspect holds one so it can build its own sub-operations
// through the same Build and the same Enabled every top-level caller uses,
// and copying it costs nothing.
type Registry struct {
	enabled EnabledFunc
	// now is the clock a request with an absolute time is resolved against.
	// It is read once per Build, never stored as an instant, so a Registry
	// that lives as long as the MCP server answers with the time of each
	// request. Nil means the system clock; tests in this package set it.
	now func() time.Time
}

// clock returns the instant a Build is resolved at. It is the one place
// Focal reads the system clock for an operation.
func (r Registry) clock() time.Time {
	if r.now == nil {
		return time.Now()
	}
	return r.now()
}

// NewRegistry builds a Registry. A nil enabled treats every operation as
// turned on, which is what a caller with no config file — or a test with
// nothing to disable — wants.
func NewRegistry(enabled EnabledFunc) Registry {
	if enabled == nil {
		enabled = func(string) bool { return true }
	}
	return Registry{enabled: enabled}
}

// Enabled reports whether name is turned on for this installation.
func (r Registry) Enabled(name string) bool { return r.enabled(name) }

// Params carries every caller-supplied value across all eleven operations. A
// registry row's builder reads only the fields the operation it builds
// actually takes; CLI and MCP fill in whichever of these their request
// named and leave the rest at the zero value.
//
// Since and Lines are the caller's raw request text, exactly as it arrived
// (empty means "not named"); MaxSince and MaxLines are the administrator's
// ceilings, already vo-typed because they come from a validated
// config.Config rather than from the wire, and a zero value there means no
// ceiling was configured.
//
// Until, Grep, GrepRegex and CountBy are read by the logs row only, which
// also reads Since as either a span or an RFC 3339 instant; kernel reads
// Since as a span alone.
type Params struct {
	ProcessName string
	PID         string
	ServiceName string
	PackageName string
	Since       string
	Until       string
	Lines       string
	Grep        string
	GrepRegex   string
	CountBy     string
	MaxSince    vo.Duration
	MaxLines    vo.LineLimit
}

// registryBuilder turns Params into one Operation. It receives the Registry
// itself so that inspect's row can build its sub-operations through the same
// table and the same Enabled a direct CLI/MCP call to one of those names
// would use.
type registryBuilder func(r Registry, params Params) (Operation, *result.Error)

// registryTable is the single row set this package declares: Focal's eleven
// operation names, each paired with the one way to build it. registry_test.go
// fails if this set and internal/policy's declared capabilities ever
// diverge.
//
// Every builder that takes a caller-named value reaches internal/vo's Parse*
// before that value reaches an operation constructor — either directly here
// (logs, kernel) or through the constructor it calls (processes, service,
// which parse their own arguments). There is no row that hands a raw string
// to an operation.
var registryTable = map[string]registryBuilder{
	"system":  func(Registry, Params) (Operation, *result.Error) { return System{}, nil },
	"cpu":     func(Registry, Params) (Operation, *result.Error) { return CPU{}, nil },
	"memory":  func(Registry, Params) (Operation, *result.Error) { return Memory{}, nil },
	"storage": func(Registry, Params) (Operation, *result.Error) { return Storage{}, nil },
	"network": func(Registry, Params) (Operation, *result.Error) { return Network{}, nil },
	"processes": func(_ Registry, params Params) (Operation, *result.Error) {
		return NewProcesses(params.ProcessName, params.PID)
	},
	"service": func(_ Registry, params Params) (Operation, *result.Error) {
		return NewService(params.ServiceName)
	},
	"package": func(_ Registry, params Params) (Operation, *result.Error) {
		name, err := vo.ParsePackageName(params.PackageName)
		if err != nil {
			return nil, err
		}
		return NewPackage(name), nil
	},
	"logs": func(r Registry, params Params) (Operation, *result.Error) {
		service, err := vo.ParseServiceName(params.ServiceName)
		if err != nil {
			return nil, err
		}
		var q logsQuery
		if q.since, err = parseJournalBound(params.Since); err != nil {
			return nil, err
		}
		if q.until, err = parseJournalBound(params.Until); err != nil {
			return nil, err
		}
		lines, err := parseOptionalLineLimit(params.Lines)
		if err != nil {
			return nil, err
		}
		if params.Grep != "" {
			if q.grep, err = vo.ParseLogPattern(params.Grep); err != nil {
				return nil, err
			}
		}
		if params.GrepRegex != "" {
			if q.grepRegex, err = vo.ParseLogRegex(params.GrepRegex); err != nil {
				return nil, err
			}
		}
		if params.CountBy != "" {
			if q.countBy, err = vo.ParseCountBy(params.CountBy); err != nil {
				return nil, err
			}
		}
		return newLogs(service, q, lines, params.MaxSince, params.MaxLines, r.clock())
	},
	"kernel": func(_ Registry, params Params) (Operation, *result.Error) {
		since, err := parseOptionalDuration(params.Since)
		if err != nil {
			return nil, err
		}
		lines, err := parseOptionalLineLimit(params.Lines)
		if err != nil {
			return nil, err
		}
		return NewKernel(since, lines, params.MaxSince, params.MaxLines)
	},
	"inspect": func(r Registry, _ Params) (Operation, *result.Error) { return NewInspect(r), nil },
}

// parseOptionalDuration and parseOptionalLineLimit are how the logs and
// kernel rows read Since/Lines: the empty string means the caller named
// neither, which NewLogs and NewKernel already treat as "apply the
// default", so it is passed through as the zero value rather than rejected.
// Anything else must be accepted by vo.ParseDuration / vo.ParseLineLimit
// before a row returns it — there is no path from a non-empty raw string to
// an operation that skips this.
func parseOptionalDuration(v string) (vo.Duration, *result.Error) {
	if v == "" {
		return vo.Duration{}, nil
	}
	return vo.ParseDuration(v)
}

func parseOptionalLineLimit(v string) (vo.LineLimit, *result.Error) {
	if v == "" {
		return vo.LineLimit{}, nil
	}
	return vo.ParseLineLimit(v)
}

// registryNames returns registryTable's keys, sorted, for error reporting and
// for registry_test.go's drift check against internal/policy's declared
// capabilities.
func registryNames() []string {
	names := make([]string, 0, len(registryTable))
	for name := range registryTable {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Build resolves name to an Operation using params. An unknown name reports a
// Kind: policy error shaped like policy.Resolve's own unknown_operation
// error, since choosing among Focal's operation names is the same kind of
// decision whichever table makes it.
func (r Registry) Build(name string, params Params) (Operation, *result.Error) {
	build, ok := registryTable[name]
	if !ok {
		return nil, result.PolicyError(
			"unknown_operation",
			fmt.Sprintf("unknown operation %q", name),
			"operations",
			registryNames(),
		)
	}
	return build(r, params)
}
