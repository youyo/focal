package ssm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// fakeAPI is a hand-written stand-in for the three ssm.Client methods this
// package calls. invocationResponses is consumed in order per CommandId, so a
// test can script a transient InvocationDoesNotExist or an in-progress status
// before the terminal one.
type fakeAPI struct {
	mu sync.Mutex

	sendErr   error
	sendCalls []*ssm.SendCommandInput

	invocations map[string][]invocationStep
	invCalls    []*ssm.GetCommandInvocationInput

	cancelCalls []*ssm.CancelCommandInput
	cancelErr   error
}

type invocationStep struct {
	out *ssm.GetCommandInvocationOutput
	err error
}

func (f *fakeAPI) SendCommand(_ context.Context, params *ssm.SendCommandInput, _ ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalls = append(f.sendCalls, params)
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	return &ssm.SendCommandOutput{Command: &types.Command{CommandId: aws.String("cmd-1")}}, nil
}

func (f *fakeAPI) GetCommandInvocation(_ context.Context, params *ssm.GetCommandInvocationInput, _ ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invCalls = append(f.invCalls, params)
	id := aws.ToString(params.CommandId)
	steps := f.invocations[id]
	if len(steps) == 0 {
		return nil, errors.New("fakeAPI: no more steps programmed for " + id)
	}
	step := steps[0]
	if len(steps) > 1 {
		f.invocations[id] = steps[1:]
	}
	return step.out, step.err
}

func (f *fakeAPI) CancelCommand(_ context.Context, params *ssm.CancelCommandInput, _ ...func(*ssm.Options)) (*ssm.CancelCommandOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls = append(f.cancelCalls, params)
	if f.cancelErr != nil {
		return nil, f.cancelErr
	}
	return &ssm.CancelCommandOutput{}, nil
}

func newTestExecutor(t *testing.T, fake *fakeAPI) *Executor {
	t.Helper()
	return &Executor{
		api:          fake,
		opts:         Options{Timeout: time.Second, MaxOutput: 1024},
		pollInterval: time.Millisecond,
	}
}

func successInvocation(stdout, stderr string, responseCode int32) invocationStep {
	return invocationStep{out: &ssm.GetCommandInvocationOutput{
		Status:                types.CommandInvocationStatusSuccess,
		StandardOutputContent: aws.String(stdout),
		StandardErrorContent:  aws.String(stderr),
		ResponseCode:          responseCode,
	}}
}

func testTarget(t *testing.T) vo.Target {
	t.Helper()
	target, err := vo.ParseTarget("i-0123456789abcdef0")
	if err != nil {
		t.Fatalf("vo.ParseTarget: %v", err)
	}
	return target
}

func mustCmd(t *testing.T) ssh.Command {
	t.Helper()
	cmd, err := ssh.UptimeCommand(policy.Policy{})
	if err != nil {
		t.Fatalf("ssh.UptimeCommand: %v", err)
	}
	return cmd
}

