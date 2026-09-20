package ssm

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// api is the slice of *ssm.Client this package depends on: SendCommand starts
// a run, GetCommandInvocation polls it, and CancelCommand is the best-effort
// stop when a context ends before the invocation finished. Narrowing to an
// unexported interface — rather than depending on *ssm.Client directly — is
// what lets a test double stand in for the whole AWS SDK client.
type api interface {
	SendCommand(ctx context.Context, params *ssm.SendCommandInput, optFns ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
	GetCommandInvocation(ctx context.Context, params *ssm.GetCommandInvocationInput, optFns ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)
	CancelCommand(ctx context.Context, params *ssm.CancelCommandInput, optFns ...func(*ssm.Options)) (*ssm.CancelCommandOutput, error)
}
