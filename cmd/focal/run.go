// Command focal is Focal's entry point. At this stage (M1+M2) it only proves
// out the startup path: load and validate the configuration, and confirm the
// validated values can build an SSH executor. The CLI subcommands that will
// actually run operations are M3 scope and are not implemented here.
package main

import (
	"flag"
	"fmt"
	"io"
	"runtime/debug"

	"github.com/youyo/focal/internal/config"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
)

// run is the whole of main's logic, factored out so it can be exercised with
// captured stdout/stderr instead of the process's real ones. It takes no
// dependency on the environment beyond what config.Load and ssh.NewOpenSSH
// already read directly (os.Getenv, os.UserHomeDir): a test steers those
// through t.Setenv rather than through a parameter here.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("focal", flag.ContinueOnError)
	fs.SetOutput(stderr)
	version := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *version {
		fmt.Fprintln(stdout, buildVersion())
		return 0
	}

	cfg, err := config.Load()
	if err != nil {
		return reportError(stderr, err)
	}

	// internal/ssh must not depend on internal/config; run is the one place
	// that copies the validated values across.
	if _, err := ssh.NewOpenSSH(ssh.Options{
		Timeout:      cfg.Timeout(),
		MaxOutput:    cfg.MaxOutput(),
		IdentityFile: cfg.IdentityFile(),
		Port:         cfg.Port(),
	}); err != nil {
		return reportError(stderr, err)
	}

	return 0
}

// reportError writes err to stderr as one line of compact JSON and returns
// the process exit code for it.
func reportError(stderr io.Writer, err *result.Error) int {
	line, encErr := result.Encode(err)
	if encErr != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	fmt.Fprintln(stderr, string(line))
	return 1
}

// buildVersion reports the module version embedded by the Go toolchain, or
// "devel" when that information is not available (e.g. a build without
// module info, such as `go run`).
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	return info.Main.Version
}
