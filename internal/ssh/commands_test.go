package ssh

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// factoryCase is one entry of the catalogue as a test reads it: the factory's
// name, the Command it built, and the exact argv that Command must carry. The
// table below is the second copy of what commands.go declares, written out by
// hand so that a change to a factory has to be made twice — once in the source
// and once here — before the tests agree with it.
type factoryCase struct {
	name    string
	cmd     Command
	program string
	args    []string
}

// factoryName is the catalogue entry this row exercises. A factory that hands
// back more than one Command has a row, and a golden block, for each of them,
// named "Factory/which"; the part before the slash is the factory.
func (tc factoryCase) factoryName() string {
	name, _, _ := strings.Cut(tc.name, "/")
	return name
}

// factoryCases builds every exported factory once. The same table feeds the
// ssh-level golden file in argv_test.go, so a factory that is added here is
// also reviewable as the complete command line it produces.
func factoryCases(t *testing.T) []factoryCase {
	t.Helper()
	must := commandBuilder(t)
	p := policy.Policy{}
	unit := mustServiceName(t, "nginx.service")
	since := mustDuration(t, "30m")
	kernelSince := mustDuration(t, "1h")
	lines := mustLineLimit(t, "200")
	pkg := mustPackageName(t, "libstdc++6")
	windowSince := mustDuration(t, "150m")
	windowUntil := mustDuration(t, "2h")
	scan := mustLineLimit(t, "100000")
	from := mustTimestamp(t, "2026-11-20T09:00:00+09:00")
	to := mustTimestamp(t, "2026-11-21T09:00:00+09:00")
	filesCheck, filesList := mustFilesCommands(t, p, mustFilesPath(t, "/var/log/nginx"))
	shallowCheck, shallowList := mustFilesCommands(t, p, mustFilesPath(t, "/srv"))

	return []factoryCase{
		{"UptimeCommand", must(UptimeCommand(p)), "uptime", nil},
		{"UnameCommand", must(UnameCommand(p)), "uname", []string{"-a"}},
		{"HostnameCommand", must(HostnameCommand(p)), "hostname", nil},
		{"DateCommand", must(DateCommand(p)), "date", []string{"--iso-8601=seconds"}},
		{"OSReleaseCommand", must(OSReleaseCommand(p)), "cat", []string{"/etc/os-release"}},
		{"LSCPUCommand", must(LSCPUCommand(p)), "lscpu", nil},
		{"LoadAvgCommand", must(LoadAvgCommand(p)), "cat", []string{"/proc/loadavg"}},
		{"FreeCommand", must(FreeCommand(p)), "free", []string{"-b"}},
		{"MemInfoCommand", must(MemInfoCommand(p)), "cat", []string{"/proc/meminfo"}},
		{"DFCommand", must(DFCommand(p)), "df", []string{"-PT"}},
		{"LSBLKCommand", must(LSBLKCommand(p)), "lsblk", []string{"-o", "NAME,TYPE,SIZE,FSTYPE,MOUNTPOINTS"}},
		{"FindMntCommand", must(FindMntCommand(p)), "findmnt", nil},
		{"IPAddrCommand", must(IPAddrCommand(p)), "ip", []string{"-details", "addr", "show"}},
		{"IPRouteCommand", must(IPRouteCommand(p)), "ip", []string{"route", "show"}},
		{"SocketStatsCommand", must(SocketStatsCommand(p)), "ss", []string{"-lntup"}},
		{"ResolvConfCommand", must(ResolvConfCommand(p)), "cat", []string{"/etc/resolv.conf"}},
		{"ProcessListCommand", must(ProcessListCommand(p)), "ps", []string{"-eo", "pid,ppid,user,state,%cpu,%mem,etime,nlwp,comm,args"}},
		{"ServiceStatusCommand", must(ServiceStatusCommand(p, unit)), "systemctl", []string{
			"show", "nginx.service", "--no-pager",
			"--property=Id,Description,LoadState,ActiveState,SubState,MainPID,ExecMainStatus,Result,TasksCurrent,TasksMax,NRestarts,MemoryCurrent",
		}},
		{"RPMQueryCommand", must(RPMQueryCommand(p, pkg)), "rpm", []string{"-q", "libstdc++6"}},
		{"DpkgStatusCommand", must(DpkgStatusCommand(p, pkg)), "dpkg-query", []string{"-s", "libstdc++6"}},
		{"LogsCommand", must(LogsCommand(p, unit, since, lines)), "journalctl", []string{
			"-u", "nginx.service", "--since=-30m", "-n", "200", "--reverse", "--no-pager", "--output=short-iso",
		}},
		{"LogsRelativeWindowCommand", must(LogsRelativeWindowCommand(p, unit, windowSince, windowUntil, scan)), "journalctl", []string{
			"-u", "nginx.service", "--since=-150m", "--until=-2h", "-n", "100000", "--reverse", "--no-pager", "--output=short-iso",
		}},
		{"LogsSinceEpochCommand", must(LogsSinceEpochCommand(p, unit, from, scan)), "journalctl", []string{
			"-u", "nginx.service", "--since=@1795132800", "-n", "100000", "--reverse", "--no-pager", "--output=short-iso",
		}},
		{"LogsEpochWindowCommand", must(LogsEpochWindowCommand(p, unit, from, to, lines)), "journalctl", []string{
			"-u", "nginx.service", "--since=@1795132800", "--until=@1795219200", "-n", "200", "--reverse", "--no-pager", "--output=short-iso",
		}},
		{"KernelLogsCommand", must(KernelLogsCommand(p, kernelSince, lines)), "journalctl", []string{
			"-k", "--since=-1h", "-n", "200", "--reverse", "--no-pager", "--output=short-iso",
		}},
		{"FilesCommands/check", filesCheck, "ls", []string{
			"-d", "-n", "-b", "--time-style=full-iso", "/", "/var", "/var/log", "/var/log/nginx",
		}},
		{"FilesCommands/list", filesList, "ls", []string{
			"-A", "-b", "-g", "-o", "--time-style=full-iso", "--block-size=1", "/var/log/nginx",
		}},
		{"FilesCommands/check shallow", shallowCheck, "ls", []string{
			"-d", "-n", "-b", "--time-style=full-iso", "/", "/srv",
		}},
		{"FilesCommands/list shallow", shallowList, "ls", []string{
			"-A", "-b", "-g", "-o", "--time-style=full-iso", "--block-size=1", "/srv",
		}},
	}
}

