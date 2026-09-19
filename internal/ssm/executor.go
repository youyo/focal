// Package ssm runs the same ssh.Command values internal/ssh builds, but over
// AWS Systems Manager Run Command instead of ssh(1). It does not build a
// remote argv of its own: the only text it produces is the one shell line
// commandLine renders from a Command already validated by internal/ssh, so
// the security boundary internal/ssh owns is unchanged by adding this
// transport. Its dependency on the AWS SDK is narrowed to the three methods
// declared in api.go, and AWS configuration is read exclusively from the
// SDK's own default chain — this package adds no flag and no config key of
// its own.
package ssm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// Options are the execution parameters an Executor runs under, mirroring
// ssh.Options' Timeout and MaxOutput so the two transports are configured the
// same way from internal/cli and internal/mcp. Region and credentials are
// deliberately absent: they come from the AWS SDK's default chain (env vars,
// shared config, instance profile), never from a Focal flag or config key.
type Options struct {
	Timeout   time.Duration
	MaxOutput int64
}

// documentName is the only SSM document this package ever runs. AWS-RunShellScript
// is an AWS-owned document; Focal defines no custom document, so there is
// nothing here for an administrator to author or misconfigure.
const documentName = "AWS-RunShellScript"

// truncationMarker is the text SSM appends to StandardOutputContent or
// StandardErrorContent when a stream exceeded its own service-side ceiling.
const truncationMarker = "---Output truncated---"

// ssmStdoutLimit and ssmStderrLimit are the fixed ceilings SSM Run Command
// applies to StandardOutputContent and StandardErrorContent regardless of
// Options.MaxOutput. A stream at its ceiling is treated as truncated even
// when truncationMarker is absent, since AWS does not always append it.
const (
	ssmStdoutLimit = 24000
	ssmStderrLimit = 8000
)

const (
	minTimeoutSeconds = 30
	maxTimeoutSeconds = 2592000

	// defaultPollInterval is how long Execute waits between GetCommandInvocation
	// calls. It is a field on Executor, not a constant baked into poll, so a
	// test can shrink it and never wait on a real clock.
	defaultPollInterval = 2 * time.Second
)

// codeTimeout matches the unexported constant of the same name in
// internal/ssh: ssh.TimedOut(err) inspects only the *result.Error's Kind and
// Code, so using the same string here lets a caller ask "did this time out"
// the same way regardless of which transport answered.
const codeTimeout = "timeout"

// Executor runs an ssh.Command through AWS SSM Run Command. It implements
// ssh.Executor so operations built against that interface do not know or care
// which transport is underneath.
//
// Every operation SSM runs is root: AWS-RunShellScript executes as root on
// the instance regardless of what Focal asks for, so a SudoNever command
// still runs privileged under this transport even though it never would over
// ssh(1). Execute's own doc comment states what that means for the sudo
// prefix and the SudoAuto retry.
type Executor struct {
	api          api
	opts         Options
	pollInterval time.Duration
}

var _ ssh.Executor = (*Executor)(nil)

// New validates the execution parameters and loads the AWS SDK's default
// configuration chain once, at startup, the same way ssh.NewOpenSSH validates
// its own Options before any host is contacted.
func New(ctx context.Context, opts Options) (*Executor, *result.Error) {
	switch {
	case opts.Timeout <= 0:
		return nil, result.ValidationError("invalid_timeout", "execution timeout must be positive",
			"execution.timeout", []string{"a positive duration"})
	case opts.MaxOutput <= 0:
		return nil, result.ValidationError("invalid_max_output", "execution max_output must be positive",
			"execution.max_output", []string{"a positive number of bytes"})
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, result.ExecutionError("aws_config_failed", "cannot load AWS configuration: "+err.Error())
	}
	return &Executor{api: ssm.NewFromConfig(cfg), opts: opts, pollInterval: defaultPollInterval}, nil
}

