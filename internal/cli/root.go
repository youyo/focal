// Package cli is focal's command line: it turns `focal HOST OPERATION [args]`
// into one typed operation and prints the structured result.
//
// The destination comes first, the way ssh(1) takes it, and everything after
// the operation name is that operation's own input. There is no way to name a
// remote command here: focal has no exec, run, shell or command operation, no
// -o for ssh_config options, and an operation name that is not in
// operationSpecs is refused rather than passed along. What a caller can choose
// is the host, one operation, and that operation's typed parameters — nothing
// this package does can widen that set, because it builds no Command of its own
// and reaches internal/ssh only through internal/operation.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/youyo/focal/internal/config"
	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// Execute runs focal with args (os.Args[1:]) and returns the process exit
// code. It is the whole of this package's public surface.
func Execute(args []string, stdout, stderr io.Writer) int {
	return (&app{stdout: stdout, stderr: stderr, newExecutor: newOpenSSH}).run(args)
}

// executorFactory builds the executor a run uses from the resolved connection
// options. Production passes newOpenSSH; a test passes one that hands back an
// sshtest.Recorder, which is how the argv focal would have sent is asserted
// without a remote host.
type executorFactory func(ssh.Options) (ssh.Executor, *result.Error)

func newOpenSSH(opts ssh.Options) (ssh.Executor, *result.Error) {
	ex, err := ssh.NewOpenSSH(opts)
	if err != nil {
		return nil, err
	}
	return ex, nil
}

type app struct {
	stdout      io.Writer
	stderr      io.Writer
	newExecutor executorFactory
}

// globals are the flags that may appear before HOST. The set is deliberately
// small and fixed: it covers how to reach the host and how to print the
// answer, and nothing that could reach ssh_config.
type globals struct {
	identity string
	port     int
	user     string
	timeout  time.Duration
	config   string
	pretty   bool
	version  bool
}

// run parses args and dispatches. Every failure leaves through the same place:
// a *cliError carries the structured error and its exit code, and anything
// cobra itself rejected (an unknown flag, a malformed --port) is restated in
// the same schema rather than printed as a usage dump.
func (a *app) run(args []string) int {
	var g globals
	cmd := a.newRootCommand(&g)
	cmd.SetArgs(args)

	err := cmd.Execute()
	if err == nil {
		return exitOK
	}
	var ce *cliError
	if errors.As(err, &ce) {
		writeError(a.stderr, ce.err)
		return ce.code
	}
	writeError(a.stderr, result.ValidationError(
		"invalid_arguments", err.Error(), "", []string{usageForm},
	))
	return exitUsage
}

func (a *app) newRootCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "focal [flags] HOST OPERATION [args]",
		Short: "Give coding agents visibility, not shell access",
		Long: "focal runs a fixed set of read-only inspection operations on a host\n" +
			"through your existing OpenSSH configuration. It never accepts an\n" +
			"arbitrary remote command: HOST names where to look and OPERATION names\n" +
			"which of focal's own inspections to run there.\n\n" +
			"Flags belong before HOST. Everything after OPERATION is that\n" +
			"operation's own input.\n\n" +
			"Operations: " + strings.Join(operationNames(), ", "),
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		// focal has no subcommands, so cobra's generated completion and help
		// commands would be the only ones — and a `focal completion` that is
		// neither a host nor an operation is exactly the kind of second entry
		// point this CLI is shaped to avoid.
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		// dispatch's result is checked rather than returned directly: a nil
		// *cliError returned as an error interface would not compare equal to
		// nil, and every successful run would look like a failure.
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.dispatch(cmd, g, args); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.SetOut(a.stdout)
	cmd.SetErr(a.stderr)

	flags := cmd.Flags()
	// HOST is a positional argument, and the tokens after it belong to the
	// operation. Without this, `focal web logs nginx --since 30m` would have
	// --since read as a global flag; with it, parsing stops at the first
	// positional and --since reaches the operation's own flag set. The
	// asymmetry is deliberate and tested: a global flag written after HOST is
	// an unknown flag, not a late global.
	flags.SetInterspersed(false)

	flags.StringVarP(&g.identity, "identity", "i", "", "private key file to authenticate with")
	flags.IntVarP(&g.port, "port", "p", 0, "SSH port to connect to")
	flags.StringVarP(&g.user, "user", "l", "", "user to log in as, when HOST does not name one")
	flags.DurationVar(&g.timeout, "timeout", 0, "ceiling on a single remote command, e.g. 30s")
	flags.StringVar(&g.config, "config", "", "configuration file to read instead of the default location")
	// --json is the explicit spelling of what focal does anyway, so nothing
	// binds or reads it: --pretty is the flag that changes the rendering, and
	// the two are mutually exclusive, which is the whole of --json's effect.
	flags.Bool("json", false, "print the result as one line of JSON (the default)")
	flags.BoolVar(&g.pretty, "pretty", false, "print the result as indented JSON")
	flags.BoolVar(&g.version, "version", false, "print the version and exit")
	cmd.MarkFlagsMutuallyExclusive("json", "pretty")

	cmd.AddCommand(newServeCommand(a))

	return cmd
}

// loadConfig reads focal's configuration from --config when the caller named
// one, or from the default XDG location otherwise.
func loadConfig(g *globals) (config.Config, *result.Error) {
	return loadConfigFrom(g.config)
}

// loadConfigFrom reads focal's configuration from path, or the default XDG
// location when path is empty. Every other check — existence, size,
// permissions, YAML well-formedness, capability resolution — is identical
// either way; only where the file is looked up changes. Both the root
// command's --config and `focal serve`'s own --config go through this, so a
// bad path or a malformed file is rejected the same way from either.
func loadConfigFrom(path string) (config.Config, *result.Error) {
	if path != "" {
		return config.LoadFrom(path)
	}
	return config.Load()
}

