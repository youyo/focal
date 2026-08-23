package operation_test

import (
	"context"
	"testing"

	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
)

// TestMemoryGoldenArgv is the golden argv test issue #9 requires for
// "memory": free -b, then cat /proc/meminfo.
func TestMemoryGoldenArgv(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)
	p := mustPolicy(t, "memory", policy.SudoNever)

	_, err := operation.Memory{}.Execute(context.Background(), rec, mustTarget(t, "example.com"), p)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	calls := rec.Calls()
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	want := []struct {
		program string
		args    []string
	}{
		{"free", []string{"-b"}},
		{"cat", []string{"/proc/meminfo"}},
	}
	for i, w := range want {
		if got := calls[i].Command.Program(); got != w.program {
			t.Errorf("call %d Program() = %q, want %q", i, got, w.program)
		}
		if got := calls[i].Command.Args(); !argsEqual(got, w.args) {
			t.Errorf("call %d Args() = %v, want %v", i, got, w.args)
		}
		if calls[i].SudoPrefixed {
			t.Errorf("call %d SudoPrefixed = true, want false", i)
		}
	}
}

func TestMemoryPartNamesAndOrder(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)
	p := mustPolicy(t, "memory", policy.SudoNever)

	env, err := operation.Memory{}.Execute(context.Background(), rec, mustTarget(t, "example.com"), p)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	wantNames := []string{"free", "meminfo"}
	if len(env.Parts) != len(wantNames) {
		t.Fatalf("got %d parts, want %d", len(env.Parts), len(wantNames))
	}
	for i, name := range wantNames {
		if env.Parts[i].Name != name {
			t.Errorf("Parts[%d].Name = %q, want %q", i, env.Parts[i].Name, name)
		}
	}
}
