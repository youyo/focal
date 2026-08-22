// Package sshtest provides the ssh.Executor stand-in that lets code above the
// SSH layer — operations, the CLI, the MCP server — be tested without a remote
// host. It lives in its own package rather than in an internal/ssh test file
// because those callers are in other packages and cannot link a test file.
//
// A Recorder answers with whatever it was told to answer and remembers what it
// was asked. What it is worth asserting on is the Command it received: a test
// that finds the expected program, arguments and sudo mode there has shown that
// the caller went through a factory in internal/ssh and through policy.Resolve,
// because there is no other way to obtain a Command at all.
package sshtest

import (
	"context"
	"slices"
	"sync"

	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// Call is one recorded Execute.
type Call struct {
	Target  vo.Target
	Command ssh.Command
	// SudoPrefixed is whether the real executor would have put "sudo -n"
	// in front of this command on its first attempt. It is answered by
	// ssh.PrefixesSudo so that the mock cannot keep asserting a rule the
	// executor has stopped following. A SudoAuto command is recorded as
	// false: escalation there depends on an exit status only a real
	// invocation produces.
	SudoPrefixed bool
}

// Recorder is an ssh.Executor that runs nothing. Its zero value is usable and
// returns an empty Output with a nil error; Respond changes that.
type Recorder struct {
	mu     sync.Mutex
	calls  []Call
	output ssh.Output
	err    error
}

var _ ssh.Executor = (*Recorder)(nil)

// Respond sets what every subsequent Execute returns.
func (r *Recorder) Respond(output ssh.Output, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.output, r.err = output, err
}

// Execute records the call and returns the programmed response. The context is
// unused: nothing here can block, so there is nothing for a caller's deadline
// to cut short.
func (r *Recorder) Execute(_ context.Context, target vo.Target, cmd ssh.Command) (ssh.Output, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, Call{Target: target, Command: cmd, SudoPrefixed: ssh.PrefixesSudo(cmd)})
	return r.output, r.err
}

// Calls returns the calls in the order they arrived. The slice is a copy, so a
// later Execute cannot change what a test has already read.
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}
