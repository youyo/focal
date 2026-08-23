// processes_test.go is an internal test: the local filter is the substance of
// this operation and it is unexported, so the row-selection rules are asserted
// against processesFilter directly, the way exec_test.go asserts buildPart.
// What crosses the package boundary — the argv and the Envelope — is asserted
// through internal/sshtest's Recorder in the same file.
package operation

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
	"github.com/youyo/focal/internal/vo"
)

// processesSample is one ps(1) response shaped exactly as the fixed argv asks
// for it: pid,ppid,user,state,%cpu,%mem,etime,comm,args, header row first.
const processesSample = `    PID    PPID USER     S %CPU %MEM     ELAPSED COMMAND         COMMAND
      1       0 root     S  0.0  0.1 10-02:11:33 systemd         /sbin/init
    412       1 root     S  0.0  0.3    02:11:33 nginx           nginx: master process
    998     412 www-data S  1.5  0.9    01:00:00 nginx           nginx: worker process
   1500       1 postgres S  0.2  2.0    00:30:00 postgres_export /usr/bin/postgres_exporter
`

const processesHeader = `    PID    PPID USER     S %CPU %MEM     ELAPSED COMMAND         COMMAND
`

func processesTarget(t *testing.T) vo.Target {
	t.Helper()
	target, err := vo.ParseTarget("example.com")
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	return target
}

func processesPolicy(t *testing.T) policy.Policy {
	t.Helper()
	p, err := policy.Resolve("processes", policy.SudoNever)
	if err != nil {
		t.Fatalf("policy.Resolve: %v", err)
	}
	return p
}

func processesMustNew(t *testing.T, name, pid string) Processes {
	t.Helper()
	op, err := NewProcesses(name, pid)
	if err != nil {
		t.Fatalf("NewProcesses(%q, %q): %v", name, pid, err)
	}
	return op
}

// TestProcessesFieldsAreTyped fixes the rule that made this step's own value
// object necessary: the name filter is a vo.ProcessName, not the vo.ServiceName
// the service operation carries and not a raw string.
func TestProcessesFieldsAreTyped(t *testing.T) {
	typ := reflect.TypeOf(Processes{})
	want := map[string]reflect.Type{
		"name": reflect.TypeOf(vo.ProcessName{}),
		"pid":  reflect.TypeOf(vo.PID{}),
	}
	if typ.NumField() != len(want) {
		t.Fatalf("Processes has %d fields, want %d (no raw filter string may be stored)", typ.NumField(), len(want))
	}
	for i := range typ.NumField() {
		field := typ.Field(i)
		wantType, ok := want[field.Name]
		if !ok {
			t.Errorf("unexpected field %q of type %s", field.Name, field.Type)
			continue
		}
		if field.Type != wantType {
			t.Errorf("field %q is %s, want %s", field.Name, field.Type, wantType)
		}
	}
}

func TestNewProcessesAcceptsAllFourForms(t *testing.T) {
	cases := []struct {
		label    string
		name     string
		pid      string
		wantName string
		wantPID  string
	}{
		{label: "neither", name: "", pid: "", wantName: "", wantPID: ""},
		{label: "name only", name: "nginx", pid: "", wantName: "nginx", wantPID: ""},
		{label: "pid only", name: "", pid: "412", wantName: "", wantPID: "412"},
		{label: "both", name: "nginx", pid: "412", wantName: "nginx", wantPID: "412"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			op := processesMustNew(t, tc.name, tc.pid)
			if got := op.name.String(); got != tc.wantName {
				t.Errorf("name = %q, want %q", got, tc.wantName)
			}
			if got := op.pid.String(); got != tc.wantPID {
				t.Errorf("pid = %q, want %q", got, tc.wantPID)
			}
		})
	}
}

