package ssh

import (
	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
)

// This file is the complete catalogue of remote commands Focal can run. A
// reader auditing what a coding agent is able to cause on a server needs to
// read only this file: newCommand is unexported, so a package outside
// internal/ssh can obtain a Command only by calling one of the factories here,
// and boundary_test.go fails if a factory appears that the catalogue in
// allowedCommandFactories does not list.
//
// Every factory takes the resolved policy.Policy rather than a sudo mode, so
// the privilege a command carries is always the one policy.Resolve arrived at
// by intersecting the operation's capability with the administrator's config.

// UptimeCommand builds the uptime(1) invocation. It is the only command M2
// defines: it needs no arguments and no privilege anywhere, which makes it the
// command that proves the boundary works end to end without also being useful
// to an attacker. The operations of M3 add their factories alongside it.
func UptimeCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "uptime")
}
