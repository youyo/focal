package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/youyo/focal/internal/mcp"
	"github.com/youyo/focal/internal/result"
)

// This file is `focal mcp`: focal serve's tools, offered over stdin and stdout
// to an MCP client that starts focal itself, rather than over a listener.
//
// It is a command of its own rather than a mode of focal serve because none of
// what decides whether focal serve may listen applies to it: there is no
// address to judge, no socket to create and no token to demand. Keeping it out
// of serve's code path is what shows that in the structure rather than in a
// combination of flags. What it does share with serve is everything about the
// tools — the configuration file, the identity aliases, the runner, and the
// ceiling on concurrent work.
//
// stdout belongs to the protocol from the moment the session starts: the SDK
// writes JSON-RPC there and nothing else does. A refusal before that — a bad
// flag, a bad configuration file, a bad key — goes to stderr in focal's own
// error schema, like every other command's, and leaves stdout empty.

// mcpOptions is what `focal mcp` was started with: the same three things
// focal serve is told about the tools, and nothing about listening.
type mcpOptions struct {
	identities []string
	user       string
	config     string
}

// newMCPCommand builds the mcp subcommand.
func newMCPCommand(a *app) *cobra.Command {
	var o mcpOptions
	cmd := &cobra.Command{
		Use:   "mcp [flags]",
		Short: "Serve focal's operations to a coding agent over MCP on stdin and stdout",
		Long: "focal mcp serves the same read-only inspections focal serve offers, as\n" +
			"MCP tools over stdin and stdout, for an MCP client that starts focal as\n" +
			"its own child process.\n\n" +
			"It opens no listener: only the process that started focal can talk to\n" +
			"it, so there is no address to choose and no upstream token to demand.\n" +
			"stdout carries MCP messages only; diagnostics go to stderr.\n\n" +
			"--identity, --user and --config mean what they mean for focal serve.\n" +
			"focal mcp exits when the client closes stdin, or on SIGINT or SIGTERM.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.serveStdio(cmd.Context(), o); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.SetOut(a.stdout)
	cmd.SetErr(a.stderr)

	flags := cmd.Flags()
	flags.StringArrayVar(&o.identities, "identity", nil,
		"register a private key as alias=path, repeatable; a tool call may name only the alias")
	flags.StringVar(&o.user, "user", "", "default user to log in as, when a tool call and its host name none")
	flags.StringVar(&o.config, "config", "", "configuration file to read instead of the default location")

	return cmd
}

// serveStdio is one focal mcp invocation: build the service, then serve it on
// this process's stdin and stdout until the client closes stdin or a signal
// arrives.
//
// Everything that can be refused is refused before the session starts, so a
// misconfigured focal mcp exits having written nothing to stdout.
func (a *app) serveStdio(ctx context.Context, o mcpOptions) *cliError {
	cfg, runner, err := a.newServeRunner(o.config, o.identities)
	if err != nil {
		return err
	}

	// Said only once the configuration and the keys have been accepted, so
	// a start refused for either keeps its stderr to the one error line
	// every command's refusal is.
	if a.token != "" {
		// The value is never repeated: it is a secret even here, and
		// stderr ends up in logs.
		fmt.Fprintf(a.stderr,
			"focal mcp: note: %s is set, but focal mcp does not use it: stdio has no listener for it to guard.\n"+
				"It has been removed from the environment of everything focal starts.\n",
			upstreamTokenEnv)
	}

	// A signal is one of the two ordinary ways a stdio server is told to
	// stop, so it ends the session rather than the process: the calls still
	// running are cancelled, and focal exits as it would at the end of
	// stdin.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := mcp.ServeStdio(ctx, cfg, runner, mcp.Options{
		Version:     buildVersion(),
		DefaultUser: o.user,
		MaxInFlight: serveMaxInFlight,
	}, a.stdin, a.stdout)
	if serveErr != nil {
		return rejected(result.ExecutionError("mcp_session_failed", "the MCP session failed: "+serveErr.Error()))
	}
	return nil
}
