// Package mcp exposes Focal's operations to a coding agent as a Remote MCP
// server. It is the CLI's sibling, not its replacement: both turn a request
// into exactly one typed operation.Operation and print the same
// result.Envelope, and neither can name a remote command.
//
// This package deliberately knows nothing about SSH. It builds an Operation
// from a Registry and a Target from a value object, then hands both to a
// Runner — the one interface it declares — and an import of internal/ssh here
// is a test failure, not a review comment. Everything about how the host is
// reached (which key an alias names, what ~/.ssh/config says, the ssh(1)
// invocation itself) belongs to whoever implements Runner; see internal/cli's
// focal serve.
//
// The server is stateless in the sense the 2026-07-28 revision of the MCP
// specification means: no initialize handshake, no session id, every POST
// self-contained. That is what lets focal serve sit behind an ordinary
// authenticating reverse proxy, which is where authenticating a user lives —
// focal does none of that, and binds to loopback unless told otherwise.
//
// Nothing in this package reads the Authorization header, and nothing should.
// The handler NewHandler returns answers whatever reaches it; deciding what may
// reach it belongs to internal/cli, which chooses the address or socket focal
// listens on and wraps this handler in the shared-secret check
// `focal serve --upstream-token` asks for.
package mcp

import (
	"context"
	"fmt"
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/youyo/focal/internal/config"
	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// Runner runs one already-built operation against one already-validated
// target and reports the outcome. It is the whole of this package's dependency
// on the layer below.
//
// identity is the alias the call named, or the empty string when it named
// none; resolving it to a key — and refusing an alias nobody registered — is
// the implementation's business, because the aliases and their files are
// things only the process that was started with them knows.
//
// The returned error is the structured refusal or failure the agent should
// see. An operation that ran and reported a non-zero exit is not an error: it
// is an Envelope with a failed status.
type Runner interface {
	Run(ctx context.Context, op operation.Operation, target vo.Target, identity string, transport vo.Transport) (result.Envelope, *result.Error)
}

// Options are the server-wide defaults a focal serve invocation was started
// with. They are defaults in the strict sense: a tool call that names a value
// itself is never overridden by one.
type Options struct {
	// Version is what the server reports as its implementation version.
	Version string
	// DefaultUser is the login user applied to a call whose host does not
	// name one, or the empty string to leave the choice to ssh(1) and
	// ~/.ssh/config.
	DefaultUser string
}

// NewHandler builds the HTTP handler that serves focal's operations over MCP.
// Only the operations this installation has enabled become tools, and the set
// is fixed here at startup: an operation the configuration turned off has no
// tool to list and no tool to call, so it is refused before a target is even
// parsed.
func NewHandler(cfg config.Config, runner Runner, opts Options) http.Handler {
	s := &server{
		cfg:    cfg,
		runner: runner,
		opts:   opts,
		registry: operation.NewRegistry(func(name string) bool {
			op, known := cfg.Operation(name)
			return known && op.Enabled()
		}),
	}

	srv := sdk.NewServer(&sdk.Implementation{
		Name:        "focal",
		Title:       "Focal",
		Description: "Read-only inspection of hosts over your existing OpenSSH configuration.",
		Version:     opts.Version,
	}, nil)
	for name, spec := range toolSpecs {
		if !s.registry.Enabled(name) {
			continue
		}
		spec.add(srv, s, name, spec.summary)
	}

	// Stateless is not a tuning knob: the 2026-07-28 revision dropped the
	// initialize handshake and the session id, and the SDK serves that
	// revision only in this mode.
	return sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return srv },
		&sdk.StreamableHTTPOptions{Stateless: true},
	)
}

type server struct {
	cfg      config.Config
	runner   Runner
	opts     Options
	registry operation.Registry
}

// call is one tools/call: read the arguments, build the operation, run it,
// answer with the envelope.
//
// Every way this can go wrong short of a protocol violation leaves as a tool
// result with isError set, carrying focal's own error JSON. That is what lets
// the agent read which layer said no and what would have been accepted, and
// correct itself; a JSON-RPC error would tell it only that the call failed.
// The protocol errors are left to the SDK, which raises them for the things
// that are genuinely not about this request's content: an unknown tool name,
// arguments that do not fit the tool's declared schema, a malformed message.
func (s *server) call(ctx context.Context, name string, args toolArgs) (*sdk.CallToolResult, any, error) {
	dest := args.destination()

	transport, err := resolveCallTransport(dest)
	if err != nil {
		return toolError(err), nil, nil
	}
	if !s.cfg.TransportEnabled(transport) {
		return toolError(result.PolicyError(
			"transport_not_enabled",
			fmt.Sprintf("transport %q is not enabled for this installation", transport),
			"execution.transports",
			[]string{"enable it in the configuration file"},
		)), nil, nil
	}

	// The server-wide default user is an ssh(1) concept: applying it to an
	// ssm call would silently turn a plain instance ID into "user@i-...",
	// which this package's own dest.User check just refused when the call
	// named a user explicitly. Composing it here as instead of there would
	// let the same refusal be bypassed through the server's own default
	// rather than through the call, so it is suppressed the same way.
	defaultUser := s.opts.DefaultUser
	if transport == vo.TransportSSM {
		defaultUser = ""
	}
	target, err := resolveTarget(dest.Host, dest.User, defaultUser)
	if err != nil {
		return toolError(err), nil, nil
	}

	// The operation is known to be enabled — a disabled one has no tool —
	// so this only reads the ceilings the administrator set for it.
	opCfg, _ := s.cfg.Operation(name)
	params := args.params()
	params.MaxSince, params.MaxLines = opCfg.MaxSince(), opCfg.MaxLines()

	op, err := s.registry.Build(name, params)
	if err != nil {
		return toolError(err), nil, nil
	}

	env, err := s.runner.Run(ctx, op, target, dest.Identity, transport)
	if err != nil {
		return toolError(err), nil, nil
	}
	body, encErr := env.Encode()
	if encErr != nil {
		return toolError(result.ExecutionError("encode_failed", "cannot encode the result: "+encErr.Error())), nil, nil
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(body)}}}, nil, nil
}

// toolError renders a structured error as the failed tool result an agent
// reads: the same JSON the CLI writes to stderr, so kind, code, message, field
// and the allowed values are available whichever way focal was called.
func toolError(err *result.Error) *sdk.CallToolResult {
	text := err.Error()
	if line, encErr := result.Encode(err); encErr == nil {
		text = string(line)
	}
	return &sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{&sdk.TextContent{Text: text}},
	}
}
