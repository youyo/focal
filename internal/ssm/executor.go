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
	"strconv"
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

	// minExecutionTimeoutSeconds and maxExecutionTimeoutSeconds bound the
	// AWS-RunShellScript document's own "executionTimeout" parameter: how
	// long the command may run on the instance once delivered. This is a
	// separate range from TimeoutSeconds' 30..2592000 above, which bounds
	// only how long SendCommand keeps trying to deliver the command in the
	// first place — the two are not interchangeable.
	minExecutionTimeoutSeconds = 1
	maxExecutionTimeoutSeconds = 172800

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
//
// The region is the one piece of that chain worth checking explicitly.
// LoadDefaultConfig does not fall back to the EC2 instance metadata service
// for a region on its own, so on an EC2 host with AWS_REGION unset the chain
// would otherwise resolve to an empty region and fail later, deep inside
// SendCommand, with an error that does not say why. WithEC2IMDSRegion adds
// that fallback, and an empty region even after it is reported here, at
// startup, as the one thing an operator needs to set.
func New(ctx context.Context, opts Options) (*Executor, *result.Error) {
	switch {
	case opts.Timeout <= 0:
		return nil, result.ValidationError("invalid_timeout", "execution timeout must be positive",
			"execution.timeout", []string{"a positive duration"})
	case opts.MaxOutput <= 0:
		return nil, result.ValidationError("invalid_max_output", "execution max_output must be positive",
			"execution.max_output", []string{"a positive number of bytes"})
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithEC2IMDSRegion())
	if err != nil {
		return nil, result.ExecutionError("aws_config_failed", "cannot load AWS configuration: "+err.Error())
	}
	if regionErr := checkRegion(cfg); regionErr != nil {
		return nil, regionErr
	}
	return &Executor{api: ssm.NewFromConfig(cfg), opts: opts, pollInterval: defaultPollInterval}, nil
}

// checkRegion is the one thing about the loaded configuration this package
// verifies itself, pulled out as a pure function so it can be tested against
// a config literal rather than against whatever this machine's real
// environment and IMDS reachability happen to resolve to.
func checkRegion(cfg aws.Config) *result.Error {
	if cfg.Region == "" {
		return result.ExecutionError("aws_region_not_set",
			"cannot determine the AWS region: set AWS_REGION (or AWS_DEFAULT_REGION), "+
				"or run on an EC2 instance whose IMDS reports one")
	}
	return nil
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
		DocumentName: aws.String(documentName),
		InstanceIds:  []string{instanceID},
		Parameters: map[string][]string{
			"commands":         {commandLine(cmd, sudo)},
			"executionTimeout": {strconv.Itoa(int(clampExecutionTimeoutSeconds(e.opts.Timeout)))},
		},
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
					// SendCommand already succeeded — the invocation record
					// simply had not propagated yet — so the command may
					// still be running on the instance even though this
					// poll loop is about to give up. The same best-effort
					// cancel the other two exit paths below make applies
					// here too.
					e.cancelBestEffort(commandID, instanceID)
					return ssh.Output{}, waitErr
				}
				continue
			}
			// GetCommandInvocation can fail this way when ctx's own
			// deadline or cancellation is what actually ended the call —
			// the AWS SDK reports that as a generic request error, not as
			// context.DeadlineExceeded, so it has to be checked for
			// explicitly rather than left to errors.Is on err itself.
			// SendCommand already succeeded by this point, so the same
			// best-effort cancel every other ctx-ending exit makes applies
			// here too, and the error focal reports should say "timed out"
			// or "canceled" — checkable through ssh.TimedOut — rather than
			// the SDK's own wording for a call that failed only because
			// its context ended.
			if ctxErr := e.ctxError(ctx); ctxErr != nil {
				e.cancelBestEffort(commandID, instanceID)
				return ssh.Output{}, ctxErr
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
	// The exact text of each value below is AWS's own wording for
	// StatusDetails, spaces included where AWS puts them ("Delivery Timed
	// Out", "Execution Timed Out") and not where it doesn't ("Undeliverable",
	// "Terminated"). See GetCommandInvocation's StatusDetails documentation.
	switch aws.ToString(inv.StatusDetails) {
	case "Delivery Timed Out":
		return ssh.Output{}, false, result.ExecutionError("ssm_delivery_timed_out",
			"ssm could not deliver the command to the instance in time")
	case "Undeliverable":
		return ssh.Output{}, false, result.ExecutionError("ssm_undeliverable",
			"ssm could not deliver the command to the instance")
	case "Terminated":
		return ssh.Output{}, false, result.ExecutionError("ssm_terminated",
			"ssm invocation was terminated before it completed")
	case "Execution Timed Out":
		return ssh.Output{}, false, result.ExecutionError(codeTimeout,
			fmt.Sprintf("ssm command did not finish within %s", e.opts.Timeout))
	}

	switch inv.Status {
	// Cancelling is not a terminal status: it is the window between a
	// CancelCommand request and the invocation actually stopping, and the
	// invocation may still finish (Success/Failed) before the cancel takes
	// effect. Treating it as terminal here would report a command as
	// cancelled when it might go on to complete normally, so it is polled
	// the same way Pending/InProgress/Delayed are.
	case types.CommandInvocationStatusPending, types.CommandInvocationStatusInProgress,
		types.CommandInvocationStatusDelayed, types.CommandInvocationStatusCancelling:
		return ssh.Output{}, false, nil
	case types.CommandInvocationStatusSuccess, types.CommandInvocationStatusFailed:
		return e.finish(inv), true, nil
	case types.CommandInvocationStatusTimedOut:
		return ssh.Output{}, false, result.ExecutionError(codeTimeout,
			fmt.Sprintf("ssm command did not finish within %s", e.opts.Timeout))
	case types.CommandInvocationStatusCancelled:
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
	truncatedBySSM := strings.HasSuffix(stdout, truncationMarker) ||
		strings.HasSuffix(stderr, truncationMarker) ||
		len(stdout) >= ssmStdoutLimit ||
		len(stderr) >= ssmStderrLimit

	stdout, stderr, cappedByMaxOutput := capOutput(stdout, stderr, e.opts.MaxOutput)

	return ssh.Output{
		Stdout:    stdout,
		Stderr:    stderr,
		ExitCode:  int(inv.ResponseCode),
		Truncated: truncatedBySSM || cappedByMaxOutput,
	}
}

// capOutput bounds stdout and stderr to a shared byte budget, the same
// meaning Options.MaxOutput carries for ssh.OpenSSH's own outputCap: the most
// output a single command may hand back combined, across both streams, not a
// ceiling applied to each one separately. Unlike OpenSSH's executor, SSM has
// already delivered both streams as complete strings by the time this runs —
// there is no live process to stop early — so the budget is applied by
// slicing rather than by capping a writer mid-stream. stdout is kept whole
// first and stderr absorbs whatever the budget has left, which is arbitrary
// but deterministic: the two streams were never interleaved in the first
// place, so there is no original order for a cut to preserve.
func capOutput(stdout, stderr string, budget int64) (string, string, bool) {
	stdoutLen, stderrLen := int64(len(stdout)), int64(len(stderr))
	if stdoutLen+stderrLen <= budget {
		return stdout, stderr, false
	}
	if stdoutLen >= budget {
		// New rejects a non-positive Options.MaxOutput, so budget is always
		// positive here and stdout[:budget] is always a valid slice.
		return stdout[:budget], "", true
	}
	return stdout, stderr[:budget-stdoutLen], true
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
		return e.ctxError(ctx)
	}
}