// TestFactoryArgvIsExact is the golden argv test at the factory level: not
// "contains journalctl" but the whole token list, in order, with nothing else.
func TestFactoryArgvIsExact(t *testing.T) {
	for _, tc := range factoryCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cmd.Program(); got != tc.program {
				t.Errorf("Program() = %q, want %q", got, tc.program)
			}
			if got := tc.cmd.Args(); !slices.Equal(got, tc.args) {
				t.Errorf("Args()\n got: %q\nwant: %q", got, tc.args)
			}
		})
	}
}

// TestFactoryCasesCoverTheCatalogue keeps this table and the catalogue
// boundary_test.go guards from drifting apart: a factory added to commands.go
// and to allowedCommandFactories but never exercised here would otherwise ship
// an argv nobody wrote down.
func TestFactoryCasesCoverTheCatalogue(t *testing.T) {
	if problems := factoryRowProblems(factoryCases(t)); len(problems) > 0 {
		t.Errorf("factoryCases rows:\n%s", strings.Join(problems, "\n"))
	}
	var covered []string
	for _, tc := range factoryCases(t) {
		if name := tc.factoryName(); !slices.Contains(covered, name) {
			covered = append(covered, name)
		}
	}
	want := slices.Clone(allowedCommandFactories)
	sort.Strings(covered)
	sort.Strings(want)
	if !slices.Equal(covered, want) {
		t.Errorf("factoryCases covers\n %q\nallowedCommandFactories lists\n %q", covered, want)
	}
}

// multiCommandFactories names the factories that hand back more than one
// Command, with the parts each must have at least one row for. Every other
// factory has exactly one row, named after it.
var multiCommandFactories = map[string][]string{
	"FilesCommands": {"check", "list"},
}

