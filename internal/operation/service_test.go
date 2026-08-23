// service_test.go is an internal test for the same reason processes_test.go
// is: the KEY=VALUE parse and the property allowlist it enforces are
// unexported, and they are the part of this operation worth pinning down.
package operation

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/sshtest"
	"github.com/youyo/focal/internal/vo"
)

// serviceSample is systemctl show output for the requested properties, with
// one property the remote host volunteered that Focal did not ask for.
const serviceSample = `Id=nginx.service
Description=A high performance web server
LoadState=loaded
ActiveState=active
SubState=running
MainPID=412
ExecMainStatus=0
Result=success
Environment=SECRET=hunter2
FragmentPath=/lib/systemd/system/nginx.service
`

func serviceTarget(t *testing.T) vo.Target {
	t.Helper()
	target, err := vo.ParseTarget("example.com")
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	return target
}

func servicePolicy(t *testing.T) policy.Policy {
	t.Helper()
	p, err := policy.Resolve("service", policy.SudoNever)
	if err != nil {
		t.Fatalf("policy.Resolve: %v", err)
	}
	return p
}

func serviceMustNew(t *testing.T, name string) Service {
	t.Helper()
	op, err := NewService(name)
	if err != nil {
		t.Fatalf("NewService(%q): %v", name, err)
	}
	return op
}

// TestServiceFieldIsAServiceName fixes that the operation carries the unit as a
// value object and nothing else — no raw name, no pre-built Command.
func TestServiceFieldIsAServiceName(t *testing.T) {
	typ := reflect.TypeOf(Service{})
	if typ.NumField() != 1 {
		t.Fatalf("Service has %d fields, want 1", typ.NumField())
	}
	if got := typ.Field(0).Type; got != reflect.TypeOf(vo.ServiceName{}) {
		t.Errorf("field %q is %s, want vo.ServiceName", typ.Field(0).Name, got)
	}
}

func TestNewServiceRejectsBadInput(t *testing.T) {
	for _, in := range []string{"", "nginx;id", "$(id)", "../../etc/passwd", "-nginx", "nginx id"} {
		t.Run(in, func(t *testing.T) {
			if _, err := NewService(in); err == nil {
				t.Fatalf("NewService(%q) accepted, want rejected", in)
			} else if err.Kind != result.KindValidation {
				t.Errorf("Kind = %q, want %q", err.Kind, result.KindValidation)
			}
		})
	}
}

// TestServiceArgv is the golden argv: the whole invocation, in order, for a
// bare name and for a name that already carries a unit suffix. The suffix rule
// itself belongs to normalizeUnitName in exec.go; what this asserts is that
// service.go goes through it rather than appending ".service" of its own.
func TestServiceArgv(t *testing.T) {
	const properties = "--property=Id,Description,LoadState,ActiveState,SubState,MainPID,ExecMainStatus,Result"
	cases := []struct {
		in       string
		wantUnit string
	}{
		{in: "nginx", wantUnit: "nginx.service"},
		{in: "nginx.service", wantUnit: "nginx.service"},
		{in: "docker.socket", wantUnit: "docker.socket"},
		{in: "logrotate.timer", wantUnit: "logrotate.timer"},
		{in: "foo@bar.service", wantUnit: "foo@bar.service"},
		{in: "myapp", wantUnit: "myapp.service"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			rec := &sshtest.Recorder{}
			rec.Respond(ssh.Output{Stdout: serviceSample, ExitCode: 0, Duration: 30 * time.Millisecond}, nil)

			op := serviceMustNew(t, tc.in)
			if _, err := op.Execute(context.Background(), rec, serviceTarget(t), servicePolicy(t)); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			calls := rec.Calls()
			if len(calls) != 1 {
				t.Fatalf("got %d recorded calls, want exactly 1", len(calls))
			}
			cmd := calls[0].Command
			if cmd.Program() != "systemctl" {
				t.Errorf("Program() = %q, want systemctl", cmd.Program())
			}
			want := []string{"show", tc.wantUnit, "--no-pager", properties}
			if !reflect.DeepEqual(cmd.Args(), want) {
				t.Errorf("Args() =\n%v\nwant\n%v", cmd.Args(), want)
			}
			if cmd.Sudo() != policy.SudoNever {
				t.Errorf("Sudo() = %v, want SudoNever", cmd.Sudo())
			}
		})
	}
}

// TestServicePropertiesMatchTheArgv keeps the allowlist Data is filtered
// through and the --property list the remote host is asked for from drifting
// apart: adding a property to one without the other fails here rather than
// silently dropping it from the output or admitting an unrequested key.
func TestServicePropertiesMatchTheArgv(t *testing.T) {
	cmd, err := ssh.ServiceStatusCommand(servicePolicy(t), serviceMustNew(t, "nginx").unit)
	if err != nil {
		t.Fatalf("ssh.ServiceStatusCommand: %v", err)
	}

	var requested []string
	for _, arg := range cmd.Args() {
		if after, ok := strings.CutPrefix(arg, "--property="); ok {
			requested = strings.Split(after, ",")
		}
	}
	if len(requested) == 0 {
		t.Fatalf("no --property argument in %v", cmd.Args())
	}

	slices.Sort(requested)
	allowed := slices.Sorted(maps.Keys(serviceProperties))
	if !slices.Equal(requested, allowed) {
		t.Errorf("serviceProperties = %v, but the argv requests %v", allowed, requested)
	}
}

