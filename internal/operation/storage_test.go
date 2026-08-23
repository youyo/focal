package operation_test

import (
	"context"
	"testing"

	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
)

// TestStorageGoldenArgv is the golden argv test issue #9 requires for
// "storage": df -PT, lsblk with its explicit column list, then findmnt.
func TestStorageGoldenArgv(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)
	p := mustPolicy(t, "storage", policy.SudoNever)

	_, err := operation.Storage{}.Execute(context.Background(), rec, mustTarget(t, "example.com"), p)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	calls := rec.Calls()
	if len(calls) != 3 {
		t.Fatalf("got %d calls, want 3", len(calls))
	}
	want := []struct {
		program string
		args    []string
	}{
		{"df", []string{"-PT"}},
		{"lsblk", []string{"-o", "NAME,TYPE,SIZE,FSTYPE,MOUNTPOINTS"}},
		{"findmnt", nil},
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

func TestStoragePartNamesAndOrder(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{ExitCode: 0}, nil)
	p := mustPolicy(t, "storage", policy.SudoNever)

	env, err := operation.Storage{}.Execute(context.Background(), rec, mustTarget(t, "example.com"), p)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	wantNames := []string{"df", "lsblk", "findmnt"}
	if len(env.Parts) != len(wantNames) {
		t.Fatalf("got %d parts, want %d", len(env.Parts), len(wantNames))
	}
	for i, name := range wantNames {
		if env.Parts[i].Name != name {
			t.Errorf("Parts[%d].Name = %q, want %q", i, env.Parts[i].Name, name)
		}
	}
}
