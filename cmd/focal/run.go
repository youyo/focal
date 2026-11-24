// Command focal is Focal's entry point. Everything it does lives in
// internal/cli; this package only hands over the process's arguments and
// streams and returns the exit code, so there is one place the command line is
// defined and one place it is tested.
package main

import (
	"io"

	"github.com/youyo/focal/internal/cli"
)

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return cli.Execute(args, stdin, stdout, stderr)
}
