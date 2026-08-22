package vo

import (
	"strconv"

	"github.com/youyo/focal/internal/result"
)

// Duration is a validated relative time span such as 15m or 7d, in the form
// journalctl --since accepts. Its zero value is not a usable duration and
// renders as the empty string.
type Duration struct {
	s string
}

// String returns the duration exactly as it was accepted.
func (d Duration) String() string { return d.s }

const (
	maxDurationLen    = 8
	maxDurationDigits = 6
	minDurationSecs   = 1
	maxDurationSecs   = 30 * 24 * 60 * 60
)

// durationUnitSecs is the whole set of accepted unit suffixes. Go's own
// duration syntax (1h30m, 1.5h, ns/us/ms) is deliberately not accepted: one
// magnitude and one unit keeps the accepted set small enough to enumerate.
var durationUnitSecs = map[byte]int64{
	's': 1,
	'm': 60,
	'h': 60 * 60,
	'd': 24 * 60 * 60,
}

var durationSpec = tokenSpec{
	field:   "duration",
	subject: "duration",
	allowed: []string{
		`1-6 digits without a leading zero followed by one of "s", "m", "h", "d"`,
		"between 1s and 30d",
		"for example 30s, 15m, 24h, 7d",
	},
	chars:  durationChars,
	first:  nonZeroDigits,
	maxLen: maxDurationLen,
}

// ParseDuration accepts a magnitude and a single unit suffix, bounded to the
// range 1s..30d.
func ParseDuration(v string) (Duration, *result.Error) {
	if err := durationSpec.check(v); err != nil {
		return Duration{}, err
	}
	unit, ok := durationUnitSecs[v[len(v)-1]]
	if !ok {
		return Duration{}, durationSpec.reject(`must end with one of "s", "m", "h", "d"`)
	}
	magnitude := v[:len(v)-1]
	if len(magnitude) > maxDurationDigits {
		return Duration{}, durationSpec.reject("must have at most 6 digits")
	}
	for i := 0; i < len(magnitude); i++ {
		if !digitChars[magnitude[i]] {
			return Duration{}, durationSpec.reject("must be digits followed by exactly one unit")
		}
	}
	// The leading digit and the digit count are already checked, so the
	// magnitude cannot overflow and the product cannot either.
	n, err := strconv.ParseInt(magnitude, 10, 64)
	if err != nil {
		return Duration{}, durationSpec.reject("must be digits followed by exactly one unit")
	}
	if secs := n * unit; secs < minDurationSecs || secs > maxDurationSecs {
		return Duration{}, durationSpec.reject("must be between 1s and 30d")
	}
	return Duration{s: v}, nil
}
