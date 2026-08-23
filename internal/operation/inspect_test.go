// inspect_test.go is an internal test: inspectSubOperations' fixed order and
// inspectMaxConcurrency are unexported, and they are the part of this
// operation worth pinning down directly, the way logs_test.go and
// kernel_test.go reach journalSince/journalLines.
package operation

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
	"github.com/youyo/focal/internal/vo"
)

func inspectTestTarget(t *testing.T) vo.Target {
	t.Helper()
	target, err := vo.ParseTarget("web01")
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	return target
}

func inspectTestPolicy(t *testing.T) policy.Policy {
	t.Helper()
	p, err := policy.Resolve("inspect", policy.SudoNever)
	if err != nil {
		t.Fatalf("policy.Resolve(inspect): %v", err)
	}
	return p
}

// keyedExecutor answers Execute by the Command's Program and Args rather
// than by call order, so a test can pin a failure to one specific remote
// command (e.g. "ip route show") while inspect runs its sub-operations
// concurrently and the order Execute is actually called in is not
// deterministic. sshtest.Recorder's RespondSeq answers strictly by call
// order and cannot do this, which is why this type exists instead of a
// parallelism-1 workaround.
type keyedExecutor struct {
	mu       sync.Mutex
	calls    []ssh.Command
	byKey    map[string]sshtest.Response
	fallback ssh.Output
}

var _ ssh.Executor = (*keyedExecutor)(nil)

func newKeyedExecutor() *keyedExecutor {
	return &keyedExecutor{byKey: make(map[string]sshtest.Response), fallback: ssh.Output{ExitCode: 0}}
}

func commandKey(program string, args []string) string {
	return program + "\x00" + strings.Join(args, "\x00")
}

func (e *keyedExecutor) respond(program string, args []string, out ssh.Output, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.byKey[commandKey(program, args)] = sshtest.Response{Output: out, Err: err}
}

func (e *keyedExecutor) Execute(_ context.Context, _ vo.Target, cmd ssh.Command) (ssh.Output, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, cmd)
	if resp, ok := e.byKey[commandKey(cmd.Program(), cmd.Args())]; ok {
		return resp.Output, resp.Err
	}
	return e.fallback, nil
}