// ctxError reports why ctx ended, in the same terms wait already used: a
// deadline is reported as codeTimeout, so ssh.TimedOut(err) can recognize it
// regardless of which of this package's several ctx-ending paths produced
// it; anything else — an explicit cancel — is reported as "canceled". It
// returns nil when ctx has not ended, so a caller elsewhere in this file
// that suspects ctx (rather than the SDK call itself) explains a failure can
// check that suspicion without duplicating this classification.
func (e *Executor) ctxError(ctx context.Context) *result.Error {
	if ctx.Err() == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result.ExecutionError(codeTimeout, fmt.Sprintf("ssm command did not finish within %s", e.opts.Timeout))
	}
	return result.ExecutionError("canceled", "ssm command was canceled before it finished")
}

// cancelBestEffort asks SSM to stop an invocation Execute is about to report
// as failed. It uses its own background context and discards the outcome: a
// failed cancel does not change the error Execute already decided to return,
// and this package must not block returning that error on a second API call.
func (e *Executor) cancelBestEffort(commandID, instanceID string) {
	// 2 seconds, the same meaning ssh.OpenSSH's waitDelay carries: how long
	// this best-effort cleanup call may hold the caller who is already
	// giving up on the command, not a budget for the cancel to actually
	// succeed. A slow or wedged CancelCommand should not turn a fast
	// timeout/cancel into a slow one.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = e.api.CancelCommand(ctx, &ssm.CancelCommandInput{
		CommandId:   aws.String(commandID),
		InstanceIds: []string{instanceID},
	})
}

// clampTimeoutSeconds bounds Options.Timeout to the range SendCommand's own
// TimeoutSeconds parameter accepts: how long SendCommand keeps trying to
// deliver the command to the instance before giving up.
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

// clampExecutionTimeoutSeconds bounds Options.Timeout to the range
// AWS-RunShellScript's own "executionTimeout" parameter accepts: how long the
// command may run on the instance once delivered. TimeoutSeconds alone does
// not bound this — it is a delivery deadline, not an execution deadline — so
// without this parameter a command could keep running past Options.Timeout
// even though Execute's own context deadline had already given up on it.
func clampExecutionTimeoutSeconds(d time.Duration) int32 {
	s := int64(d.Seconds())
	switch {
	case s < minExecutionTimeoutSeconds:
		return minExecutionTimeoutSeconds
	case s > maxExecutionTimeoutSeconds:
		return maxExecutionTimeoutSeconds
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
