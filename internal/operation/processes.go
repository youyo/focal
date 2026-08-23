package operation

import (
	"context"
	"strings"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// Processes is the processes operation: the remote process table, optionally
// narrowed to a name or a pid.
//
// The narrowing happens here, in Focal, not on the remote host. ps(1) is asked
// the same fixed question however a caller filters — ssh.ProcessListCommand
// takes no parameters at all — and the rows are selected from what came back.
// That is deliberate: a filter composed into a remote argv is a filter whose
// syntax the remote shell would have to be trusted with, and the only way to
// never have to trust it is to never send it. Filtering locally costs one
// process table on the wire and removes the question entirely.
//
// The fields are value objects rather than strings so that "no filter" and "a
// filter that happens to be empty" cannot be confused: ParseProcessName and
// ParsePID both reject the empty string, so a zero-valued field is
// unambiguously "not asked for".
type Processes struct {
	name vo.ProcessName
	pid  vo.PID
}

var _ Operation = Processes{}

// NewProcesses builds the operation from a caller's raw filters. An empty name
// or pid means that filter was not requested; anything else must be accepted by
// internal/vo before it is stored, so the operation cannot hold a value that
// was never validated. Passing both narrows to rows matching both.
func NewProcesses(name, pid string) (Processes, *result.Error) {
	var op Processes
	if name != "" {
		parsed, err := vo.ParseProcessName(name)
		if err != nil {
			return Processes{}, err
		}
		op.name = parsed
	}
	if pid != "" {
		parsed, err := vo.ParsePID(pid)
		if err != nil {
			return Processes{}, err
		}
		op.pid = parsed
	}
	return op, nil
}

// Name returns the operation name.
func (o Processes) Name() string { return "processes" }

// Execute runs the fixed ps(1) invocation and reports the rows that match the
// operation's filters.
func (o Processes) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	cmd, cmdErr := ssh.ProcessListCommand(p)
	if cmdErr != nil {
		return result.Envelope{}, cmdErr
	}

	out, execErr := ex.Execute(ctx, t, cmd)
	if execErr != nil {
		return result.Envelope{}, execErr
	}

	part := buildPart(o.Name(), out, nil)
	// The filter runs after buildPart has read Truncated off the raw output, so
	// a cutoff is recorded against what the host actually sent rather than
	// against what survived the filter. A caller whose filter matched nothing
	// therefore still sees StatusTruncated and can tell "not running" from "cut
	// off before we got there".
	part.Stdout = processesFilter(part.Stdout, o.name.String(), o.pid.String())

	status, exitCode, durationMs, truncated := aggregate([]result.Part{part})
	return result.Envelope{
		Operation:  o.Name(),
		Host:       t.String(),
		Status:     status,
		ExitCode:   exitCode,
		DurationMs: durationMs,
		Truncated:  truncated,
		Stdout:     part.Stdout,
		Stderr:     part.Stderr,
		Error:      part.Error,
	}, nil
}

// The column positions processesFilter reads, fixed by the -eo list
// ssh.ProcessListCommand pins: pid,ppid,user,state,%cpu,%mem,etime,comm,args.
// Only the first eight columns are whitespace-free, so a row is addressable by
// field index up to comm; args is whatever follows and is not matched against.
const (
	processesPIDField  = 0
	processesCommField = 7
	processesMinFields = processesCommField + 1
)

// processesFilter returns the rows of raw ps(1) output that match name and pid,
// with the header row always kept. An empty name or pid is not a filter.
//
// The header survives even when nothing else does, so an empty answer is still
// a readable table rather than a blank string, and a caller can see which
// columns it was looking at.
func processesFilter(stdout, name, pid string) string {
	if stdout == "" || (name == "" && pid == "") {
		return stdout
	}

	trailingNewline := strings.HasSuffix(stdout, "\n")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")

	kept := make([]string, 0, len(lines))
	kept = append(kept, lines[0])
	for _, line := range lines[1:] {
		if processesRowMatches(line, name, pid) {
			kept = append(kept, line)
		}
	}

	filtered := strings.Join(kept, "\n")
	if trailingNewline {
		filtered += "\n"
	}
	return filtered
}

// processesRowMatches reports whether one ps(1) row satisfies both filters. The
// pid must match its column exactly — a prefix match would report the wrong
// process — while the name matches the comm column as a substring, because comm
// is what ps(1) truncates to the executable's basename and a caller asking for
// "postgres_exporter" should still find "postgres_export".
//
// A row with fewer columns than the format promises is not matched: it is not
// something this function can read, and silently treating it as a match would
// put a row in front of a caller that was never checked against the filter.
func processesRowMatches(line, name, pid string) bool {
	fields := strings.Fields(line)
	if len(fields) < processesMinFields {
		return false
	}
	if pid != "" && fields[processesPIDField] != pid {
		return false
	}
	if name != "" && !strings.Contains(fields[processesCommField], name) {
		return false
	}
	return true
}
