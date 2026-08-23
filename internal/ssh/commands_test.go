package ssh

import (
	"slices"
	"sort"
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
		{"ProcessListCommand", must(ProcessListCommand(p)), "ps", []string{"-eo", "pid,ppid,user,state,%cpu,%mem,etime,comm,args"}},
		{"ServiceStatusCommand", must(ServiceStatusCommand(p, unit)), "systemctl", []string{
			"show", "nginx.service", "--no-pager",
			"--property=Id,Description,LoadState,ActiveState,SubState,MainPID,ExecMainStatus,Result",
		}},
		{"LogsCommand", must(LogsCommand(p, unit, since, lines)), "journalctl", []string{
			"-u", "nginx.service", "--since=-30m", "-n", "200", "--no-pager", "--output=short-iso",
		}},
		{"KernelLogsCommand", must(KernelLogsCommand(p, kernelSince, lines)), "journalctl", []string{
			"-k", "--since=-1h", "-n", "200", "--no-pager", "--output=short-iso",
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
	var covered []string
	for _, tc := range factoryCases(t) {
		covered = append(covered, tc.name)
	}
	want := slices.Clone(allowedCommandFactories)
	sort.Strings(covered)
	sort.Strings(want)
	if !slices.Equal(covered, want) {
		t.Errorf("factoryCases covers\n %q\nallowedCommandFactories lists\n %q", covered, want)
	}
}

// TestFactoriesCarryTheResolvedSudoMode states that the privilege of every
// command still comes from the policy it was handed, now that there are twenty
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
		"nginx.service": true,
		"30m":           true,
		"1h":            true,
		"200":           true,
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