// dispatch is one invocation: read the command line, load the configuration,
// build the operation the caller named, run it, print the result.
//
// The order of the checks is what keeps a disabled operation from reaching the
// network: whether this installation offers the operation at all is settled
// before an executor exists, so a `focal HOST kernel` against the default
// configuration produces a policy error and no SSH invocation whatsoever.
func (a *app) dispatch(cmd *cobra.Command, g *globals, args []string) *cliError {
	if g.version {
		fmt.Fprintln(a.stdout, buildVersion())
		return nil
	}
	if len(args) < 2 {
		return usageError(
			"missing_arguments",
			"focal needs a host and an operation",
			"", []string{usageForm, "operations: " + strings.Join(operationNames(), ", ")},
		)
	}

	host, name, rest := args[0], args[1], args[2:]
	spec, ok := operationSpecs[name]
	if !ok {
		return usageError(
			"unknown_operation",
			fmt.Sprintf("unknown operation %q", name),
			"operation", operationNames(),
		)
	}

	cfg, cfgErr := loadConfig(g)
	if cfgErr != nil {
		return rejected(cfgErr)
	}
	opCfg, known := cfg.Operation(name)
	if !known {
		return rejected(result.PolicyError(
			"unknown_operation",
			fmt.Sprintf("unknown operation %q", name),
			"operation", operationNames(),
		))
	}
	if !opCfg.Enabled() {
		return rejected(result.PolicyError(
			"operation_disabled",
			fmt.Sprintf("operation %q is not enabled for this installation", name),
			"operations."+name+".enabled",
			[]string{"enable it in the configuration file"},
		))
	}

	target, targetErr := resolveTarget(host, g.user)
	if targetErr != nil {
		return rejected(targetErr)
	}

	params, paramsErr := parseOperationArgs(name, spec, rest)
	if paramsErr != nil {
		return paramsErr
	}
	params.MaxSince, params.MaxLines = opCfg.MaxSince(), opCfg.MaxLines()

	registry := operation.NewRegistry(func(n string) bool {
		op, ok := cfg.Operation(n)
		return ok && op.Enabled()
	})
	op, buildErr := registry.Build(name, params)
	if buildErr != nil {
		return rejected(buildErr)
	}

	opts, optsErr := connectionOptions(cmd, g, cfg)
	if optsErr != nil {
		return rejected(optsErr)
	}
	executor, execErr := a.newExecutor(opts)
	if execErr != nil {
		return rejected(execErr)
	}

	env, runErr := op.Execute(context.Background(), executor, target, opCfg.Policy())
	if runErr != nil {
		return rejected(asResultError(runErr))
	}
	return writeEnvelope(a.stdout, env, g.pretty)
}

// resolveTarget builds the destination from HOST and -l. A HOST that already
// names a user together with -l is an error rather than a silent preference for
// one of them: the two spell the same thing, and guessing which one the caller
// meant is a decision focal has no basis to make. The assembled string goes
// through vo.ParseTarget like any other, so a user name from -l is validated
// rather than trusted for having arrived on a separate flag.
func resolveTarget(host, user string) (vo.Target, *result.Error) {
	if user != "" {
		if strings.Contains(host, "@") {
			return vo.Target{}, result.ValidationError(
				"conflicting_user",
				"the user is named twice: once in HOST and once in -l/--user",
				"target",
				[]string{"either user@host or --user with a bare host"},
			)
		}
		host = user + "@" + host
	}
	return vo.ParseTarget(host)
}

// connectionOptions layers the caller's overrides onto the configured values.
// The priority is CLI override → config file → whatever ssh(1) decides, which
// is why an unset --port or --identity leaves the field at the config's value
// (itself possibly zero, which internal/ssh reads as "leave it to OpenSSH")
// rather than at the flag's own default. cmd.Flags().Changed is what
// distinguishes "not given" from "given as zero".
func connectionOptions(cmd *cobra.Command, g *globals, cfg config.Config) (ssh.Options, *result.Error) {
	opts := ssh.Options{
		Timeout:      cfg.Timeout(),
		MaxOutput:    cfg.MaxOutput(),
		IdentityFile: cfg.IdentityFile(),
		Port:         cfg.Port(),
	}
	flags := cmd.Flags()
	if flags.Changed("identity") {
		path, err := expandHome(g.identity)
		if err != nil {
			return ssh.Options{}, err
		}
		opts.IdentityFile = path
	}
	if flags.Changed("port") {
		opts.Port = g.port
	}
	if flags.Changed("timeout") {
		opts.Timeout = g.timeout
	}
	return opts, nil
}

// expandHome expands a leading ~/ the way OpenSSH does, so that -i ~/.ssh/id
// names the same file from the shell and from a script that quoted it. Whether
// the file exists and is privately permissioned is internal/ssh's and the
// operator's business: unlike the configured key, which internal/config checks
// once at load, this path is typed by the person running the command.
func expandHome(path string) (string, *result.Error) {
	rest, ok := strings.CutPrefix(path, "~/")
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", result.ValidationError(
			"no_home_directory",
			"cannot expand ~ in the identity file path: "+err.Error(),
			"identity", nil,
		)
	}
	return filepath.Join(home, rest), nil
}

// asResultError recovers the structured error an operation returned, wrapping
// anything else as an execution failure so stderr always carries the same
// schema.
func asResultError(err error) *result.Error {
	var rerr *result.Error
	if errors.As(err, &rerr) {
		return rerr
	}
	return result.ExecutionError("operation_failed", err.Error())
}

// buildVersion reports the module version embedded by the Go toolchain, or
// "devel" when that information is not available (e.g. `go run`).
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	return info.Main.Version
}
