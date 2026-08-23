package operation

import (
	"context"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// kernelDefaultSince is the span kernel looks back over when the caller names
// none. It is an hour rather than the thirty minutes logs defaults to: kernel
// messages are sparse, and a window that returns nothing is not an answer.
var kernelDefaultSince = journalMustDuration("1h")

// Kernel reads the kernel ring buffer from the journal. It carries no unit
// name — journalctl -k selects the messages — so the only caller-chosen
// values in its argv are the span and the line count.
type Kernel struct {
	since vo.Duration
	lines vo.LineLimit
}

// NewKernel builds a kernel request. since and lines may be zero values,
// meaning the caller named neither and the defaults apply; maxSince and
// maxLines are the administrator's ceilings, resolved by the same rules
// NewLogs uses, and may themselves be zero when none was configured.
//
// This operation is disabled in Focal's default config. That is a separate
// gate, applied by the layer that reads the config before an operation is
// built at all, and nothing here weakens or restates it.
func NewKernel(since vo.Duration, lines vo.LineLimit, maxSince vo.Duration, maxLines vo.LineLimit) (Kernel, *result.Error) {
	resolvedSince, err := journalSince(since, kernelDefaultSince, maxSince)
	if err != nil {
		return Kernel{}, err
	}
	resolvedLines, err := journalLines(lines, journalDefaultLines, maxLines)
	if err != nil {
		return Kernel{}, err
	}
	return Kernel{since: resolvedSince, lines: resolvedLines}, nil
}

// Name returns "kernel".
func (o Kernel) Name() string { return "kernel" }

// Execute runs journalctl -k.
func (o Kernel) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	cmd, err := ssh.KernelLogsCommand(p, o.since, o.lines)
	if err != nil {
		return result.Envelope{}, err
	}
	out, execErr := ex.Execute(ctx, t, cmd)
	return journalEnvelope(o.Name(), t, out, execErr), nil
}
