package operation

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/sshtest"
	"github.com/youyo/focal/internal/vo"
)

// logsTestNow is the instant every test in this file reads as Focal's clock:
// 2026-11-25T14:00:00+09:00, which is 1795582800 seconds after the epoch.
var logsTestNow = time.Date(2026, 11, 25, 14, 0, 0, 0, time.FixedZone("JST", 9*60*60))

const logsTestNowUnix = 1795582800

// logsTestRegistry is a Registry whose clock is logsTestNow, so that every
// rule that reads the time can be checked to the second.
func logsTestRegistry() Registry {
	return Registry{enabled: func(string) bool { return true }, now: func() time.Time { return logsTestNow }}
}

func logsTestDuration(t *testing.T, v string) vo.Duration {
	t.Helper()
	d, err := vo.ParseDuration(v)
	if err != nil {
		t.Fatalf("ParseDuration(%q) = %v", v, err)
	}
	return d
}

// logsTestArgv builds params through the registry and runs the result against
// a Recorder, returning the argv after the program name.
func logsTestArgv(t *testing.T, r Registry, params Params) []string {
	t.Helper()
	op, err := r.Build("logs", params)
	if err != nil {
		t.Fatalf("Build(logs, %+v) = %v", params, err)
	}
	rec := &sshtest.Recorder{}
	if _, execErr := op.Execute(context.Background(), rec, journalTestTarget(t), policy.Policy{}); execErr != nil {
		t.Fatalf("Execute() = %v", execErr)
	}
	program, args := journalTestArgv(t, rec)
	if program != "journalctl" {
		t.Fatalf("program = %q, want journalctl", program)
	}
	return args
}

func TestLogsNowIsTheTestClock(t *testing.T) {
	if logsTestNow.Unix() != logsTestNowUnix {
		t.Fatalf("logsTestNow is %d, the tests below assume %d", logsTestNow.Unix(), logsTestNowUnix)
	}
}

