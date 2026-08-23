package cli

import (
	"fmt"
	"io"

	"github.com/youyo/focal/internal/result"
)

// The exit codes focal leaves with. The split is between "focal did not
// understand what you asked for" and "focal understood and refused, or the run
// failed": an agent can retry the second with different values, while the first
// means the invocation itself was malformed.
const (
	// exitOK means an Envelope was produced and written to stdout. The remote
	// command's own outcome is inside it — a unit that is down is a successful
	// inspection, not a failed invocation.
	exitOK = 0
	// exitRejected means focal produced no Envelope: input was rejected by a
	// value object or by policy, or the run itself failed.
	exitRejected = 1
	// exitUsage means the command line could not be read at all — an unknown
	// flag, an unknown operation, the wrong number of arguments.
	exitUsage = 2
)

// usageForm is the one invocation shape focal accepts, reported in the allowed
// list of every usage error so a caller that got the order wrong is told the
// order rather than shown a flag table.
const usageForm = "focal [flags] HOST OPERATION [args]"

// cliError is a structured error together with the exit code focal leaves with
// for it. Every failure path in this package produces one, so stderr carries
// the same result.Error JSON schema whichever layer said no.
type cliError struct {
	err  *result.Error
	code int
}

func (e *cliError) Error() string { return e.err.Error() }

// usageError builds the exitUsage error for a command line focal could not
// read. It is a validation error because that is what it is — input rejected
// before it reached policy — and the code distinguishes it from a value object
// rejection only by the exit status.
func usageError(code, message, field string, allowed []string) *cliError {
	if allowed == nil {
		allowed = []string{usageForm}
	}
	return &cliError{err: result.ValidationError(code, message, field, allowed), code: exitUsage}
}

// rejected wraps an error a value object, policy, config or the executor
// produced. Those already carry their own kind and code; this only attaches the
// exit status.
func rejected(err *result.Error) *cliError {
	return &cliError{err: err, code: exitRejected}
}

// writeError writes err to w as one line of compact JSON. A failure to encode
// the error itself falls back to its text form rather than losing it.
func writeError(w io.Writer, err *result.Error) {
	line, encErr := result.Encode(err)
	if encErr != nil {
		fmt.Fprintln(w, err.Error())
		return
	}
	fmt.Fprintln(w, string(line))
}

// writeEnvelope writes env to w: one line of compact JSON by default, indented
// when pretty is set. JSON is the default rather than an option because the
// caller focal is built for reads it, and a human-facing rendering that had to
// stay in sync with the schema would be a second description of the same shape.
func writeEnvelope(w io.Writer, env result.Envelope, pretty bool) *cliError {
	var (
		out []byte
		err error
	)
	if pretty {
		out, err = env.EncodeIndent()
	} else {
		out, err = env.Encode()
	}
	if err != nil {
		return rejected(result.ExecutionError("encode_failed", "cannot encode the result: "+err.Error()))
	}
	fmt.Fprintln(w, string(out))
	return nil
}
