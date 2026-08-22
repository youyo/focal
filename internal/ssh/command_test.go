package ssh

import (
	"slices"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
)

// mustPolicy resolves a policy the way a caller of Focal would, so the sudo
// mode a test burns into a Command has passed the capability table.
func mustPolicy(t *testing.T, name string, mode policy.SudoMode) policy.Policy {
	t.Helper()
	p, err := policy.Resolve(name, mode)
	if err != nil {
		t.Fatalf("policy.Resolve(%q, %v): %v", name, mode, err)
	}
	return p
}

func TestNewCommandAccepts(t *testing.T) {
	tests := []struct {
		name    string
		program string
		args    []string
	}{
		{"program only", "uptime", nil},
		{"hyphenated program", "lsb-release", nil},
		{"digit in program", "ss2", nil},
		{"typical arguments", "systemctl", []string{"status", "nginx.service"}},
		{"absolute path argument", "cat", []string{"/proc/meminfo"}},
		{"every allowed character", "df", []string{"aZ0_@+=:,./-"}},
		{"argument at the size ceiling", "cat", []string{strings.Repeat("a", 512)}},
		{"program at the size ceiling", strings.Repeat("a", 512), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := newCommand(policy.Policy{}, tc.program, tc.args...)
			if err != nil {
				t.Fatalf("newCommand(%q, %q) = %v, want no error", tc.program, tc.args, err)
			}
			if cmd.Program() != tc.program {
				t.Errorf("Program() = %q, want %q", cmd.Program(), tc.program)
			}
			if !slices.Equal(cmd.Args(), tc.args) {
				t.Errorf("Args() = %q, want %q", cmd.Args(), tc.args)
			}
			if cmd.IsZero() {
				t.Error("IsZero() = true for a constructed command")
			}
		})
	}
}

func TestNewCommandRejectsUnsafeArguments(t *testing.T) {
	tests := []struct {
		name string
		arg  string
	}{
		{"empty", ""},
		{"space", "nginx conf"},
		{"tab", "nginx\tconf"},
		{"semicolon", "nginx;id"},
		{"pipe", "nginx|id"},
		{"ampersand", "nginx&&id"},
		{"dollar", "$(id)"},
		{"backtick", "`id`"},
		{"newline", "nginx\nid"},
		{"carriage return", "nginx\rid"},
		{"nul", "nginx\x00id"},
		{"output redirect", "nginx>out"},
		{"input redirect", "nginx<in"},
		{"glob", "nginx*"},
		{"single quote", "nginx'id'"},
		{"double quote", "nginx\"id\""},
		{"backslash", "nginx\\id"},
		{"tilde", "~/id"},
		{"percent expansion", "%h"},
		{"brace", "{a,b}"},
		{"leading hyphen flag", "--since"},
		{"leading hyphen option", "-oProxyCommand=evil"},
		{"non-ascii", "café"},
		{"over the size ceiling", strings.Repeat("a", 513)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := newCommand(policy.Policy{}, "cat", tc.arg)
			assertRejected(t, cmd, err, "args")
		})
	}
}

func TestNewCommandRejectsUnsafePrograms(t *testing.T) {
	tests := []struct {
		name    string
		program string
	}{
		{"empty", ""},
		{"uppercase", "Uptime"},
		{"leading digit", "9uptime"},
		{"leading hyphen", "-uptime"},
		{"underscore", "up_time"},
		{"dot", "uptime.sh"},
		{"path", "/usr/bin/uptime"},
		{"space", "up time"},
		{"semicolon", "uptime;id"},
		{"dollar", "$(id)"},
		{"newline", "uptime\nid"},
		{"nul", "uptime\x00"},
		{"over the size ceiling", strings.Repeat("a", 513)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := newCommand(policy.Policy{}, tc.program)
			assertRejected(t, cmd, err, "program")
		})
	}
}

// assertRejected states what every rejection owes its caller: a validation
// error naming the offending input, and no half-built Command.
func assertRejected(t *testing.T, cmd Command, err *result.Error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("newCommand accepted the input, want a rejection (got program %q args %q)", cmd.Program(), cmd.Args())
	}
	if err.Kind != result.KindValidation {
		t.Errorf("Kind = %q, want %q", err.Kind, result.KindValidation)
	}
	if err.Field != field {
		t.Errorf("Field = %q, want %q", err.Field, field)
	}
	if len(err.Allowed) == 0 {
		t.Error("Allowed is empty: a rejection must state what would have been accepted")
	}
	if !cmd.IsZero() {
		t.Errorf("rejected input produced a usable Command %q", cmd.Program())
	}
}

func TestCommandArgsAreCopiedInAndOut(t *testing.T) {
	args := []string{"status", "nginx.service"}
	cmd, err := newCommand(policy.Policy{}, "systemctl", args...)
	if err != nil {
		t.Fatalf("newCommand: %v", err)
	}
	args[0] = "stop"
	got := cmd.Args()
	got[1] = "sshd.service"

	if want := []string{"status", "nginx.service"}; !slices.Equal(cmd.Args(), want) {
		t.Errorf("Args() = %q after the caller mutated its slices, want %q", cmd.Args(), want)
	}
}

func TestZeroCommand(t *testing.T) {
	var cmd Command
	if !cmd.IsZero() {
		t.Error("IsZero() = false for the zero value")
	}
	if cmd.Program() != "" {
		t.Errorf("Program() = %q, want empty", cmd.Program())
	}
	if len(cmd.Args()) != 0 {
		t.Errorf("Args() = %q, want empty", cmd.Args())
	}
	if cmd.Sudo() != policy.SudoNever {
		t.Errorf("Sudo() = %v, want %v", cmd.Sudo(), policy.SudoNever)
	}
}

func TestCommandSudoComesFromThePolicy(t *testing.T) {
	tests := []struct {
		name   string
		policy policy.Policy
		want   policy.SudoMode
	}{
		{"zero policy", policy.Policy{}, policy.SudoNever},
		{"resolved never", mustPolicy(t, "system", policy.SudoNever), policy.SudoNever},
		{"resolved auto", mustPolicy(t, "logs", policy.SudoAuto), policy.SudoAuto},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := newCommand(tc.policy, "uptime")
			if err != nil {
				t.Fatalf("newCommand: %v", err)
			}
			if cmd.Sudo() != tc.want {
				t.Errorf("Sudo() = %v, want %v", cmd.Sudo(), tc.want)
			}
		})
	}
}