// factoryRowProblems checks the shape of the rows before they are reduced to
// factory names: a factory outside multiCommandFactories has one row, named
// exactly as the factory, and a factory inside it has at least one row for
// each of its parts ("FilesCommands/check", "FilesCommands/check shallow").
func factoryRowProblems(cases []factoryCase) []string {
	rows := map[string][]string{}
	for _, tc := range cases {
		name, part, _ := strings.Cut(tc.name, "/")
		rows[name] = append(rows[name], part)
	}
	var problems []string
	for name, parts := range rows {
		want, multi := multiCommandFactories[name]
		if !multi {
			if len(parts) != 1 || parts[0] != "" {
				problems = append(problems, fmt.Sprintf("%s has rows %q, want exactly one named %s", name, parts, name))
			}
			continue
		}
		for _, w := range want {
			if !slices.ContainsFunc(parts, func(p string) bool { return p == w || strings.HasPrefix(p, w+" ") }) {
				problems = append(problems, fmt.Sprintf("%s has no row for its %s Command (rows %q)", name, w, parts))
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// TestFactoryRowProblemsCatchesBadRows feeds the row check the two mistakes it
// exists for.
func TestFactoryRowProblemsCatchesBadRows(t *testing.T) {
	cases := factoryCases(t)
	duplicated := append(slices.Clone(cases), cases[0])
	if len(factoryRowProblems(duplicated)) == 0 {
		t.Errorf("a second row for %s was accepted", cases[0].name)
	}
	oneSided := slices.DeleteFunc(slices.Clone(cases), func(tc factoryCase) bool {
		return strings.HasPrefix(tc.name, "FilesCommands/list")
	})
	if len(factoryRowProblems(oneSided)) == 0 {
		t.Error("FilesCommands without a list row was accepted")
	}
}

// TestFactoriesCarryTheResolvedSudoMode states that the privilege of every
// command still comes from the policy it was handed, now that there are many
// factories rather than one.
func TestFactoriesCarryTheResolvedSudoMode(t *testing.T) {
	for _, tc := range factoryCases(t) {
		if got := tc.cmd.Sudo(); got != policy.SudoNever {
			t.Errorf("%s built from the zero Policy carries sudo %s, want %s", tc.name, got, policy.SudoNever)
		}
	}
	must := commandBuilder(t)
	auto := mustPolicy(t, "logs", policy.SudoAuto)
	cmd := must(LogsCommand(auto, mustServiceName(t, "nginx.service"), mustDuration(t, "30m"), mustLineLimit(t, "200")))
	if got := cmd.Sudo(); got != policy.SudoAuto {
		t.Errorf("LogsCommand built from a resolved auto policy carries sudo %s, want %s", got, policy.SudoAuto)
	}
}

// TestFactoriesDoNotNormaliseUnitNames pins where unit-name normalisation is
// not: internal/ssh places the vo.ServiceName it was given into the argv
// verbatim. The single normaliser lives in internal/operation, so a name that
// arrives here without a suffix must leave here without one too — otherwise
// two layers would be appending ".service" and neither would own the rule.
func TestFactoriesDoNotNormaliseUnitNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		unit string
	}{
		{"bare name is left bare", "nginx"},
		{"service suffix is left alone", "nginx.service"},
		{"socket suffix is not rewritten", "docker.socket"},
		{"templated unit is left alone", "foo@bar.service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			must := commandBuilder(t)
			unit := mustServiceName(t, tc.unit)
			logs := must(LogsCommand(policy.Policy{}, unit, mustDuration(t, "30m"), mustLineLimit(t, "200")))
			if got := logs.Args()[1]; got != tc.unit {
				t.Errorf("LogsCommand put %q in the argv, want the unit name as given, %q", got, tc.unit)
			}
			status := must(ServiceStatusCommand(policy.Policy{}, unit))
			if got := status.Args()[1]; got != tc.unit {
				t.Errorf("ServiceStatusCommand put %q in the argv, want the unit name as given, %q", got, tc.unit)
			}
		})
	}
}

// TestFactoryArgvCarriesNoShellSyntax is the property behind the tables above:
// whatever a factory built, no token it produced can be read as shell syntax,
// and no token that came from a caller can be read as an option.
func TestFactoryArgvCarriesNoShellSyntax(t *testing.T) {
	callerSupplied := map[string]bool{
		"nginx.service":  true,
		"libstdc++6":     true,
		"30m":            true,
		"1h":             true,
		"200":            true,
		"100000":         true,
		"/":              true,
		"/var":           true,
		"/var/log":       true,
		"/var/log/nginx": true,
		"/srv":           true,
	}
	for _, tc := range factoryCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			for _, arg := range append([]string{tc.cmd.Program()}, tc.cmd.Args()...) {
				for i := 0; i < len(arg); i++ {
					if !literalSpec.chars[arg[i]] {
						t.Errorf("token %q has a byte at %d that no argv token may carry", arg, i)
					}
				}
				if callerSupplied[arg] && arg[0] == '-' {
					t.Errorf("caller-supplied token %q starts with a hyphen: it would be read as an option", arg)
				}
			}
		})
	}
}

// commandBuilder returns a helper that unwraps a factory's two results. It is
// a closure rather than a plain function because a factory call cannot be
// spread into a call that already has a *testing.T in front of it.
func commandBuilder(t *testing.T) func(Command, *result.Error) Command {
	t.Helper()
	return func(cmd Command, err *result.Error) Command {
		t.Helper()
		if err != nil {
			t.Fatalf("factory rejected its own arguments: %v", err)
		}
		return cmd
	}
}

