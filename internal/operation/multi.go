package operation

import (
	"context"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// commandFactory is the shape every internal/ssh factory that takes only a
// resolved Policy has (UptimeCommand, UnameCommand, LSCPUCommand, and the
// rest of the fixed-argv commands system/cpu/memory/storage/network run). A
// factory that also takes a caller value (LogsCommand, ServiceStatusCommand)
// does not fit this signature, which is exactly the set s4's five
// no-input operations are built from.
type commandFactory func(policy.Policy) (ssh.Command, *result.Error)

// commandStep pairs one factory with the Part name its outcome is reported
// under.
type commandStep struct {
	name    string
	factory commandFactory
}

// runMulti is the one place a no-input operation turns a declared list of
// commandSteps into an Envelope: it runs each step's factory in order,
// executes it through ex, folds every outcome into a result.Part via
// buildPart, and aggregates the Parts into the Envelope's own
// Status/ExitCode/DurationMs/Truncated. system, cpu, memory, storage and
// network differ only in the step table they pass here — none of them
// restates this loop.
//
// A factory error means Execute could not even obtain a Command (the Policy
// or a caller value it closed over was rejected), so nothing was run and
// runMulti returns it as the error the Operation contract expects, rather
// than folding it into a Part. A command that ran and failed is not that: it
// becomes a failed Part like any other, and the loop keeps going, so one
// failing step never stops the rest from running.
func runMulti(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy, operationName string, steps []commandStep) (result.Envelope, error) {
	parts := make([]result.Part, 0, len(steps))
	for _, step := range steps {
		cmd, err := step.factory(p)
		if err != nil {
			return result.Envelope{}, err
		}
		out, execErr := ex.Execute(ctx, t, cmd)
		parts = append(parts, buildPart(step.name, out, execErr))
	}

	status, exitCode, durationMs, truncated := aggregate(parts)
	return result.Envelope{
		Operation:  operationName,
		Host:       t.String(),
		Status:     status,
		ExitCode:   exitCode,
		DurationMs: durationMs,
		Truncated:  truncated,
		Parts:      parts,
	}, nil
}
