package operation_test

import (
	"context"
	"testing"

	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
)

// TestNetworkGoldenArgv is the golden argv test issue #9 requires for
// "network": ip -details addr show, ip route show, ss -lntup, then
// cat /etc/resolv.conf.
func TestNetworkGoldenArgv(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)
	p := mustPolicy(t, "network", policy.SudoNever)

	_, err := operation.Network{}.Execute(context.Background(), rec, mustTarget(t, "example.com"), p)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	calls := rec.Calls()
	if len(calls) != 4 {
		t.Fatalf("got %d calls, want 4", len(calls))
	}
	want := []struct {
		program string
		args    []string
	}{
		{"ip", []string{"-details", "addr", "show"}},
		{"ip", []string{"route", "show"}},
		{"ss", []string{"-lntup"}},
		{"cat", []string{"/etc/resolv.conf"}},
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

func TestNetworkPartNamesAndOrder(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)
	p := mustPolicy(t, "network", policy.SudoNever)

	env, err := operation.Network{}.Execute(context.Background(), rec, mustTarget(t, "example.com"), p)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	wantNames := []string{"ip-addr", "ip-route", "sockets", "resolv-conf"}
	if len(env.Parts) != len(wantNames) {
		t.Fatalf("got %d parts, want %d", len(env.Parts), len(wantNames))
	}
	for i, name := range wantNames {
		if env.Parts[i].Name != name {
			t.Errorf("Parts[%d].Name = %q, want %q", i, env.Parts[i].Name, name)
		}
	}
}
