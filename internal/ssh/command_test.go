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
		args    []argToken
		want    []string
	}{
		{"program only", "uptime", nil, nil},
		{"hyphenated program", "lsb-release", nil, nil},
		{"digit in program", "ss2", nil, nil},
		{"typical arguments", "systemctl", []argToken{lit("show"), val("nginx.service")}, []string{"show", "nginx.service"}},
		{"absolute path argument", "cat", []argToken{lit("/proc/meminfo")}, []string{"/proc/meminfo"}},
		{"every allowed character", "df", []argToken{val("aZ0_@+=:,./-")}, []string{"aZ0_@+=:,./-"}},
		{"option literal", "uname", []argToken{lit("-a")}, []string{"-a"}},
		{"literal carrying a percent sign", "ps", []argToken{lit("-eo"), lit("%cpu,%mem")}, []string{"-eo", "%cpu,%mem"}},
		{"prefixed value", "journalctl", []argToken{prefixed("--since=-", "30m")}, []string{"--since=-30m"}},
		{"argument at the size ceiling", "cat", []argToken{val(strings.Repeat("a", 512))}, []string{strings.Repeat("a", 512)}},
		{"program at the size ceiling", strings.Repeat("a", 512), nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := newCommand(policy.Policy{}, tc.program, tc.args...)
			if err != nil {
				t.Fatalf("newCommand(%q, %v) = %v, want no error", tc.program, tc.args, err)
			}
			if cmd.Program() != tc.program {
				t.Errorf("Program() = %q, want %q", cmd.Program(), tc.program)
			}
			if !slices.Equal(cmd.Args(), tc.want) {
				t.Errorf("Args() = %q, want %q", cmd.Args(), tc.want)
			}
			if cmd.IsZero() {
				t.Error("IsZero() = true for a constructed command")
			}
		})
	}
}

// TestNewCommandRejectsUnsafeArguments is the reject list for val(): every
// value a caller can put into an argv goes through it, and nothing on this list
// may survive the trip.
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
		{"percent expansion is a literal-only character", "%cpu"},
		{"over the size ceiling", strings.Repeat("a", 513)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := newCommand(policy.Policy{}, "cat", val(tc.arg))
			assertRejected(t, cmd, err, "args")
		})

		// The same value is refused when it arrives behind a prefix: the
		// literal is what makes the token an option, so a prefix must not
		// double as permission to relax what follows it.
		t.Run(tc.name+" behind a prefix", func(t *testing.T) {
			cmd, err := newCommand(policy.Policy{}, "journalctl", prefixed("--since=-", tc.arg))
			assertRejected(t, cmd, err, "args")
		})
	}
}

// TestPrefixedTokenIsBoundedAsAWhole covers the seam between the two halves of
// a prefixed token: each half can be under the ceiling while their
// concatenation is over it.
func TestPrefixedTokenIsBoundedAsAWhole(t *testing.T) {
	cmd, err := newCommand(policy.Policy{}, "journalctl", prefixed("--since=-", strings.Repeat("a", 512)))
	assertRejected(t, cmd, err, "args")
}

// TestLiteralTokensMayLookLikeOptions states the asymmetry the token type
// exists for. "-a" is a legitimate token when Focal wrote it and an injected
// option when a caller supplied it, and only the constructor tells the two
// apart.
func TestLiteralTokensMayLookLikeOptions(t *testing.T) {
	cmd, err := newCommand(policy.Policy{}, "uname", lit("-a"))
	if err != nil {
		t.Fatalf("newCommand with a literal option: %v", err)
	}
	if got := cmd.Args(); !slices.Equal(got, []string{"-a"}) {
		t.Errorf("Args() = %q, want %q", got, []string{"-a"})
	}
	rejected, err := newCommand(policy.Policy{}, "uname", val("-a"))
	assertRejected(t, rejected, err, "args")
}

// TestLiteralTokensAreStillValidated keeps lit() from becoming an escape
// hatch. It is trusted about leading hyphens only; a shell metacharacter is
// refused wherever it came from.
func TestLiteralTokensAreStillValidated(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    string
	}{
		{"empty", ""},
		{"space", "-u nginx"},
		{"semicolon", "-u;id"},
		{"dollar", "$(id)"},
		{"backtick", "`id`"},
		{"newline", "-u\nid"},
		{"nul", "-u\x00"},
		{"pipe", "-u|cat"},
		{"redirect", "-u>out"},
		{"quote", "-u'x'"},
		{"over the size ceiling", strings.Repeat("-", 513)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := newCommand(policy.Policy{}, "journalctl", lit(tc.s))
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
	args := []argToken{lit("show"), val("nginx.service")}
	cmd, err := newCommand(policy.Policy{}, "systemctl", args...)
	if err != nil {
		t.Fatalf("newCommand: %v", err)
	}
	args[0] = lit("stop")
	got := cmd.Args()
	got[1] = "sshd.service"

	if want := []string{"show", "nginx.service"}; !slices.Equal(cmd.Args(), want) {
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
