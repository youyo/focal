package vo

import "github.com/youyo/focal/internal/result"

// LineLimit is a validated cap on how many lines an operation may return.
// Its zero value is not a usable limit and renders as the empty string.
type LineLimit struct {
	s string
}

// String returns the limit exactly as it was accepted.
func (l LineLimit) String() string { return l.s }

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
	return LineLimit{s: v}, nil
}