func TestNewProcessesRejectsBadInput(t *testing.T) {
	cases := []struct {
		label     string
		name      string
		pid       string
		wantField string
	}{
		{label: "injection in name", name: "nginx;id", pid: "", wantField: "process_name"},
		{label: "command substitution in name", name: "$(id)", pid: "", wantField: "process_name"},
		{label: "traversal in name", name: "../../etc/passwd", pid: "", wantField: "process_name"},
		{label: "overlong name", name: strings.Repeat("a", 65), pid: "", wantField: "process_name"},
		{label: "non numeric pid", name: "", pid: "1;id", wantField: "pid"},
		{label: "zero pid", name: "", pid: "0", wantField: "pid"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, err := NewProcesses(tc.name, tc.pid)
			if err == nil {
				t.Fatalf("NewProcesses(%q, %q) accepted, want rejected", tc.name, tc.pid)
			}
			if err.Kind != result.KindValidation {
				t.Errorf("Kind = %q, want %q", err.Kind, result.KindValidation)
			}
			if err.Field != tc.wantField {
				t.Errorf("Field = %q, want %q", err.Field, tc.wantField)
			}
		})
	}
}

// TestProcessesArgvIsFixedForEveryFilter is the "filtering should happen
// locally" contract: however a caller narrows the answer, the remote host is
// asked the same question. No part of a filter is ever composed into an argv.
func TestProcessesArgvIsFixedForEveryFilter(t *testing.T) {
	wantArgs := []string{"-eo", "pid,ppid,user,state,%cpu,%mem,etime,comm,args"}
	cases := []struct {
		label string
		name  string
		pid   string
	}{
		{label: "neither", name: "", pid: ""},
		{label: "name only", name: "nginx", pid: ""},
		{label: "pid only", name: "", pid: "412"},
		{label: "both", name: "nginx", pid: "412"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			rec := &sshtest.Recorder{}
			rec.Respond(ssh.Output{Stdout: processesSample, ExitCode: 0, Duration: 40 * time.Millisecond}, nil)

			op := processesMustNew(t, tc.name, tc.pid)
			if _, err := op.Execute(context.Background(), rec, processesTarget(t), processesPolicy(t)); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			calls := rec.Calls()
			if len(calls) != 1 {
				t.Fatalf("got %d recorded calls, want exactly 1", len(calls))
			}
			cmd := calls[0].Command
			if cmd.Program() != "ps" {
				t.Errorf("Program() = %q, want ps", cmd.Program())
			}
			if !reflect.DeepEqual(cmd.Args(), wantArgs) {
				t.Errorf("Args() = %v, want %v", cmd.Args(), wantArgs)
			}
			if cmd.Sudo() != policy.SudoNever {
				t.Errorf("Sudo() = %v, want SudoNever", cmd.Sudo())
			}
		})
	}
}

