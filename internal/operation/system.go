package operation

import (
	"context"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// System is the "system" operation: five fixed commands that describe the
// host itself. It has no fields — none of the five commands it runs take a
// caller value, so there is nothing for it to hold.
type System struct{}

var _ Operation = System{}

// Name returns "system".
func (System) Name() string { return "system" }

// systemSteps is the declared table system's Execute runs: Part name paired
// with the internal/ssh factory that produces it, in the order the Parts are
// reported.
var systemSteps = []commandStep{
	{name: "uname", factory: ssh.UnameCommand},
	{name: "hostname", factory: ssh.HostnameCommand},
	{name: "uptime", factory: ssh.UptimeCommand},
	{name: "date", factory: ssh.DateCommand},
	{name: "os-release", factory: ssh.OSReleaseCommand},
}

// Execute runs systemSteps through runMulti.
func (o System) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	return runMulti(ctx, ex, t, p, o.Name(), systemSteps)
}
