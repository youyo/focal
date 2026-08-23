package operation

import (
	"context"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// Memory is the "memory" operation: free(1) in bytes and the detail in
// /proc/meminfo, neither of which takes a caller value.
type Memory struct{}

var _ Operation = Memory{}

// Name returns "memory".
func (Memory) Name() string { return "memory" }

var memorySteps = []commandStep{
	{name: "free", factory: ssh.FreeCommand},
	{name: "meminfo", factory: ssh.MemInfoCommand},
}

// Execute runs memorySteps through runMulti.
func (o Memory) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	return runMulti(ctx, ex, t, p, o.Name(), memorySteps)
}
