package vo

import "github.com/youyo/focal/internal/result"

// PID is a validated Linux process identifier. Its zero value is not a usable
// identifier and renders as the empty string.
type PID struct {
	s string
}

// String returns the process identifier exactly as it was accepted.
func (p PID) String() string { return p.s }

const (
	maxPIDLen = 7
	minPID    = 1
	// maxPID is Linux PID_MAX_LIMIT on 64-bit. Rejecting 0 and negatives is
	// what keeps the process-group and every-process forms of kill(2)
	// unreachable through this type.
	maxPID = 4194304
)

var pidSpec = tokenSpec{
	field:   "pid",
	subject: "pid",
	allowed: []string{
		"a decimal integer from 1 to 4194304 without a leading zero",
	},
	chars:  digitChars,
	first:  nonZeroDigits,
	maxLen: maxPIDLen,
}

// ParsePID accepts a decimal process identifier in the range 1..4194304.
func ParsePID(v string) (PID, *result.Error) {
	if err := pidSpec.checkDecimal(v, minPID, maxPID); err != nil {
		return PID{}, err
	}
	return PID{s: v}, nil
}
