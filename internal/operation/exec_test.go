// exec_test.go exercises exec.go's unexported helpers directly (buildPart,
// aggregate, normalizeUnitName), the way command_test.go in internal/ssh
// tests newCommand: these are the shared machinery s4/s5/s6 build on, not
// part of the exported Operation surface, so there is nothing to reach them
// with from outside the package.
package operation

import (
	"errors"
	"testing"
	"time"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
)

func TestBuildPartOK(t *testing.T) {
	out := ssh.Output{Stdout: "up 3 days", ExitCode: 0, Duration: 250 * time.Millisecond}

	p := buildPart("uptime", out, nil)

	if p.Name != "uptime" {
		t.Errorf("Name = %q, want uptime", p.Name)
	}
	if p.Status != result.StatusOK {
		t.Errorf("Status = %q, want ok", p.Status)
	}
	if p.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", p.ExitCode)
	}
	if p.DurationMs != 250 {
		t.Errorf("DurationMs = %d, want 250", p.DurationMs)
	}
	if p.Stdout != "up 3 days" {
		t.Errorf("Stdout = %q, want %q", p.Stdout, "up 3 days")
	}
	if p.Error != nil {
		t.Errorf("Error = %+v, want nil", p.Error)
	}
}

func TestBuildPartNonZeroExitIsFailedNotAnError(t *testing.T) {
	out := ssh.Output{Stdout: "", Stderr: "no such unit", ExitCode: 3, Duration: 10 * time.Millisecond}

	p := buildPart("service", out, nil)

	if p.Status != result.StatusFailed {
		t.Errorf("Status = %q, want failed", p.Status)
	}
	if p.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", p.ExitCode)
	}
	if p.Error != nil {
		t.Errorf("Error = %+v, want nil: a non-zero exit code is not an execution error", p.Error)
	}
}

func TestBuildPartTruncated(t *testing.T) {
	out := ssh.Output{Stdout: "partial", ExitCode: 0, Truncated: true, Duration: 5 * time.Second}

	p := buildPart("logs", out, nil)

	if p.Status != result.StatusTruncated {
		t.Errorf("Status = %q, want truncated", p.Status)
	}
	if !p.Truncated {
		t.Error("Truncated = false, want true")
	}
	if p.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 (captured before cutoff)", p.ExitCode)
	}
}

func TestBuildPartTimeout(t *testing.T) {
	execErr := result.ExecutionError("timeout", "command timed out after 30s")
	if !ssh.TimedOut(execErr) {
		t.Fatalf("test setup: ssh.TimedOut did not recognize %v as a timeout", execErr)
	}

	p := buildPart("kernel", ssh.Output{}, execErr)

	if p.Status != result.StatusTimeout {
		t.Errorf("Status = %q, want timeout", p.Status)
	}
	if p.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", p.ExitCode)
	}
	if p.Error == nil || p.Error.Code != "timeout" {
		t.Errorf("Error = %+v, want the timeout Error attached", p.Error)
	}
}

func TestBuildPartExecutionFailureIsFailedWithError(t *testing.T) {
	execErr := result.ExecutionError("ssh_failed", "ssh: connection refused")

	p := buildPart("system", ssh.Output{}, execErr)

	if p.Status != result.StatusFailed {
		t.Errorf("Status = %q, want failed", p.Status)
	}
	if p.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 (no status was ever reached)", p.ExitCode)
	}
	if p.Error == nil || p.Error.Code != "ssh_failed" {
		t.Errorf("Error = %+v, want ssh_failed attached", p.Error)
	}
}

func TestBuildPartWrapsAnUnstructuredError(t *testing.T) {
	execErr := errors.New("boom")

	p := buildPart("system", ssh.Output{}, execErr)

	if p.Status != result.StatusFailed {
		t.Errorf("Status = %q, want failed", p.Status)
	}
	if p.Error == nil || p.Error.Kind != result.KindExecution {
		t.Fatalf("Error = %+v, want a KindExecution Error even for an unstructured error", p.Error)
	}
	if p.Error.Message != "boom" {
		t.Errorf("Error.Message = %q, want %q", p.Error.Message, "boom")
	}
}