func TestExecuteSuccess(t *testing.T) {
	fake := &fakeAPI{invocations: map[string][]invocationStep{
		"cmd-1": {successInvocation("up 3 days\n", "", 0)},
	}}
	e := newTestExecutor(t, fake)
	out, err := e.Execute(context.Background(), testTarget(t), mustCmd(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Stdout != "up 3 days\n" || out.ExitCode != 0 {
		t.Errorf("out = %+v", out)
	}
}

func TestExecuteFailedIsNotAnError(t *testing.T) {
	fake := &fakeAPI{invocations: map[string][]invocationStep{
		"cmd-1": {successInvocation("", "boom\n", 7)},
	}}
	fake.invocations["cmd-1"][0].out.Status = types.CommandInvocationStatusFailed
	e := newTestExecutor(t, fake)
	out, err := e.Execute(context.Background(), testTarget(t), mustCmd(t))
	if err != nil {
		t.Fatalf("Execute returned an error for a non-zero exit: %v", err)
	}
	if out.ExitCode != 7 || out.Stderr != "boom\n" {
		t.Errorf("out = %+v", out)
	}
}

func TestExecuteTimedOut(t *testing.T) {
	fake := &fakeAPI{invocations: map[string][]invocationStep{
		"cmd-1": {{out: &ssm.GetCommandInvocationOutput{Status: types.CommandInvocationStatusTimedOut}}},
	}}
	e := newTestExecutor(t, fake)
	_, err := e.Execute(context.Background(), testTarget(t), mustCmd(t))
	if !ssh.TimedOut(err) {
		t.Fatalf("ssh.TimedOut(err) = false, want true; err = %v", err)
	}
}

func TestExecuteCancelled(t *testing.T) {
	fake := &fakeAPI{invocations: map[string][]invocationStep{
		"cmd-1": {{out: &ssm.GetCommandInvocationOutput{Status: types.CommandInvocationStatusCancelled}}},
	}}
	e := newTestExecutor(t, fake)
	_, err := e.Execute(context.Background(), testTarget(t), mustCmd(t))
	var re *result.Error
	if !errors.As(err, &re) || re.Code != "ssm_cancelled" {
		t.Fatalf("err = %v, want code ssm_cancelled", err)
	}
}

func TestExecuteDeliveryTimedOut(t *testing.T) {
	fake := &fakeAPI{invocations: map[string][]invocationStep{
		"cmd-1": {{out: &ssm.GetCommandInvocationOutput{
			Status:        types.CommandInvocationStatusFailed,
			StatusDetails: aws.String("DeliveryTimedOut"),
		}}},
	}}
	e := newTestExecutor(t, fake)
	_, err := e.Execute(context.Background(), testTarget(t), mustCmd(t))
	var re *result.Error
	if !errors.As(err, &re) || re.Code != "ssm_delivery_timed_out" {
		t.Fatalf("err = %v, want code ssm_delivery_timed_out", err)
	}
}

func TestExecuteRetriesTransientInvocationDoesNotExist(t *testing.T) {
	fake := &fakeAPI{invocations: map[string][]invocationStep{
		"cmd-1": {
			{err: &types.InvocationDoesNotExist{}},
			{err: &types.InvocationDoesNotExist{}},
			successInvocation("ok\n", "", 0),
		},
	}}
	e := newTestExecutor(t, fake)
	out, err := e.Execute(context.Background(), testTarget(t), mustCmd(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Stdout != "ok\n" {
		t.Errorf("out = %+v", out)
	}
	if len(fake.invCalls) != 3 {
		t.Errorf("GetCommandInvocation called %d times, want 3", len(fake.invCalls))
	}
}

func TestExecuteContextCancelCallsCancelCommand(t *testing.T) {
	fake := &fakeAPI{invocations: map[string][]invocationStep{
		"cmd-1": {
			{out: &ssm.GetCommandInvocationOutput{Status: types.CommandInvocationStatusInProgress}},
			{out: &ssm.GetCommandInvocationOutput{Status: types.CommandInvocationStatusInProgress}},
			{out: &ssm.GetCommandInvocationOutput{Status: types.CommandInvocationStatusInProgress}},
		},
	}}
	e := newTestExecutor(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	_, err := e.Execute(ctx, testTarget(t), mustCmd(t))
	if err == nil {
		t.Fatal("Execute returned no error for a canceled context")
	}
	if len(fake.cancelCalls) == 0 {
		t.Error("CancelCommand was not called after the context was canceled")
	}
}

func TestExecuteAWSAPIError(t *testing.T) {
	fake := &fakeAPI{sendErr: errors.New("boom")}
	e := newTestExecutor(t, fake)
	_, err := e.Execute(context.Background(), testTarget(t), mustCmd(t))
	var re *result.Error
	if !errors.As(err, &re) || re.Code != "ssm_api_error" {
		t.Fatalf("err = %v, want code ssm_api_error", err)
	}
}

func TestExecuteInvalidInstanceID(t *testing.T) {
	// Each of these is a valid vo.Target (vo's own grammar has no notion of
	// "instance ID"), so what is under test here is this package's second
	// layer: an SSM target must be an instance ID, and "user@" — meaningful
	// to ssh(1) — must not silently be dropped.
	for _, target := range []string{"user@i-0123456789abcdef0", "example.com", "i-short"} {
		t.Run(target, func(t *testing.T) {
			vt, err := vo.ParseTarget(target)
			if err != nil {
				t.Fatalf("vo.ParseTarget(%q): %v", target, err)
			}
			e := newTestExecutor(t, &fakeAPI{})
			_, execErr := e.Execute(context.Background(), vt, mustCmd(t))
			var re *result.Error
			if !errors.As(execErr, &re) || re.Code != "invalid_target" {
				t.Fatalf("err = %v, want code invalid_target", execErr)
			}
		})
	}
}

// TestExecuteSudoAutoNeverRetries pins the point where this transport
// deliberately does not follow ssh.OpenSSH's rule: AWS-RunShellScript always
// runs as root, so a SudoAuto command that comes back non-zero is reported as
// is, with exactly one SendCommand call, never a privileged second attempt.
func TestExecuteSudoAutoNeverRetries(t *testing.T) {
	fake := &fakeAPI{invocations: map[string][]invocationStep{
		"cmd-1": {successInvocation("", "permission denied\n", 1)},
	}}
	fake.invocations["cmd-1"][0].out.Status = types.CommandInvocationStatusFailed

	e := newTestExecutor(t, fake)
	auto, perr := policy.Resolve("logs", policy.SudoAuto)
	if perr != nil {
		t.Fatalf("policy.Resolve: %v", perr)
	}
	unit, verr := vo.ParseServiceName("nginx.service")
	if verr != nil {
		t.Fatalf("vo.ParseServiceName: %v", verr)
	}
	since, verr := vo.ParseDuration("30m")
	if verr != nil {
		t.Fatalf("vo.ParseDuration: %v", verr)
	}
	lines, verr := vo.ParseLineLimit("200")
	if verr != nil {
		t.Fatalf("vo.ParseLineLimit: %v", verr)
	}
	cmd, cerr := ssh.LogsCommand(auto, unit, since, lines)
	if cerr != nil {
		t.Fatalf("ssh.LogsCommand: %v", cerr)
	}

	out, err := e.Execute(context.Background(), testTarget(t), cmd)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fake.sendCalls) != 1 {
		t.Fatalf("SendCommand called %d times, want exactly 1 (no sudo retry for ssm)", len(fake.sendCalls))
	}
	if out.ExitCode != 1 || out.Stderr != "permission denied\n" {
		t.Errorf("out = %+v", out)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	if _, err := New(context.Background(), Options{Timeout: 0, MaxOutput: 1}); err == nil {
		t.Error("New accepted a zero timeout")
	}
	if _, err := New(context.Background(), Options{Timeout: time.Second, MaxOutput: 0}); err == nil {
		t.Error("New accepted a zero max_output")
	}
}