func TestServiceParseKeepsOnlyRequestedProperties(t *testing.T) {
	data := serviceParse(serviceSample)

	want := map[string]string{
		"Id":             "nginx.service",
		"Description":    "A high performance web server",
		"LoadState":      "loaded",
		"ActiveState":    "active",
		"SubState":       "running",
		"MainPID":        "412",
		"ExecMainStatus": "0",
		"Result":         "success",
	}
	if !reflect.DeepEqual(data, want) {
		t.Errorf("serviceParse(...) =\n%v\nwant\n%v", data, want)
	}
	for _, unwanted := range []string{"Environment", "FragmentPath"} {
		if _, ok := data[unwanted]; ok {
			t.Errorf("Data carries %q, which the argv never asked for", unwanted)
		}
	}
}

func TestServiceParseEdgeCases(t *testing.T) {
	cases := []struct {
		label  string
		stdout string
		want   map[string]string
	}{
		{
			label:  "a value may contain the separator",
			stdout: "Description=nginx --with-x=y\n",
			want:   map[string]string{"Description": "nginx --with-x=y"},
		},
		{
			label:  "an empty value is still a value",
			stdout: "Result=\n",
			want:   map[string]string{"Result": ""},
		},
		{
			label:  "a line without a separator is not a property",
			stdout: "Id\nLoadState=loaded\n",
			want:   map[string]string{"LoadState": "loaded"},
		},
		{
			label:  "nothing recognizable yields no Data at all",
			stdout: "Failed to get unit: No such unit\n",
			want:   nil,
		},
		{
			label:  "empty output yields no Data at all",
			stdout: "",
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := serviceParse(tc.stdout); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("serviceParse(%q) = %v, want %v", tc.stdout, got, tc.want)
			}
		})
	}
}

// TestServiceEnvelopeKeepsRawStdoutAlongsideData fixes that parsing into Data
// is additive: the raw output a human would read is still there.
func TestServiceEnvelopeKeepsRawStdoutAlongsideData(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{Stdout: serviceSample, ExitCode: 0, Duration: 75 * time.Millisecond}, nil)

	op := serviceMustNew(t, "nginx")
	target := serviceTarget(t)
	env, err := op.Execute(context.Background(), rec, target, servicePolicy(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if env.Operation != "service" {
		t.Errorf("Operation = %q, want service", env.Operation)
	}
	if env.Host != target.String() {
		t.Errorf("Host = %q, want %q", env.Host, target.String())
	}
	if env.Status != result.StatusOK {
		t.Errorf("Status = %q, want ok", env.Status)
	}
	if env.DurationMs != 75 {
		t.Errorf("DurationMs = %d, want 75", env.DurationMs)
	}
	if env.Stdout != serviceSample {
		t.Errorf("Stdout = %q, want the raw output verbatim", env.Stdout)
	}
	if env.Data["ActiveState"] != "active" {
		t.Errorf("Data[ActiveState] = %q, want active", env.Data["ActiveState"])
	}
	if len(env.Data) != len(serviceProperties) {
		t.Errorf("Data has %d keys, want %d", len(env.Data), len(serviceProperties))
	}
	if len(env.Parts) != 0 {
		t.Errorf("Parts = %v, want empty for a single-command operation", env.Parts)
	}
}

func TestServiceUnknownUnitIsFailedWithoutData(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{
		Stderr:   "Failed to get unit file state for nope.service: No such file or directory",
		ExitCode: 4,
		Duration: 20 * time.Millisecond,
	}, nil)

	op := serviceMustNew(t, "nope")
	env, err := op.Execute(context.Background(), rec, serviceTarget(t), servicePolicy(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if env.Status != result.StatusFailed {
		t.Errorf("Status = %q, want failed", env.Status)
	}
	if env.ExitCode != 4 {
		t.Errorf("ExitCode = %d, want 4", env.ExitCode)
	}
	if env.Data != nil {
		t.Errorf("Data = %v, want nil", env.Data)
	}
}

func TestServiceTruncatedIsReported(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{Stdout: "Id=nginx.service\n", ExitCode: 0, Truncated: true, Duration: time.Millisecond}, nil)

	op := serviceMustNew(t, "nginx")
	env, err := op.Execute(context.Background(), rec, serviceTarget(t), servicePolicy(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !env.Truncated || env.Status != result.StatusTruncated {
		t.Errorf("Status = %q, Truncated = %v, want truncated/true", env.Status, env.Truncated)
	}
	if env.Data["Id"] != "nginx.service" {
		t.Errorf("Data[Id] = %q, want the property that did arrive", env.Data["Id"])
	}
}

func TestServiceExecutionFailureIsReturnedAsError(t *testing.T) {
	rec := &sshtest.Recorder{}
	rec.Respond(ssh.Output{}, result.ExecutionError("ssh_failed", "ssh: connection refused"))

	op := serviceMustNew(t, "nginx")
	if _, err := op.Execute(context.Background(), rec, serviceTarget(t), servicePolicy(t)); err == nil {
		t.Fatal("Execute returned no error, want the execution failure")
	}
}