// TestLogsWindowArgv covers every shape of window a logs request can name, as
// the argv that reaches the executor. A relative-only request keeps the remote
// clock; a request with an absolute bound has both bounds turned into
// instants on Focal's clock. A request that filters or counts scans the
// journal up to the scan limit rather than the caller's line count.
func TestLogsWindowArgv(t *testing.T) {
	r := logsTestRegistry()
	tail := func(n string) []string { return []string{"-n", n, "--reverse", "--no-pager", "--output=short-iso"} }
	unit := []string{"-u", "nginx.service"}

	for _, tc := range []struct {
		name   string
		params Params
		window []string
		n      string
	}{
		{"legacy request is unchanged", Params{}, []string{"--since=-30m"}, "200"},
		{"grep scans the default span", Params{Grep: "refused"}, []string{"--since=-30m"}, "100000"},
		{"grep-regex scans the default span", Params{GrepRegex: "re(fused|set)"}, []string{"--since=-30m"}, "100000"},
		{"count-by scans the default span", Params{CountBy: "hour"}, []string{"--since=-30m"}, "100000"},
		{"grep keeps a relative since relative", Params{Since: "6h", Grep: "refused", Lines: "50"}, []string{"--since=-6h"}, "100000"},
		{"relative since and until", Params{Since: "2h", Until: "1h"}, []string{"--since=-2h", "--until=-1h"}, "200"},
		{"equal relative bounds are allowed", Params{Since: "1h", Until: "1h"}, []string{"--since=-1h", "--until=-1h"}, "200"},
		{"relative until alone takes the default span before it", Params{Until: "2h"}, []string{"--since=-150m", "--until=-2h"}, "200"},
		{"relative until in seconds", Params{Until: "45s"}, []string{"--since=-1845s", "--until=-45s"}, "200"},
		{
			"relative until twelve days back stays in six digits",
			Params{Until: "12d", MaxSince: logsTestDuration(t, "30d")},
			[]string{"--since=-17310m", "--until=-12d"}, "200",
		},
		{"absolute since", Params{Since: "2026-11-25T09:00:00+09:00"}, []string{"--since=@1795564800"}, "200"},
		{"absolute since in UTC", Params{Since: "2026-11-25T00:00:00Z"}, []string{"--since=@1795564800"}, "200"},
		{"absolute since equal to now", Params{Since: "2026-11-25T05:00:00Z"}, []string{"--since=@1795582800"}, "200"},
		{
			"absolute since and until",
			Params{Since: "2026-11-25T09:00:00+09:00", Until: "2026-11-25T10:00:00+09:00"},
			[]string{"--since=@1795564800", "--until=@1795568400"}, "200",
		},
		{
			"until in the future is allowed",
			Params{Since: "2026-11-25T09:00:00+09:00", Until: "2026-11-26T09:00:00+09:00"},
			[]string{"--since=@1795564800", "--until=@1795651200"}, "200",
		},
		{
			"relative since with an absolute until is taken from Focal's clock",
			Params{Since: "1h", Until: "2026-11-25T13:30:00+09:00"},
			[]string{"--since=@1795579200", "--until=@1795581000"}, "200",
		},
		{
			"absolute since with a relative until is taken from Focal's clock",
			Params{Since: "2026-11-25T09:00:00+09:00", Until: "10m"},
			[]string{"--since=@1795564800", "--until=@1795582200"}, "200",
		},
		{
			"absolute until alone takes the default span before it",
			Params{Until: "2026-11-25T13:00:00+09:00"},
			[]string{"--since=@1795577400", "--until=@1795579200"}, "200",
		},
		{
			"absolute since with grep scans",
			Params{Since: "2026-11-25T09:00:00+09:00", Grep: "refused"},
			[]string{"--since=@1795564800"}, "100000",
		},
		{
			"absolute since exactly max_since back is allowed",
			Params{Since: "2026-11-24T14:00:00+09:00"},
			[]string{"--since=@1795496400"}, "200",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.params
			params.ServiceName = "nginx"
			if params.MaxSince == (vo.Duration{}) {
				params.MaxSince = journalTestMaxSince(t)
			}
			params.MaxLines = journalTestMaxLines(t)
			got := logsTestArgv(t, r, params)
			want := slices.Concat(unit, tc.window, tail(tc.n))
			if !slices.Equal(got, want) {
				t.Errorf("argv\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// TestLogsWindowRejects covers the requests a window cannot answer. Each is
// refused before anything runs, as a validation error naming the field.
func TestLogsWindowRejects(t *testing.T) {
	r := logsTestRegistry()
	for _, tc := range []struct {
		name   string
		params Params
		code   string
		field  string
	}{
		{"relative until before since", Params{Since: "1h", Until: "2h"}, "until_before_since", "until"},
		{
			"absolute until before since",
			Params{Since: "2026-11-25T10:00:00+09:00", Until: "2026-11-25T09:00:00+09:00"},
			"until_before_since", "until",
		},
		{
			"relative until before an absolute since",
			Params{Since: "2026-11-25T13:00:00+09:00", Until: "2h"},
			"until_before_since", "until",
		},
		{"absolute since in the future", Params{Since: "2026-11-25T14:00:01+09:00"}, "since_in_future", "since"},
		{
			"until alone so far ahead that the default since is in the future",
			Params{Until: "2026-11-25T15:00:00+09:00"},
			"since_in_future", "since",
		},
		{"absolute since one second past max_since", Params{Since: "2026-11-24T13:59:59+09:00"}, "since_above_max", "since"},
		{"relative since past max_since with an absolute until", Params{Since: "25h", Until: "2026-11-25T13:00:00+09:00"}, "since_above_max", "since"},
		{"relative until whose default since passes max_since", Params{Until: "24h"}, "since_above_max", "since"},
		{"absolute until whose default since passes max_since", Params{Until: "2026-11-24T14:20:00+09:00"}, "since_above_max", "since"},
		{"grep and grep-regex together", Params{Grep: "a", GrepRegex: "b"}, "conflicting_arguments", "grep-regex"},
		{"timestamp without an offset", Params{Since: "2026-11-25T09:00:00"}, "invalid_timestamp", "timestamp"},
		{"timestamp with fractional seconds", Params{Until: "2026-11-25T09:00:00.5+09:00"}, "invalid_timestamp", "timestamp"},
		{"until that is neither form", Params{Until: "yesterday"}, "invalid_duration", "duration"},
		{"unknown count-by", Params{CountBy: "week"}, "invalid_count_by", "count_by"},
		{"grep with a control character", Params{Grep: "a\nb"}, "invalid_log_pattern", "log_pattern"},
		{"grep-regex that does not compile", Params{GrepRegex: "("}, "invalid_log_regex", "log_regex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.params
			params.ServiceName = "nginx"
			params.MaxSince = journalTestMaxSince(t)
			params.MaxLines = journalTestMaxLines(t)
			_, err := r.Build("logs", params)
			if err == nil {
				t.Fatalf("Build(logs, %+v) succeeded, want %s", params, tc.code)
			}
			if err.Kind != result.KindValidation || err.Code != tc.code || err.Field != tc.field {
				t.Errorf("error = %s/%s on %q, want %s/%s on %q (%s)",
					err.Kind, err.Code, err.Field, result.KindValidation, tc.code, tc.field, err.Message)
			}
		})
	}
}

// TestLogsAbsoluteSinceWithoutACeilingStopsAtThirtyDays mirrors the relative
// rule: with no max_since configured, vo.ParseDuration's own 30d bound is the
// only cap, and an absolute since is held to the same distance from now.
func TestLogsAbsoluteSinceWithoutACeilingStopsAtThirtyDays(t *testing.T) {
	r := logsTestRegistry()
	if _, err := r.Build("logs", Params{ServiceName: "nginx", Since: "2026-10-26T14:00:00+09:00"}); err != nil {
		t.Fatalf("Build(logs) 30 days back with no ceiling = %v", err)
	}
	_, err := r.Build("logs", Params{ServiceName: "nginx", Since: "2026-10-26T13:59:59+09:00"})
	if err == nil || err.Code != "since_above_max" {
		t.Fatalf("Build(logs) past 30 days with no ceiling = %v, want since_above_max", err)
	}
}

// TestLogsDerivedSinceThatNoUnitCanWriteIsRefused pins the one span the
// default-before-until rule cannot express in an argv: an until in seconds
// whose sum with the default needs more than six digits of every unit that
// divides it.
func TestLogsDerivedSinceThatNoUnitCanWriteIsRefused(t *testing.T) {
	r := logsTestRegistry()
	_, err := r.Build("logs", Params{ServiceName: "nginx", Until: "999999s", MaxSince: logsTestDuration(t, "30d")})
	if err == nil || err.Code != "invalid_duration" {
		t.Fatalf("Build(logs, until 999999s) = %v, want invalid_duration", err)
	}
}

// TestKernelSinceStaysRelative pins that the absolute form is logs' alone:
// kernel's --since is still read by vo.ParseDuration only.
func TestKernelSinceStaysRelative(t *testing.T) {
	r := logsTestRegistry()
	_, err := r.Build("kernel", Params{Since: "2026-11-25T09:00:00+09:00"})
	if err == nil || err.Code != "invalid_duration" {
		t.Fatalf("Build(kernel, absolute since) = %v, want invalid_duration", err)
	}
}

// TestRegistryReadsTheClockOnEveryBuild keeps a long-lived Registry — the MCP
// server holds one for its whole life — from answering with the time it was
// created at.
func TestRegistryReadsTheClockOnEveryBuild(t *testing.T) {
	calls := 0
	r := Registry{enabled: func(string) bool { return true }, now: func() time.Time {
		calls++
		return logsTestNow
	}}
	for range 2 {
		if _, err := r.Build("logs", Params{ServiceName: "nginx", Since: "2026-11-25T09:00:00+09:00"}); err != nil {
			t.Fatalf("Build(logs) = %v", err)
		}
	}
	if calls != 2 {
		t.Errorf("clock read %d times over two builds, want 2", calls)
	}
}
