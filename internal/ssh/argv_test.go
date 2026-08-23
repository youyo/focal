package ssh

import (
	"bufio"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/policy"
)

const goldenArgvPath = "testdata/argv_golden.txt"

// argvCases pairs every block name in the golden file with the inputs that
// must produce it. Command values are built here with a struct literal for the
// sudo modes no capability currently offers; outside this package that is not
// possible, which is the property boundary_test.go asserts.
func argvCases(t *testing.T) []struct {
	name   string
	opts   Options
	target string
	cmd    Command
	sudo   bool
} {
	t.Helper()
	uptime := mustCommand(t, "uptime")
	systemctl := mustCommand(t, "systemctl", lit("show"), val("nginx.service"))
	keyAndPort := Options{IdentityFile: "/home/focal/.ssh/id_ed25519", Port: 2222}

	cases := []struct {
		name   string
		opts   Options
		target string
		cmd    Command
		sudo   bool
	}{
		{name: "plain", target: "example.com", cmd: uptime},
		{name: "user in target", target: "ops@example.com", cmd: uptime},
		{name: "identity file", opts: Options{IdentityFile: "/home/focal/.ssh/id_ed25519"}, target: "example.com", cmd: uptime},
		{name: "port", opts: Options{Port: 2222}, target: "example.com", cmd: uptime},
		{name: "identity file and port", opts: keyAndPort, target: "ops@example.com", cmd: uptime},
		{name: "ipv6 target", target: "ops@[2001:db8::1]", cmd: uptime},
		{name: "sudo prefix", target: "example.com", cmd: uptime, sudo: true},
		{name: "command arguments", target: "example.com", cmd: systemctl},
		{name: "sudo prefix with command arguments", opts: keyAndPort, target: "ops@example.com", cmd: systemctl, sudo: true},
	}

	// Every factory in the catalogue also appears as the whole command line it
	// causes, so the golden file reads as the complete list of what any host
	// can be asked to run rather than as a sample of it.
	for _, fc := range factoryCases(t) {
		cases = append(cases, struct {
			name   string
			opts   Options
			target string
			cmd    Command
			sudo   bool
		}{name: fc.name, target: "example.com", cmd: fc.cmd})
	}
	return cases
}

func TestBuildArgvMatchesGolden(t *testing.T) {
	golden := readGoldenArgv(t)
	cases := argvCases(t)
	if len(cases) != len(golden) {
		t.Fatalf("golden file has %d cases, the test table has %d: every accepted argv must be reviewable in %s",
			len(golden), len(cases), goldenArgvPath)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, ok := golden[tc.name]
			if !ok {
				t.Fatalf("no block named %q in %s", tc.name, goldenArgvPath)
			}
			got := "ssh " + strings.Join(buildArgv(tc.opts, tc.target, tc.cmd, tc.sudo), " ")
			if got != want {
				t.Errorf("argv\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

// TestBuildArgvSeparatorAndOptions states the two invariants the golden file
// would only catch by inspection: where "--" sits, and that BatchMode=yes is
// the only -o Focal is able to emit. Together they close the option-injection
// and ProxyCommand/RemoteCommand escape hatches for every case, not just the
// nine recorded ones.
func TestBuildArgvSeparatorAndOptions(t *testing.T) {
	for _, tc := range argvCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			argv := buildArgv(tc.opts, tc.target, tc.cmd, tc.sudo)

			sep := slices.Index(argv, "--")
			if sep < 0 {
				t.Fatalf("argv has no %q separator: %v", "--", argv)
			} else if argv[sep+1] != tc.target {
				t.Errorf("argv[%d] after %q is %q, want the target %q", sep+1, "--", argv[sep+1], tc.target)
			}
			if got := strings.Count(strings.Join(argv, " "), " -- "); got != 1 {
				t.Errorf("%q appears %d times, want exactly once (before the target and nowhere else): %v", "--", got, argv)
			}
			// Only the tokens before the separator are ssh's own options. A
			// "-o" after it belongs to the remote program — lsblk takes one —
			// and reading it as an ssh option would be reading the argv the
			// way neither ssh nor the remote host does.
			for i, tok := range argv[:sep] {
				if tok == "-o" && argv[i+1] != "BatchMode=yes" {
					t.Errorf("argv passes -o %q: BatchMode=yes is the only option Focal sets", argv[i+1])
				}
			}
		})
	}
}

// TestBuildArgvSudoComesFromCommandOnly pins the one input the sudo prefix is
// allowed to depend on. Nothing about the target, the options or the program
// may move "sudo -n" into or out of the argv.
func TestBuildArgvSudoComesFromCommandOnly(t *testing.T) {
	cmd := mustCommand(t, "uptime")
	without := buildArgv(Options{}, "example.com", cmd, false)
	with := buildArgv(Options{}, "example.com", cmd, true)

	if slices.Contains(without, "sudo") {
		t.Errorf("argv contains sudo without being asked for it: %v", without)
	}
	if i := slices.Index(with, "sudo"); i < 0 || with[i+1] != "-n" {
		t.Fatalf("argv does not prefix %q: %v", "sudo -n", with)
	} else if with[i-1] != "example.com" {
		t.Errorf("sudo is not directly after the target: %v", with)
	}
}

// TestPrefixesSudoIsTheFirstAttemptRule documents the rule the mock executor in
// internal/sshtest shares with the real one, so the two cannot drift.
func TestPrefixesSudoIsTheFirstAttemptRule(t *testing.T) {
	for _, tc := range []struct {
		mode policy.SudoMode
		want bool
	}{
		{policy.SudoNever, false},
		{policy.SudoAuto, false},
		{policy.SudoAlways, true},
	} {
		t.Run(tc.mode.String(), func(t *testing.T) {
			if got := PrefixesSudo(Command{program: "uptime", sudo: tc.mode}); got != tc.want {
				t.Errorf("PrefixesSudo(%s) = %t, want %t", tc.mode, got, tc.want)
			}
		})
	}
}

// readGoldenArgv parses the golden file into name -> command line. A block is a
// "# name" comment followed by one command line; the leading prose block has no
// command line after it and is skipped.
func readGoldenArgv(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(goldenArgvPath)
	if err != nil {
		t.Fatalf("open golden file: %v", err)
	}
	defer f.Close()

	golden := make(map[string]string)
	name := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		case name == "":
			t.Fatalf("command line %q in %s has no preceding %q block name", line, goldenArgvPath, "#")
		default:
			if _, dup := golden[name]; dup {
				t.Fatalf("golden file has two blocks named %q", name)
			}
			golden[name] = line
			name = ""
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	return golden
}

// mustCommand builds a Command the way a factory would, for tests that care
// about the argv rather than about validation.
func mustCommand(t *testing.T, program string, args ...argToken) Command {
	t.Helper()
	cmd, err := newCommand(policy.Policy{}, program, args...)
	if err != nil {
		t.Fatalf("newCommand(%q, %v): %v", program, args, err)
	}
	return cmd
}
