package operation

import (
	"context"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// Storage is the "storage" operation: filesystem usage, block devices and
// the mount tree, none of which takes a caller value.
type Storage struct{}

var _ Operation = Storage{}

// Name returns "storage".
func (Storage) Name() string { return "storage" }

var storageSteps = []commandStep{
	{name: "df", factory: ssh.DFCommand},
	{name: "lsblk", factory: ssh.LSBLKCommand},
	{name: "findmnt", factory: ssh.FindMntCommand},
}

// Execute runs storageSteps through runMulti.
func (o Storage) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	return runMulti(ctx, ex, t, p, o.Name(), storageSteps)
}
