// Package operation defines Operation, the single interface every one of
// Focal's user-facing capabilities implements. It is the seam M3's ten
// operations (system, cpu, memory, storage, network, processes, service,
// logs, kernel, inspect) are built behind: CLI and MCP call an Operation by
// name, never a Command or a shell string, so internal/ssh stays the only
// place a remote command is constructed.
//
// This package has no logic of its own — no operation lives here yet, and
// none of the M3 operations do either. Their capability envelope is already
// declared in internal/policy, and internal/ssh, internal/vo and
// internal/result already give them everything they need to implement this
// interface; internal/operation exists only to name the contract those
// pieces are assembled behind.
package operation

import (
	"context"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// Operation is one Focal capability. Name identifies it — the same string
// passed to policy.Resolve and reported in Envelope.Operation — and Execute
// runs it.
//
// An implementation must not build a Command itself: it obtains one from a
// factory in internal/ssh, passing p (the Policy already resolved for
// Name()), and hands that Command to ex. That is the only path from a typed
// request to a remote argv; an Operation that stored a Command or a raw
// command string on itself would open a second one, so implementations carry
// only their typed parameters (e.g. a Logs operation holds Service, Since and
// Lines, never a Command).
type Operation interface {
	// Name returns the operation name.
	Name() string
	// Execute runs the operation against t using ex, under the policy p
	// already resolved for Name(). It reports its outcome as a
	// result.Envelope; the returned error is non-nil only when the
	// operation could not produce an Envelope at all (e.g. building the
	// Command was rejected, or ex.Execute failed outright).
	Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error)
}
