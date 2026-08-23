package vo

import (
	"strconv"

	"github.com/youyo/focal/internal/result"
)

// LineLimit is a validated cap on how many lines an operation may return.
// Its zero value is not a usable limit and renders as the empty string.
type LineLimit struct {
	s string
	n int
}

// String returns the limit exactly as it was accepted.
func (l LineLimit) String() string { return l.s }

// Int returns the limit as the integer ParseLineLimit accepted. Callers
// capping a request read it here instead of re-parsing String(); the zero
// value reports 0, so a limit that never went through ParseLineLimit cannot
// exceed any ceiling.
func (l LineLimit) Int() int { return l.n }

const (
	maxLineLimitLen = 6
	minLineLimit    = 1
	maxLineLimit    = 100000
)

var lineLimitSpec = tokenSpec{
	field:   "line_limit",
	subject: "line limit",
	allowed: []string{
		"a decimal integer from 1 to 100000 without a leading zero",
	},
	chars:  digitChars,
	first:  nonZeroDigits,
	maxLen: maxLineLimitLen,
}

// ParseLineLimit accepts a decimal integer in the range 1..100000.
func ParseLineLimit(v string) (LineLimit, *result.Error) {
	if err := lineLimitSpec.checkDecimal(v, minLineLimit, maxLineLimit); err != nil {
		return LineLimit{}, err
	}
	// The digits and the range are already checked, so the value fits an int.
	n, err := strconv.Atoi(v)
	if err != nil {
		return LineLimit{}, lineLimitSpec.reject("must be a decimal integer")
	}
	return LineLimit{s: v, n: n}, nil
}