// Execute runs cmd against target, an EC2 instance ID. It applies the
// configured timeout to the whole call, including the poll loop.
//
// Unlike ssh.OpenSSH.Execute, a SudoAuto command is never retried with
// "sudo -n": AWS-RunShellScript already runs as root, so there is no
// permission-denied exit code an unprivileged first attempt could see that a
// privileged retry would fix, and retrying anyway would only run the command
// twice for no effect. SudoAlways still prefixes "sudo -n" up front, through
// ssh.PrefixesSudo, so a command's privilege reads the same way regardless of
// which transport ran it.
func (e *Executor) Execute(ctx context.Context, target vo.Target, cmd ssh.Command) (ssh.Output, error) {
	if cmd.IsZero() {
		return ssh.Output{}, result.ValidationError("empty_command",
			"command was not produced by a factory in internal/ssh", "command", nil)
	}
	instanceID := target.String()
	if err := checkInstanceID(instanceID); err != nil {
		return ssh.Output{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()

	started := time.Now()
	out, err := e.run(ctx, instanceID, cmd, ssh.PrefixesSudo(cmd))
	out.Duration = time.Since(started)
	if err != nil {
		return out, err
	}
	return out, nil
}

// run performs exactly one SendCommand and polls it to completion.
func (e *Executor) run(ctx context.Context, instanceID string, cmd ssh.Command, sudo bool) (ssh.Output, *result.Error) {
	send, err := e.api.SendCommand(ctx, &ssm.SendCommandInput{
		DocumentName:   aws.String(documentName),
		InstanceIds:    []string{instanceID},
		Parameters:     map[string][]string{"commands": {commandLine(cmd, sudo)}},
		TimeoutSeconds: aws.Int32(clampTimeoutSeconds(e.opts.Timeout)),
	})
	if err != nil {
		return ssh.Output{}, mapAPIError(err)
	}
	commandID := aws.ToString(send.Command.CommandId)
	return e.poll(ctx, commandID, instanceID)
}

// poll calls GetCommandInvocation until the invocation reaches a terminal
// status or ctx ends. A best-effort CancelCommand is issued whenever ctx ends
// while the invocation is still outstanding, using context.Background() since
// ctx itself has already fired.
func (e *Executor) poll(ctx context.Context, commandID, instanceID string) (ssh.Output, *result.Error) {
	for {
		inv, err := e.api.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
			CommandId:  aws.String(commandID),
			InstanceId: aws.String(instanceID),
		})
		if err != nil {
			var notExist *types.InvocationDoesNotExist
			if errors.As(err, &notExist) {
				if waitErr := e.wait(ctx); waitErr != nil {
					return ssh.Output{}, waitErr
				}
				continue
			}
			return ssh.Output{}, mapAPIError(err)
		}

		out, done, statusErr := e.evaluate(inv)
		if statusErr != nil {
			e.cancelBestEffort(commandID, instanceID)
			return ssh.Output{}, statusErr
		}
		if done {
			return out, nil
		}
		if waitErr := e.wait(ctx); waitErr != nil {
			e.cancelBestEffort(commandID, instanceID)
			return ssh.Output{}, waitErr
		}
	}
}

