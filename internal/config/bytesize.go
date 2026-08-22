package config

import (
	"math"
	"strconv"
	"strings"

	"github.com/youyo/focal/internal/result"
)

// maxByteSizeDigits keeps the magnitude small enough that ParseInt cannot
// overflow before the unit is applied.
const maxByteSizeDigits = 12

// byteUnits is the whole accepted suffix set, longest first so that "GiB" is
// matched before the "B" it ends with. The empty suffix comes last and means
// plain bytes. Decimal units (KB, MB) are deliberately absent: offering two
// spellings that differ by 2.4% invites a limit that is not the one intended.
var byteUnits = []struct {
	suffix string
	factor int64
}{
	{"GiB", 1 << 30},
	{"MiB", 1 << 20},
	{"KiB", 1 << 10},
	{"B", 1},
	{"", 1},
}

var byteSizeAllowed = []string{
	"a decimal integer from 1 upwards without a leading zero",
	`optionally followed by one of "B", "KiB", "MiB", "GiB"`,
	"for example 10MiB",
}

// parseByteSize converts a size such as "10MiB" to a byte count. Matching is
// exact: no whitespace, no case folding, and no fractional magnitude, so a
// malformed limit is reported instead of being rounded into something the
// administrator did not write. field names the config key for the error.
func parseByteSize(v, field string) (int64, *result.Error) {
	for _, u := range byteUnits {
		digits, ok := strings.CutSuffix(v, u.suffix)
		if !ok {
			continue
		}
		if err := checkByteSizeDigits(digits, field); err != nil {
			return 0, err
		}
		// The digit count is bounded above, so this cannot overflow.
		n, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return 0, rejectByteSize(field, "must be a decimal integer")
		}
		if n > math.MaxInt64/u.factor {
			return 0, rejectByteSize(field, "is too large to represent")
		}
		return n * u.factor, nil
	}
	// The empty suffix matches every input, so this is unreachable.
	return 0, rejectByteSize(field, "must end with a known unit")
}

func checkByteSizeDigits(digits, field string) *result.Error {
	switch {
	case digits == "":
		return rejectByteSize(field, "must have a magnitude")
	case len(digits) > maxByteSizeDigits:
		return rejectByteSize(field, "has too many digits")
	case digits[0] < '1' || digits[0] > '9':
		return rejectByteSize(field, "must start with a digit from 1 to 9")
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return rejectByteSize(field, "must be digits followed by an optional unit")
		}
	}
	return nil
}

func rejectByteSize(field, reason string) *result.Error {
	return result.ValidationError("invalid_byte_size", "byte size "+reason, field, byteSizeAllowed)
}
