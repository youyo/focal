package vo

import (
	"regexp"
	"strconv"
	"time"

	"github.com/youyo/focal/internal/result"
)

// Timestamp is a validated absolute instant, accepted in RFC 3339 form with
// an explicit offset and whole seconds. Its zero value is not a usable
// instant: it reports 0 from Unix and renders as the empty string from both
// String and Epoch, so a Timestamp that never went through this file cannot
// reach an argv.
type Timestamp struct {
	s    string
	unix int64
}

// String returns the timestamp as it was accepted, or — for one built by
// TimestampFromUnix — the same instant in UTC. It is for messages; what an
// argv carries is Epoch.
func (t Timestamp) String() string { return t.s }

// Unix returns the instant in seconds since the Unix epoch.
func (t Timestamp) Unix() int64 { return t.unix }

// Epoch returns Unix as decimal digits, the form journalctl takes after "@".
// Every accepted Timestamp is after the epoch, so the digits never carry a
// sign; the zero value returns the empty string, which no argv accepts.
func (t Timestamp) Epoch() string {
	if t.unix <= 0 {
		return ""
	}
	return strconv.FormatInt(t.unix, 10)
}

const maxTimestampLen = len("2006-01-02T15:04:05+07:00")

// timestampShape is the whole accepted grammar: RFC 3339's date-time with an
// uppercase T, no fractional seconds, and an offset that is either Z or a
// signed hh:mm. Go's time.RFC3339 parser alone would also take a fractional
// second, so the shape is checked first and time.Parse only decides whether
// the fields name a real instant.
var timestampShape = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(Z|[+-][0-9]{2}:[0-9]{2})$`)

var timestampAllowed = []string{
	"RFC 3339 with an offset and whole seconds",
	"for example 2026-11-20T09:00:00+09:00 or 2026-11-20T00:00:00Z",
	"after 1970-01-01T00:00:00Z",
}

func rejectTimestamp(reason string) *result.Error {
	return result.ValidationError("invalid_timestamp", "timestamp "+reason, "timestamp", timestampAllowed)
}

// ParseTimestamp accepts an RFC 3339 instant with an offset and without
// fractional seconds, later than the Unix epoch.
func ParseTimestamp(v string) (Timestamp, *result.Error) {
	switch {
	case v == "":
		return Timestamp{}, rejectTimestamp("must not be empty")
	case len(v) > maxTimestampLen:
		return Timestamp{}, rejectTimestamp("must be at most 25 bytes")
	case !timestampShape.MatchString(v):
		return Timestamp{}, rejectTimestamp("must be YYYY-MM-DDTHH:MM:SS followed by Z or an offset such as +09:00")
	}
	parsed, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return Timestamp{}, rejectTimestamp("does not name a valid date and time")
	}
	if parsed.Unix() <= 0 {
		return Timestamp{}, rejectTimestamp("must be after 1970-01-01T00:00:00Z")
	}
	return Timestamp{s: v, unix: parsed.Unix()}, nil
}

// TimestampFromUnix builds a Timestamp from seconds since the epoch, for an
// instant Focal computed itself — a relative span subtracted from its own
// clock — rather than one a caller wrote. The same lower bound applies.
func TimestampFromUnix(secs int64) (Timestamp, *result.Error) {
	if secs <= 0 {
		return Timestamp{}, rejectTimestamp("must be after 1970-01-01T00:00:00Z")
	}
	return Timestamp{s: time.Unix(secs, 0).UTC().Format(time.RFC3339), unix: secs}, nil
}
