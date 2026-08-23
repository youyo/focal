package operation

import (
	"context"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// Network is the "network" operation: interfaces, routes, listening sockets
// and the resolver configuration, none of which takes a caller value.
type Network struct{}

var _ Operation = Network{}

// Name returns "network".
func (Network) Name() string { return "network" }

var networkSteps = []commandStep{
	{name: "ip-addr", factory: ssh.IPAddrCommand},
	{name: "ip-route", factory: ssh.IPRouteCommand},
	{name: "sockets", factory: ssh.SocketStatsCommand},
	{name: "resolv-conf", factory: ssh.ResolvConfCommand},
}

// Execute runs networkSteps through runMulti.
func (o Network) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	return runMulti(ctx, ex, t, p, o.Name(), networkSteps)
}
