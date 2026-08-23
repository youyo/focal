package operation

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// inspectSubOperations is the fixed set and order inspect composes and
// reports through Envelope.Parts — the order Parts is built in, never the
// order goroutines happen to finish in. Every name here is also a row
// registryTable declares; registry_test.go's drift check is what keeps the
// two from drifting apart.
var inspectSubOperations = []string{"system", "cpu", "memory", "storage", "network", "processes"}

// inspectMaxConcurrency bounds how many sub-operations Execute runs at once.
// This is the one place that number is decided.
const inspectMaxConcurrency = 3

// Inspect is the inspect operation: Focal's "first move" against a host. It
// has no remote command of its own — Execute never calls a factory in
// internal/ssh — and instead composes system, cpu, memory, storage, network
// and processes through registry, the same Registry a CLI or MCP caller
// would resolve any other operation name from.
type Inspect struct {
	registry Registry
}

var _ Operation = Inspect{}

// NewInspect builds the inspect operation from registry. Holding the
// Registry itself, rather than the six sub-operations directly, is what lets
// a disabled sub-operation be decided the same way for inspect as it is for
// a direct CLI/MCP call to that name: both read registry.Enabled.
func NewInspect(registry Registry) Inspect {
	return Inspect{registry: registry}
}

// Name returns "inspect".
func (Inspect) Name() string { return "inspect" }

// Execute runs every enabled name in inspectSubOperations, up to
// inspectMaxConcurrency at a time, and folds each one's Envelope into a Part
// nested under this Envelope's own Parts, in inspectSubOperations' fixed
// order regardless of which sub-operation finished first.
//
// A disabled sub-operation (registry.Enabled reports false) is not built and
// not run at all — no Command reaches ex, so nothing about it appears in a
// Recorder — and its name is listed instead in Data["skipped"]. A
// sub-operation that ran but failed still contributes its Part rather than
// aborting the rest; the worst Status among every included Part (aggregate's
// timeout > failed > truncated > ok rule, the same one system/cpu/... use)
// becomes this Envelope's own Status.
func (o Inspect) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	n := len(inspectSubOperations)
	parts := make([]result.Part, n)
	included := make([]bool, n)
	var skipped []string

	sem := make(chan struct{}, inspectMaxConcurrency)
	var wg sync.WaitGroup
	for i, name := range inspectSubOperations {
		if !o.registry.Enabled(name) {
			skipped = append(skipped, name)
			continue
		}
		sub, buildErr := o.registry.Build(name, Params{})
		if buildErr != nil {
			// None of inspect's six sub-operations take a caller value, so
			// registry.Build cannot actually fail for them today; this stays
			// total rather than panicking if that ever changes.
			parts[i] = inspectFailedPart(name, buildErr)
			included[i] = true
			continue
		}
		included[i] = true
		wg.Add(1)
		go func(i int, name string, sub Operation) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			env, err := sub.Execute(ctx, ex, t, p)
			if err != nil {
				parts[i] = inspectFailedPart(name, err)
				return
			}
			parts[i] = inspectPart(name, env)
		}(i, name, sub)
	}
	wg.Wait()

	ordered := make([]result.Part, 0, n)
	for i := range inspectSubOperations {
		if included[i] {
			ordered = append(ordered, parts[i])
		}
	}

	status, exitCode, durationMs, truncated := aggregate(ordered)
	var data map[string]string
	if len(skipped) > 0 {
		data = map[string]string{"skipped": strings.Join(skipped, ",")}
	}
	return result.Envelope{
		Operation:  o.Name(),
		Host:       t.String(),
		Status:     status,
		ExitCode:   exitCode,
		DurationMs: durationMs,
		Truncated:  truncated,
		Data:       data,
		Parts:      ordered,
	}, nil
}

// inspectPart turns one sub-operation's Envelope into the Part inspect
// reports it as: the same fields Envelope carries, minus Operation/Host
// which Part has no room for, with the sub-operation's own Parts (system,
// for one, builds five) carried through as this Part's nested Parts.
func inspectPart(name string, env result.Envelope) result.Part {
	return result.Part{
		Name:       name,
		Status:     env.Status,
		ExitCode:   env.ExitCode,
		DurationMs: env.DurationMs,
		Truncated:  env.Truncated,
		Stdout:     env.Stdout,
		Stderr:     env.Stderr,
		Data:       env.Data,
		Error:      env.Error,
		Parts:      env.Parts,
	}
}

// inspectFailedPart reports a sub-operation that could not even produce an
// Envelope as a failed Part, the same way buildPart reports an execution
// failure, so that one sub-operation's construction error still leaves the
// rest of inspect's Parts intact instead of aborting the whole Envelope.
func inspectFailedPart(name string, err error) result.Part {
	var rerr *result.Error
	if !errors.As(err, &rerr) {
		rerr = result.ExecutionError("inspect_sub_failed", err.Error())
	}
	return result.Part{Name: name, Status: result.StatusFailed, ExitCode: -1, Error: rerr}
}