func (e *keyedExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

// TestInspectHappyPathFixedOrder is the composition contract: with every
// sub-operation enabled, Envelope.Parts carries exactly
// inspectSubOperations' six names in that fixed order, regardless of which
// goroutine happened to finish first, and the Envelope is StatusOK.
func TestInspectHappyPathFixedOrder(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)

	op := NewInspect(NewRegistry(nil))
	env, err := op.Execute(context.Background(), rec, inspectTestTarget(t), inspectTestPolicy(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(env.Parts) != len(inspectSubOperations) {
		t.Fatalf("got %d parts, want %d", len(env.Parts), len(inspectSubOperations))
	}
	for i, name := range inspectSubOperations {
		if env.Parts[i].Name != name {
			t.Errorf("Parts[%d].Name = %q, want %q", i, env.Parts[i].Name, name)
		}
	}
	if env.Status != result.StatusOK {
		t.Errorf("Envelope.Status = %q, want ok", env.Status)
	}
	if env.Data != nil {
		t.Errorf("Envelope.Data = %v, want nil (nothing skipped)", env.Data)
	}

	// system(5) + cpu(2) + memory(2) + storage(3) + network(4) + processes(1).
	const wantCalls = 5 + 2 + 2 + 3 + 4 + 1
	if got := len(rec.Calls()); got != wantCalls {
		t.Errorf("got %d recorded calls, want %d", got, wantCalls)
	}
	assertOnlyKnownSubOperationCommands(t, rec.Calls())
}

// knownSubOperationCommands is every argv the six sub-operations
// inspectSubOperations composes can produce, keyed the same way
// keyedExecutor keys a call. inspect itself calls no factory in
// internal/ssh — Execute never imports ssh's command constructors, only the
// operations it delegates to do — so every call a Recorder observes during
// an Inspect.Execute must be one of these; anything else would mean inspect
// had grown a remote command of its own.
var knownSubOperationCommands = map[string]bool{
	commandKey("uname", []string{"-a"}):                                                true,
	commandKey("hostname", nil):                                                        true,
	commandKey("uptime", nil):                                                          true,
	commandKey("date", []string{"--iso-8601=seconds"}):                                 true,
	commandKey("cat", []string{"/etc/os-release"}):                                     true,
	commandKey("lscpu", nil):                                                           true,
	commandKey("cat", []string{"/proc/loadavg"}):                                       true,
	commandKey("free", []string{"-b"}):                                                 true,
	commandKey("cat", []string{"/proc/meminfo"}):                                       true,
	commandKey("df", []string{"-PT"}):                                                  true,
	commandKey("lsblk", []string{"-o", "NAME,TYPE,SIZE,FSTYPE,MOUNTPOINTS"}):           true,
	commandKey("findmnt", nil):                                                         true,
	commandKey("ip", []string{"-details", "addr", "show"}):                             true,
	commandKey("ip", []string{"route", "show"}):                                        true,
	commandKey("ss", []string{"-lntup"}):                                               true,
	commandKey("cat", []string{"/etc/resolv.conf"}):                                    true,
	commandKey("ps", []string{"-eo", "pid,ppid,user,state,%cpu,%mem,etime,comm,args"}): true,
}

func assertOnlyKnownSubOperationCommands(t *testing.T, calls []sshtest.Call) {
	t.Helper()
	for _, call := range calls {
		key := commandKey(call.Command.Program(), call.Command.Args())
		if !knownSubOperationCommands[key] {
			t.Errorf("recorded a call inspect's six sub-operations do not produce: %s %v", call.Command.Program(), call.Command.Args())
		}
	}
}

// TestInspectNestsSubOperationParts confirms a multi-command sub-operation
// (system runs five) carries its own Parts nested under inspect's Part for
// it, matching result.Part's documented composite shape.
func TestInspectNestsSubOperationParts(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)

	op := NewInspect(NewRegistry(nil))
	env, err := op.Execute(context.Background(), rec, inspectTestTarget(t), inspectTestPolicy(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	systemPart := env.Parts[0]
	if systemPart.Name != "system" {
		t.Fatalf("Parts[0].Name = %q, want system", systemPart.Name)
	}
	wantSystemSubParts := []string{"uname", "hostname", "uptime", "date", "os-release"}
	if len(systemPart.Parts) != len(wantSystemSubParts) {
		t.Fatalf("system Part has %d nested parts, want %d", len(systemPart.Parts), len(wantSystemSubParts))
	}
	for i, name := range wantSystemSubParts {
		if systemPart.Parts[i].Name != name {
			t.Errorf("system Parts[%d].Name = %q, want %q", i, systemPart.Parts[i].Name, name)
		}
	}

	// processes runs one command, so it reports directly with no nested Parts.
	processesPart := env.Parts[len(env.Parts)-1]
	if processesPart.Name != "processes" {
		t.Fatalf("last Part.Name = %q, want processes", processesPart.Name)
	}
	if len(processesPart.Parts) != 0 {
		t.Errorf("processes Part.Parts = %v, want empty", processesPart.Parts)
	}
}

// TestInspectDisabledSubOperationSkipped is the disabled-skip contract: a
// sub-operation registry.Enabled reports false for is never run at all (no
// Command reaches the Recorder), does not appear in Parts, and is named in
// Data["skipped"] instead.
func TestInspectDisabledSubOperationSkipped(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)

	disabled := map[string]bool{"network": true, "processes": true}
	registry := NewRegistry(func(name string) bool { return !disabled[name] })

	op := NewInspect(registry)
	env, err := op.Execute(context.Background(), rec, inspectTestTarget(t), inspectTestPolicy(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	wantNames := []string{"system", "cpu", "memory", "storage"}
	if len(env.Parts) != len(wantNames) {
		t.Fatalf("got %d parts, want %d: %+v", len(env.Parts), len(wantNames), env.Parts)
	}
	for i, name := range wantNames {
		if env.Parts[i].Name != name {
			t.Errorf("Parts[%d].Name = %q, want %q", i, env.Parts[i].Name, name)
		}
	}
	if env.Data["skipped"] != "network,processes" {
		t.Errorf("Data[skipped] = %q, want %q", env.Data["skipped"], "network,processes")
	}

	// network(4) + processes(1) commands must never have been sent.
	const wantCalls = 5 + 2 + 2 + 3
	if got := len(rec.Calls()); got != wantCalls {
		t.Errorf("got %d recorded calls, want %d (network/processes must not run)", got, wantCalls)
	}
	for _, call := range rec.Calls() {
		if call.Command.Program() == "ip" || call.Command.Program() == "ss" || call.Command.Program() == "ps" {
			t.Errorf("recorded a call to %q, a disabled sub-operation's command", call.Command.Program())
		}
	}
}

// TestInspectPartialFailureAggregatesStatus is the partial-failure contract:
// one sub-operation's command failing does not stop the rest, and
// Envelope.Status reflects s3's aggregation rule (failed beats ok).
//
// This uses keyedExecutor rather than sshtest.Recorder.RespondSeq because
// inspect runs its sub-operations concurrently: the order Execute actually
// sees the six sub-operations' seventeen commands in is not fixed, so a
// sequence indexed by call order could pin the injected failure to the wrong
// command.
func TestInspectPartialFailureAggregatesStatus(t *testing.T) {
	ex := newKeyedExecutor()
	ex.respond("ip", []string{"route", "show"}, ssh.Output{ExitCode: 1, Stderr: "network unreachable"}, nil)

	op := NewInspect(NewRegistry(nil))
	env, err := op.Execute(context.Background(), ex, inspectTestTarget(t), inspectTestPolicy(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(env.Parts) != len(inspectSubOperations) {
		t.Fatalf("got %d parts, want %d (a failing command must not drop the rest)", len(env.Parts), len(inspectSubOperations))
	}
	if env.Status != result.StatusFailed {
		t.Errorf("Envelope.Status = %q, want failed", env.Status)
	}

	for _, part := range env.Parts {
		switch part.Name {
		case "network":
			if part.Status != result.StatusFailed {
				t.Errorf("network Part.Status = %q, want failed", part.Status)
			}
		default:
			if part.Status != result.StatusOK {
				t.Errorf("%s Part.Status = %q, want ok", part.Name, part.Status)
			}
		}
	}

	const wantCalls = 5 + 2 + 2 + 3 + 4 + 1
	if got := ex.callCount(); got != wantCalls {
		t.Errorf("got %d recorded calls, want %d", got, wantCalls)
	}
}

// TestInspectRunsSubOperationsConcurrentlyWithinBound proves inspect's
// parallelism: more than one sub-operation is in flight at once, and the
// number in flight never exceeds inspectMaxConcurrency, the one constant
// that bounds it. Run with -race, this also exercises the concurrent writes
// into Execute's per-index parts slice and the shared executor state.
func TestInspectRunsSubOperationsConcurrentlyWithinBound(t *testing.T) {
	var mu sync.Mutex
	current, maxSeen := 0, 0
	tracker := trackingExecutor{
		before: func() {
			mu.Lock()
			current++
			if current > maxSeen {
				maxSeen = current
			}
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
		},
		after: func() {
			mu.Lock()
			current--
			mu.Unlock()
		},
	}

	op := NewInspect(NewRegistry(nil))
	if _, err := op.Execute(context.Background(), tracker, inspectTestTarget(t), inspectTestPolicy(t)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if maxSeen < 2 {
		t.Errorf("max concurrent commands observed = %d, want at least 2 (sub-operations should overlap)", maxSeen)
	}
	if maxSeen > inspectMaxConcurrency {
		t.Errorf("max concurrent commands observed = %d, want at most inspectMaxConcurrency (%d)", maxSeen, inspectMaxConcurrency)
	}
}

// trackingExecutor calls before/after around every Execute, for measuring how
// many calls a caller has in flight at once. It answers every call the same
// way, since this test cares about concurrency, not content.
type trackingExecutor struct {
	before func()
	after  func()
}

var _ ssh.Executor = trackingExecutor{}

func (e trackingExecutor) Execute(context.Context, vo.Target, ssh.Command) (ssh.Output, error) {
	e.before()
	defer e.after()
	return ssh.Output{ExitCode: 0}, nil
}

// TestInspectSudoAlwaysIsRefusedByPolicy mirrors logs/kernel's own sudo
// contract test: inspect's capability is SudoNever only, so
// policy.Resolve("inspect", SudoAlways) never even produces a Policy for
// inspect to run with.
func TestInspectSudoAlwaysIsRefusedByPolicy(t *testing.T) {
	if _, err := policy.Resolve("inspect", policy.SudoAlways); err == nil {
		t.Fatal("policy.Resolve(inspect, always) = nil error, want a policy rejection")
	}
}
