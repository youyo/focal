package ssm

import (
	"context"
	"testing"

	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/vo"
)

// TestFilesSendsItsTwoCommandsSeparately runs the files operation against this
// package's executor. SSM always runs as root, and the ownership check is what
// keeps that from widening the listing (ADR 0012), so it has to arrive as a
// SendCommand of its own, before the listing and not joined to it: one
// command per SendCommand, the check first, each rendered by commandLine
// alone.
func TestFilesSendsItsTwoCommandsSeparately(t *testing.T) {
	const chain = "drwxr-xr-x 1 0 0 4096 2026-11-25 08:02:25.929625041 +0000 /\n" +
		"drwxr-xr-x 1 0 0 4096 2026-11-25 08:02:25.929625041 +0000 /var\n" +
		"drwxr-xr-x 1 0 0 4096 2026-11-25 08:02:25.929625041 +0000 /var/log\n"
	fake := &fakeAPI{invocations: map[string][]invocationStep{"cmd-1": {
		successInvocation(chain, "", 0),
		successInvocation("total 4\n-rw-r--r-- 1 6 2026-01-02 03:04:05.000000000 +0900 messages\n", "", 0),
	}}}
	ex := newTestExecutor(t, fake)

	list, err := vo.ParseFilesAllowList([]string{"/var/log"})
	if err != nil {
		t.Fatalf("vo.ParseFilesAllowList: %v", err)
	}
	op, buildErr := operation.NewRegistry(nil).Build("files", operation.Params{Path: "/var/log", FilesAllowList: list})
	if buildErr != nil {
		t.Fatalf("Build(files): %v", buildErr)
	}
	p, perr := policy.Resolve("files", policy.SudoNever)
	if perr != nil {
		t.Fatalf("policy.Resolve: %v", perr)
	}
	env, runErr := op.Execute(context.Background(), ex, testTarget(t), p)
	if runErr != nil {
		t.Fatalf("Execute: %v", runErr)
	}

	want := []string{
		"'ls' '-d' '-n' '-b' '--time-style=full-iso' '/' '/var' '/var/log'",
		"'ls' '-A' '-b' '-g' '-o' '--time-style=full-iso' '--block-size=1' '/var/log'",
	}
	if len(fake.sendCalls) != len(want) {
		t.Fatalf("SendCommand called %d times, want %d", len(fake.sendCalls), len(want))
	}
	for i, call := range fake.sendCalls {
		commands := call.Parameters["commands"]
		if len(commands) != 1 || commands[0] != want[i] {
			t.Errorf("SendCommand %d commands = %q, want [%q]", i, commands, want[i])
		}
	}
	if len(env.Items) != 1 || env.Items[0]["name"] != "messages" {
		t.Errorf("Items = %v", env.Items)
	}
}
