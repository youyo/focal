package vo

import "github.com/youyo/focal/internal/result"

// CountBy names the bucket a logs request counts its entries in. It is one of
// a fixed pair of words; like LogPattern it is read by Focal and never placed
// in a remote argv.
type CountBy struct {
	s string
}

// The accepted buckets.
const (
	countByHour = "hour"
	countByDay  = "day"
)

// String returns the bucket name as it was accepted, or the empty string for
// the zero value, which means no count was asked for.
func (c CountBy) String() string { return c.s }

// IsHour reports whether entries are counted per hour.
func (c CountBy) IsHour() bool { return c.s == countByHour }

// IsDay reports whether entries are counted per day.
func (c CountBy) IsDay() bool { return c.s == countByDay }

// ParseCountBy accepts "hour" or "day".
func ParseCountBy(v string) (CountBy, *result.Error) {
	switch v {
	case countByHour, countByDay:
		return CountBy{s: v}, nil
	}
	return CountBy{}, result.ValidationError(
		"invalid_count_by",
		"count_by must be one of hour, day",
		"count_by",
		[]string{countByHour, countByDay},
	)
}