func TestAggregateStatusPriority(t *testing.T) {
	cases := []struct {
		name  string
		parts []result.Part
		want  result.Status
	}{
		{
			name:  "all ok",
			parts: []result.Part{{Status: result.StatusOK}, {Status: result.StatusOK}},
			want:  result.StatusOK,
		},
		{
			name:  "truncated beats ok",
			parts: []result.Part{{Status: result.StatusOK}, {Status: result.StatusTruncated}},
			want:  result.StatusTruncated,
		},
		{
			name:  "failed beats truncated",
			parts: []result.Part{{Status: result.StatusTruncated}, {Status: result.StatusFailed}},
			want:  result.StatusFailed,
		},
		{
			name:  "timeout beats failed",
			parts: []result.Part{{Status: result.StatusFailed}, {Status: result.StatusTimeout}},
			want:  result.StatusTimeout,
		},
		{
			name:  "timeout beats everything regardless of order",
			parts: []result.Part{{Status: result.StatusTimeout}, {Status: result.StatusOK}, {Status: result.StatusFailed}, {Status: result.StatusTruncated}},
			want:  result.StatusTimeout,
		},
		{
			name:  "no parts is ok",
			parts: nil,
			want:  result.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, _, _ := aggregate(tc.parts)
			if status != tc.want {
				t.Errorf("aggregate status = %q, want %q", status, tc.want)
			}
		})
	}
}

func TestAggregateExitCodeIsFirstNonZero(t *testing.T) {
	parts := []result.Part{
		{Status: result.StatusOK, ExitCode: 0},
		{Status: result.StatusFailed, ExitCode: 5},
		{Status: result.StatusFailed, ExitCode: 9},
	}
	_, exitCode, _, _ := aggregate(parts)
	if exitCode != 5 {
		t.Errorf("exitCode = %d, want 5 (the first non-zero, not the last)", exitCode)
	}
}

func TestAggregateExitCodeAllZeroIsZero(t *testing.T) {
	parts := []result.Part{{ExitCode: 0}, {ExitCode: 0}}
	_, exitCode, _, _ := aggregate(parts)
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
}

func TestAggregateDurationIsSum(t *testing.T) {
	parts := []result.Part{{DurationMs: 100}, {DurationMs: 250}, {DurationMs: 5}}
	_, _, durationMs, _ := aggregate(parts)
	if durationMs != 355 {
		t.Errorf("durationMs = %d, want 355", durationMs)
	}
}

func TestAggregateTruncatedIfAnyPart(t *testing.T) {
	cases := []struct {
		name  string
		parts []result.Part
		want  bool
	}{
		{name: "none truncated", parts: []result.Part{{Truncated: false}, {Truncated: false}}, want: false},
		{name: "one truncated", parts: []result.Part{{Truncated: false}, {Truncated: true}}, want: true},
		{name: "truncated part is ok status", parts: []result.Part{{Status: result.StatusOK, Truncated: true}}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, truncated := aggregate(tc.parts)
			if truncated != tc.want {
				t.Errorf("truncated = %v, want %v", truncated, tc.want)
			}
		})
	}
}

func TestNormalizeUnitName(t *testing.T) {
	cases := map[string]string{
		"nginx":            "nginx.service",
		"nginx.service":    "nginx.service",
		"foo@bar.service":  "foo@bar.service",
		"docker.socket":    "docker.socket",
		"logrotate.timer":  "logrotate.timer",
		"cron.target":      "cron.target",
		"boot.mount":       "boot.mount",
		"foo.path":         "foo.path",
		"system.slice":     "system.slice",
		"init.scope":       "init.scope",
		"sda1.device":      "sda1.device",
		"zram0.swap":       "zram0.swap",
		"boot.automount":   "boot.automount",
		"myapp":            "myapp.service",
		"my-app_1.service": "my-app_1.service",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := normalizeUnitName(in); got != want {
				t.Errorf("normalizeUnitName(%q) = %q, want %q", in, got, want)
			}
		})
	}
}