// evaluate reads one GetCommandInvocation response. It returns done=true with
// an Output for a terminal success/failure, done=false to keep polling, or a
// non-nil error for every other terminal status.
func (e *Executor) evaluate(inv *ssm.GetCommandInvocationOutput) (ssh.Output, bool, *result.Error) {
	switch aws.ToString(inv.StatusDetails) {
	case "DeliveryTimedOut":
		return ssh.Output{}, false, result.ExecutionError("ssm_delivery_timed_out",
			"ssm could not deliver the command to the instance in time")
	case "Undeliverable":
		return ssh.Output{}, false, result.ExecutionError("ssm_undeliverable",
			"ssm could not deliver the command to the instance")
	case "Terminated":
		return ssh.Output{}, false, result.ExecutionError("ssm_terminated",
			"ssm invocation was terminated before it completed")
	case "ExecutionTimedOut":
		return ssh.Output{}, false, result.ExecutionError(codeTimeout,
			fmt.Sprintf("ssm command did not finish within %s", e.opts.Timeout))
	}

	switch inv.Status {
	case types.CommandInvocationStatusPending, types.CommandInvocationStatusInProgress, types.CommandInvocationStatusDelayed:
		return ssh.Output{}, false, nil
	case types.CommandInvocationStatusSuccess, types.CommandInvocationStatusFailed:
		return e.finish(inv), true, nil
	case types.CommandInvocationStatusTimedOut:
		return ssh.Output{}, false, result.ExecutionError(codeTimeout,
			fmt.Sprintf("ssm command did not finish within %s", e.opts.Timeout))
	case types.CommandInvocationStatusCancelled, types.CommandInvocationStatusCancelling:
		return ssh.Output{}, false, result.ExecutionError("ssm_cancelled", "ssm command was cancelled")
	default:
		return ssh.Output{}, false, result.ExecutionError("ssm_unexpected_status",
			fmt.Sprintf("unexpected ssm invocation status %q", inv.Status))
	}
}

// finish builds the Output for a terminal Success or Failed invocation. A
// non-zero ResponseCode is not an error here, the same rule ssh.OpenSSH
// follows: the command ran and said no.
func (e *Executor) finish(inv *ssm.GetCommandInvocationOutput) ssh.Output {
	stdout := aws.ToString(inv.StandardOutputContent)
	stderr := aws.ToString(inv.StandardErrorContent)
	truncated := strings.HasSuffix(stdout, truncationMarker) ||
		strings.HasSuffix(stderr, truncationMarker) ||
		len(stdout) >= ssmStdoutLimit ||
		len(stderr) >= ssmStderrLimit ||
		int64(len(stdout))+int64(len(stderr)) >= e.opts.MaxOutput
	return ssh.Output{
		Stdout:    stdout,
		Stderr:    stderr,
		ExitCode:  int(inv.ResponseCode),
		Truncated: truncated,
	}
}

// wait pauses for one poll interval, or returns early with an error when ctx
// ends first. It never calls time.Sleep directly: a timer selected against
// ctx.Done() is what lets a canceled or timed-out context interrupt a wait
// immediately rather than after the full interval.
func (e *Executor) wait(ctx context.Context) *result.Error {
	interval := e.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return result.ExecutionError(codeTimeout, fmt.Sprintf("ssm command did not finish within %s", e.opts.Timeout))
		}
		return result.ExecutionError("canceled", "ssm command was canceled before it finished")
	}
}

// cancelBestEffort asks SSM to stop an invocation Execute is about to report
// as failed. It uses its own background context and discards the outcome: a
// failed cancel does not change the error Execute already decided to return,
// and this package must not block returning that error on a second API call.
func (e *Executor) cancelBestEffort(commandID, instanceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = e.api.CancelCommand(ctx, &ssm.CancelCommandInput{
		CommandId:   aws.String(commandID),
		InstanceIds: []string{instanceID},
	})
}

// clampTimeoutSeconds bounds Options.Timeout to the range SendCommand's own
// TimeoutSeconds parameter accepts.
func clampTimeoutSeconds(d time.Duration) int32 {
	s := int64(d.Seconds())
	switch {
	case s < minTimeoutSeconds:
		return minTimeoutSeconds
	case s > maxTimeoutSeconds:
		return maxTimeoutSeconds
	default:
		return int32(s)
	}
}

// mapAPIError turns an AWS SDK error into the structured form every Focal
// layer returns. InvalidInstanceId gets a message naming the causes an
// operator can actually act on; anything else is reported as the SDK
// described it.
func mapAPIError(err error) *result.Error {
	var invalidInstance *types.InvalidInstanceId
	if errors.As(err, &invalidInstance) {
		return result.ExecutionError("invalid_instance_id",
			"instance is not managed by SSM, is offline, or is in a different region")
	}
	return result.ExecutionError("ssm_api_error", "ssm api error: "+err.Error())
}
