package ssm

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

const goldenCommandsPath = "testdata/commands_golden.txt"

type commandCase struct {
	name string
	cmd  ssh.Command
	sudo bool
}

// commandCases lists the same catalogue internal/ssh's own golden test
// covers, built the same way an operation would — through an exported
// factory and a zero policy.Policy — plus two shape cases (a bare command and
// a sudo-prefixed one) that are not tied to any one factory.
func commandCases(t *testing.T) []commandCase {
	t.Helper()
	p := policy.Policy{}
	must := func(cmd ssh.Command, err *result.Error) ssh.Command {
		t.Helper()
		if err != nil {
			t.Fatalf("factory rejected its own arguments: %v", err)
		}
		return cmd
	}
	unit := mustServiceName(t, "nginx.service")
	since := mustDuration(t, "30m")
	kernelSince := mustDuration(t, "1h")
	lines := mustLineLimit(t, "200")

	uptime := must(ssh.UptimeCommand(p))
	systemctl := must(ssh.ServiceStatusCommand(p, unit))

	return []commandCase{
		{name: "plain", cmd: uptime},
		{name: "sudo prefix", cmd: uptime, sudo: true},
		{name: "UptimeCommand", cmd: uptime},
		{name: "UnameCommand", cmd: must(ssh.UnameCommand(p))},
		{name: "HostnameCommand", cmd: must(ssh.HostnameCommand(p))},
		{name: "DateCommand", cmd: must(ssh.DateCommand(p))},
		{name: "OSReleaseCommand", cmd: must(ssh.OSReleaseCommand(p))},
		{name: "LSCPUCommand", cmd: must(ssh.LSCPUCommand(p))},
		{name: "LoadAvgCommand", cmd: must(ssh.LoadAvgCommand(p))},
		{name: "FreeCommand", cmd: must(ssh.FreeCommand(p))},
		{name: "MemInfoCommand", cmd: must(ssh.MemInfoCommand(p))},
		{name: "DFCommand", cmd: must(ssh.DFCommand(p))},
		{name: "LSBLKCommand", cmd: must(ssh.LSBLKCommand(p))},
		{name: "FindMntCommand", cmd: must(ssh.FindMntCommand(p))},
		{name: "IPAddrCommand", cmd: must(ssh.IPAddrCommand(p))},
		{name: "IPRouteCommand", cmd: must(ssh.IPRouteCommand(p))},
		{name: "SocketStatsCommand", cmd: must(ssh.SocketStatsCommand(p))},
		{name: "ResolvConfCommand", cmd: must(ssh.ResolvConfCommand(p))},
		{name: "ProcessListCommand", cmd: must(ssh.ProcessListCommand(p))},
		{name: "ServiceStatusCommand", cmd: systemctl},
		{name: "LogsCommand", cmd: must(ssh.LogsCommand(p, unit, since, lines))},
		{name: "KernelLogsCommand", cmd: must(ssh.KernelLogsCommand(p, kernelSince, lines))},
	}
}

// TestQuoteTokenRoundTrips is the property behind commandLine's second layer:
// whatever byte a token holds, parsing quoteToken's output back into a single
// shell word must reproduce the original token exactly, including a token
// that itself contains a single quote (a byte argSpec/literalSpec do not
// allow today, but which this function must still handle correctly rather
// than relying on that allowlist).
//
// unquoteSingleQuoted, not a real shell, does the parsing: it implements only
// the small grammar quoteToken itself emits — runs of '...' and the \' escape
// between them — so the test verifies quoteToken against an independent,
// from-scratch reading of the POSIX single-quoting rule rather than against
// whatever quirks a particular shell binary happens to have.
func TestQuoteTokenRoundTrips(t *testing.T) {
	cases := []string{
		"",
		"plain",
		"has space",
		"nginx.service",
		"a'b",
		"'leading and trailing'",
		"''",
		"a$b`c\\d*e?f",
		"'''",
		"a'''b",
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			quoted := quoteToken(s)
			got, err := unquoteSingleQuoted(quoted)
			if err != nil {
				t.Fatalf("unquoteSingleQuoted(%q): %v", quoted, err)
			}
			if got != s {
				t.Errorf("quoteToken(%q) = %q, round-tripped to %q", s, quoted, got)
			}
		})
	}
}

// unquoteSingleQuoted parses the POSIX single-quoting grammar quoteToken
// produces: a sequence of 'literal' segments — everything between a pair of
// single quotes is literal, with no escape of its own — and, between them,
// a backslash followed by exactly one byte, taken literally. It is the
// minimal parser for that grammar, not a general shell word-splitter: an
// unterminated quote, a trailing backslash, or any byte outside a quote and
// not part of a backslash pair is an error, since quoteToken never emits any
// of those.
func unquoteSingleQuoted(s string) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		switch s[i] {
		case '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return "", fmt.Errorf("unterminated quote at byte %d in %q", i, s)
			}
			b.WriteString(s[i+1 : i+1+end])
			i += 1 + end + 1
		case '\\':
			if i+1 >= len(s) {
				return "", fmt.Errorf("trailing backslash at byte %d in %q", i, s)
			}
			b.WriteByte(s[i+1])
			i += 2
		default:
			return "", fmt.Errorf("byte %d (%q) in %q is outside any quote and not part of a backslash escape",
				i, s[i], s)
		}
	}
	return b.String(), nil
}

func TestCommandLineMatchesGolden(t *testing.T) {
	golden := readGoldenCommands(t)
	cases := commandCases(t)
	if len(cases) != len(golden) {
		t.Fatalf("golden file has %d cases, the test table has %d: every commandLine output must be reviewable in %s",
			len(golden), len(cases), goldenCommandsPath)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, ok := golden[tc.name]
			if !ok {
				t.Fatalf("no block named %q in %s", tc.name, goldenCommandsPath)
			}
			got := commandLine(tc.cmd, tc.sudo)
			if got != want {
				t.Errorf("commandLine\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

// readGoldenCommands parses the golden file into name -> line, the same
// format internal/ssh's argv golden reader uses.
func readGoldenCommands(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(goldenCommandsPath)
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
			t.Fatalf("command line %q in %s has no preceding %q block name", line, goldenCommandsPath, "#")
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

func mustServiceName(t *testing.T, s string) vo.ServiceName {
	t.Helper()
	name, err := vo.ParseServiceName(s)
	if err != nil {
		t.Fatalf("vo.ParseServiceName(%q): %v", s, err)
	}
	return name
}

func mustDuration(t *testing.T, s string) vo.Duration {
	t.Helper()
	d, err := vo.ParseDuration(s)
	if err != nil {
		t.Fatalf("vo.ParseDuration(%q): %v", s, err)
	}
	return d
}

func mustLineLimit(t *testing.T, s string) vo.LineLimit {
	t.Helper()
	l, err := vo.ParseLineLimit(s)
	if err != nil {
		t.Fatalf("vo.ParseLineLimit(%q): %v", s, err)
	}
	return l
}
