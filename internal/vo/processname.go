package vo

import "github.com/youyo/focal/internal/result"

// ProcessName is a validated process name used only as a local filter over
// ps(1) output. It never reaches a remote argv.
//
// That is the whole reason it exists as its own type rather than reusing
// ServiceName. Focal runs one fixed ps(1) invocation with no filter in it and
// narrows the rows itself ("filtering should happen locally whenever
// practical"), so a ProcessName is matched against bytes that already came back
// from the host. Giving that job to ServiceName would leave one type carrying
// two meanings — a systemd unit that does reach an argv, and a filter that does
// not — and the wider character set the unit form needs (":" for a slice path)
// would then also be accepted where a filter is read. The allowlist here is
// therefore the narrower one, and the type is checked against the same
// injection table as its siblings even though nothing downstream executes it.
type ProcessName struct {
	s string
}

// String returns the process name exactly as it was accepted.
func (n ProcessName) String() string { return n.s }

const maxProcessNameLen = 64

// processChars is stated here rather than alongside the classes in charset.go
// because it belongs to this type alone: it is the only allowlist in the
// package that guards a value which is never placed in a command, so sharing it
// with a value object that is would be exactly the conflation the type exists
// to prevent. It is still a charSet built by newCharSet, so the membership rule
// itself is not restated.
var processChars = newCharSet("A-Za-z0-9_.@-")

var processNameSpec = tokenSpec{
	field:   "process_name",
	subject: "process name",
	allowed: []string{
		"1-64 bytes of A-Za-z0-9 and _.@- starting with a letter or digit",
		"for example nginx, postgres_exporter, java-app.1",
	},
	chars:  processChars,
	first:  alnumChars,
	maxLen: maxProcessNameLen,
}

// ParseProcessName accepts a process name to match against the comm column of
// ps(1) output, such as nginx, postgres_exporter or java-app.1.
func ParseProcessName(v string) (ProcessName, *result.Error) {
	if err := processNameSpec.check(v); err != nil {
		return ProcessName{}, err
	}
	return ProcessName{s: v}, nil
}