func mustServiceName(t *testing.T, s string) vo.ServiceName {
	t.Helper()
	name, err := vo.ParseServiceName(s)
	if err != nil {
		t.Fatalf("vo.ParseServiceName(%q): %v", s, err)
	}
	return name
}

func mustPackageName(t *testing.T, s string) vo.PackageName {
	t.Helper()
	name, err := vo.ParsePackageName(s)
	if err != nil {
		t.Fatalf("vo.ParsePackageName(%q): %v", s, err)
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

// mustFilesPath matches s against a list holding /var and /srv, the parents of
// every path the tables here use.
func mustFilesPath(t *testing.T, s string) vo.FilesPath {
	t.Helper()
	list, err := vo.ParseFilesAllowList([]string{"/var", "/srv"})
	if err != nil {
		t.Fatalf("vo.ParseFilesAllowList: %v", err)
	}
	p, err := vo.ParseFilesPath(s, list)
	if err != nil {
		t.Fatalf("vo.ParseFilesPath(%q): %v", s, err)
	}
	return p
}

func mustFilesCommands(t *testing.T, p policy.Policy, dir vo.FilesPath) (Command, Command) {
	t.Helper()
	check, list, err := FilesCommands(p, dir)
	if err != nil {
		t.Fatalf("FilesCommands(%q) rejected its own argument: %v", dir.String(), err)
	}
	return check, list
}

// TestFilesCommandsRefuseAPathThatWasNeverMatched pins the zero FilesPath: a
// value that never went through vo.ParseFilesPath builds nothing.
func TestFilesCommandsRefuseAPathThatWasNeverMatched(t *testing.T) {
	check, list, err := FilesCommands(policy.Policy{}, vo.FilesPath{})
	if err == nil {
		t.Fatalf("FilesCommands accepted the zero FilesPath: %v %v", check.Args(), list.Args())
	}
	if !check.IsZero() || !list.IsZero() {
		t.Error("a refused FilesCommands handed back a Command")
	}
}

// TestFilesCommandsBuildBothFromOnePath pins that the check and the listing
// name the same directory: the listing's one path argument is the check's
// last.
func TestFilesCommandsBuildBothFromOnePath(t *testing.T) {
	dir := mustFilesPath(t, "/var/log/nginx")
	check, list := mustFilesCommands(t, policy.Policy{}, dir)
	checkArgs, listArgs := check.Args(), list.Args()
	if got, want := checkArgs[len(checkArgs)-1], listArgs[len(listArgs)-1]; got != want || got != dir.String() {
		t.Errorf("check ends with %q and the listing with %q, want both %q", got, want, dir.String())
	}
	if got, want := checkArgs[4:], dir.Chain(); !slices.Equal(got, want) {
		t.Errorf("check arguments %q, want the chain %q", got, want)
	}
	if check.Sudo() != policy.SudoNever || list.Sudo() != policy.SudoNever {
		t.Error("files commands carry a sudo mode from the zero policy")
	}
}

func mustTimestamp(t *testing.T, s string) vo.Timestamp {
	t.Helper()
	ts, err := vo.ParseTimestamp(s)
	if err != nil {
		t.Fatalf("vo.ParseTimestamp(%q): %v", s, err)
	}
	return ts
}

// TestEpochFactoriesRefuseTheZeroTimestamp pins that an instant which never
// went through internal/vo cannot become "--since=@" with nothing after it:
// the zero Timestamp renders no digits, and an empty value is not a token.
func TestEpochFactoriesRefuseTheZeroTimestamp(t *testing.T) {
	unit := mustServiceName(t, "nginx.service")
	lines := mustLineLimit(t, "200")
	ts := mustTimestamp(t, "2026-11-20T09:00:00+09:00")
	if _, err := LogsSinceEpochCommand(policy.Policy{}, unit, vo.Timestamp{}, lines); err == nil {
		t.Error("LogsSinceEpochCommand accepted the zero Timestamp")
	}
	if _, err := LogsEpochWindowCommand(policy.Policy{}, unit, ts, vo.Timestamp{}, lines); err == nil {
		t.Error("LogsEpochWindowCommand accepted the zero Timestamp as until")
	}
	if _, err := LogsRelativeWindowCommand(policy.Policy{}, unit, mustDuration(t, "1h"), vo.Duration{}, lines); err == nil {
		t.Error("LogsRelativeWindowCommand accepted the zero Duration as until")
	}
}