func TestProcessesFilterSelectsRows(t *testing.T) {
	cases := []struct {
		label string
		name  string
		pid   string
		want  string
	}{
		{
			label: "no filter keeps everything verbatim",
			want:  processesSample,
		},
		{
			label: "name matches the comm column",
			name:  "nginx",
			want: processesHeader +
				"    412       1 root     S  0.0  0.3    02:11:33 nginx           nginx: master process\n" +
				"    998     412 www-data S  1.5  0.9    01:00:00 nginx           nginx: worker process\n",
		},
		{
			label: "name is a substring match on comm",
			name:  "post",
			want: processesHeader +
				"   1500       1 postgres S  0.2  2.0    00:30:00 postgres_export /usr/bin/postgres_exporter\n",
		},
		{
			label: "name does not match the args column",
			name:  "sbin",
			want:  processesHeader,
		},
		{
			label: "pid is an exact match on the pid column",
			pid:   "1",
			want: processesHeader +
				"      1       0 root     S  0.0  0.1 10-02:11:33 systemd         /sbin/init\n",
		},
		{
			label: "pid does not match on a prefix",
			pid:   "15",
			want:  processesHeader,
		},
		{
			label: "name and pid must both match",
			name:  "nginx",
			pid:   "998",
			want: processesHeader +
				"    998     412 www-data S  1.5  0.9    01:00:00 nginx           nginx: worker process\n",
		},
		{
			label: "no row matches both",
			name:  "nginx",
			pid:   "1",
			want:  processesHeader,
		},
		{
			label: "nothing matches at all",
			name:  "redis",
			want:  processesHeader,
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := processesFilter(processesSample, tc.name, tc.pid)
			if got != tc.want {
				t.Errorf("processesFilter(...) =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestProcessesFilterKeepsHeaderAndDropsShortRows(t *testing.T) {
	stdout := processesHeader + "garbage\n"
	if got := processesFilter(stdout, "garbage", ""); got != processesHeader {
		t.Errorf("got %q, want just the header: a row without the nine expected columns cannot be matched", got)
	}
	if got := processesFilter("", "nginx", ""); got != "" {
		t.Errorf("got %q, want empty for empty stdout", got)
	}
}

func TestProcessesEnvelopeReportsFilteredRows(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{Stdout: processesSample, ExitCode: 0, Duration: 120 * time.Millisecond}, nil)

	op := processesMustNew(t, "nginx", "")
	target := processesTarget(t)
	env, err := op.Execute(context.Background(), rec, target, processesPolicy(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if env.Operation != "processes" {
		t.Errorf("Operation = %q, want processes", env.Operation)
	}
	if env.Host != target.String() {
		t.Errorf("Host = %q, want %q", env.Host, target.String())
	}
	if env.Status != result.StatusOK {
		t.Errorf("Status = %q, want ok", env.Status)
	}
	if env.DurationMs != 120 {
		t.Errorf("DurationMs = %d, want 120", env.DurationMs)
	}
	if strings.Contains(env.Stdout, "postgres_export") {
		t.Errorf("Stdout still carries an unmatched row:\n%s", env.Stdout)
	}
	if !strings.HasPrefix(env.Stdout, processesHeader) {
		t.Errorf("Stdout lost its header row:\n%q", env.Stdout)
	}
	if len(env.Parts) != 0 {
		t.Errorf("Parts = %v, want empty for a single-command operation", env.Parts)
	}
}

// TestProcessesTruncatedSurvivesAnEmptyFilterResult is N6. A caller that asked
// for one process and got back only a header has to be able to tell "that
// process is not running" from "the answer was cut off before we could see it".
// The local filter runs after truncation, so the only thing that distinguishes
// them is that Truncated is carried through to the Envelope and aggregated into
// Status — regardless of how many rows survived the filter.
func TestProcessesTruncatedSurvivesAnEmptyFilterResult(t *testing.T) {
	cases := []struct {
		label      string
		name       string
		wantRows   int
		wantStatus result.Status
	}{
		{label: "filter matched nothing", name: "redis", wantRows: 1, wantStatus: result.StatusTruncated},
		{label: "filter matched rows", name: "nginx", wantRows: 3, wantStatus: result.StatusTruncated},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			rec := &sshtest.Recorder{}
			rec.Respond(ssh.Output{
				Stdout:    processesSample,
				ExitCode:  0,
				Truncated: true,
				Duration:  90 * time.Millisecond,
			}, nil)

			op := processesMustNew(t, tc.name, "")
			env, err := op.Execute(context.Background(), rec, processesTarget(t), processesPolicy(t))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}

			if !env.Truncated {
				t.Error("Truncated = false, want true: the local filter must not erase the cutoff")
			}
			if env.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", env.Status, tc.wantStatus)
			}
			if got := len(strings.Split(strings.TrimSuffix(env.Stdout, "\n"), "\n")); got != tc.wantRows {
				t.Errorf("filtered stdout has %d rows, want %d", got, tc.wantRows)
			}
		})
	}
}

func TestProcessesNonZeroExitIsFailed(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{Stderr: "ps: command not found", ExitCode: 127, Duration: time.Millisecond}, nil)

	op := processesMustNew(t, "", "")
	env, err := op.Execute(context.Background(), rec, processesTarget(t), processesPolicy(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if env.Status != result.StatusFailed {
		t.Errorf("Status = %q, want failed", env.Status)
	}
	if env.ExitCode != 127 {
		t.Errorf("ExitCode = %d, want 127", env.ExitCode)
	}
}

func TestProcessesExecutionFailureIsReturnedAsError(t *testing.T) {
	execErr := result.ExecutionError("ssh_failed", "ssh: connection refused")
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{}, execErr)

	op := processesMustNew(t, "", "")
	if _, err := op.Execute(context.Background(), rec, processesTarget(t), processesPolicy(t)); err == nil {
		t.Fatal("Execute returned no error, want the execution failure")
	}
}
