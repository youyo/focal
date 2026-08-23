package operation

import (
	"context"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// CPU is the "cpu" operation: the CPU topology and load average, neither of
// which takes a caller value.
type CPU struct{}

var _ Operation = CPU{}

// Name returns "cpu".
func (CPU) Name() string { return "cpu" }

var cpuSteps = []commandStep{
	{name: "lscpu", factory: ssh.LSCPUCommand},
	{name: "loadavg", factory: ssh.LoadAvgCommand},
}

// Execute runs cpuSteps through runMulti.
func (o CPU) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	return runMulti(ctx, ex, t, p, o.Name(), cpuSteps)
}
