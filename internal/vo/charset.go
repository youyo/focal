// Package vo defines the value objects that carry externally supplied input
// into Focal's SSH layer. A value object exists only if its Parse function
// accepted the raw string against a positive allowlist, so any value that
// reaches command construction is already safe to place in an argv: there is
// no path by which a shell metacharacter, a control character, whitespace, a
// leading "-", or an ssh_config "%" expansion token can be carried inside one.
package vo

import (
	"fmt"
	"strconv"

	"github.com/youyo/focal/internal/result"
)

// charSet is a byte-membership table for one accepted character class. Every
// allowlist in this package is expressed as a charSet built here, so the set
// of bytes a value object may contain is decided in this file alone and never
// re-derived by an individual Parse function.
type charSet [256]bool

// newCharSet builds a charSet from a spec of single bytes and inclusive "a-z"
// ranges, e.g. "A-Za-z0-9._-". A "-" that cannot open a range is a literal
// hyphen, which is why the trailing "-" convention is used.
func newCharSet(spec string) charSet {
	var s charSet
	for i := 0; i < len(spec); i++ {
		if i+2 < len(spec) && spec[i+1] == '-' {
			// The counter is an int, not a byte: "b <= hi" with a byte
			// counter never goes false for a range ending at 0xff, and
			// the loop would spin forever. Every spec in this package
			// is a constant well below that today, so this is about
			// the next one.
			for b := int(spec[i]); b <= int(spec[i+2]); b++ {
				s[b] = true
			}
			i += 2
			continue
		}
		s[spec[i]] = true
	}
	return s
}

// The accepted character classes. Anything absent from these sets is
// rejected: bytes above 0x7f, control characters including NUL, CR and LF,
// whitespace, quotes, and every shell metacharacter are simply never members.
var (
	alnumChars    = newCharSet("A-Za-z0-9")
	digitChars    = newCharSet("0-9")
	nonZeroDigits = newCharSet("1-9")
	userChars     = newCharSet("A-Za-z0-9._-")
	hostLabelChar = newCharSet("A-Za-z0-9_-")
	serviceChars  = newCharSet("A-Za-z0-9:_.@-")
	ipv6Chars     = newCharSet("0-9A-Fa-f:.")
	durationChars = newCharSet("0-9dhms")
)

// tokenSpec is the complete rule set one token must satisfy, stated
// declaratively next to the value object that owns it. Keeping the rules as
// data means adding a value object cannot accidentally weaken the shared
// reject behaviour.
type tokenSpec struct {
	// field names the rejected input in Error.Field, in the same snake_case
	// used by the JSON envelope; Error.Code is derived from it.
	field string
	// subject is the human-readable name used in Error.Message.
	subject string
	// allowed states the accepted form in Error.Allowed.
	allowed []string
	// chars is the set every byte of the token must belong to.
	chars charSet
	// first is the additional set the leading byte must belong to. This is
	// what rejects "-oProxyCommand=evil" and "%h" at position zero.
	first charSet
	// maxLen is the inclusive byte ceiling for the token.
	maxLen int
}

// check applies the spec to a raw token. The rejected value is never copied
// into the message: an operator reading the error learns which input was
// refused and what would have been accepted, not the payload itself.
func (s tokenSpec) check(v string) *result.Error {
	switch {
	case v == "":
		return s.reject("must not be empty")
	case len(v) > s.maxLen:
		return s.reject(fmt.Sprintf("must be at most %d bytes", s.maxLen))
	case !s.first[v[0]]:
		return s.reject("must start with an allowed character")
	}
	for i := 0; i < len(v); i++ {
		if !s.chars[v[i]] {
			return s.reject(fmt.Sprintf("contains a character that is not allowed at byte %d", i))
		}
	}
	return nil
}

// checkDecimal applies the spec and then the numeric range, for the value
// objects whose token is a decimal integer without a leading zero.
func (s tokenSpec) checkDecimal(v string, lo, hi int64) *result.Error {
	if err := s.check(v); err != nil {
		return err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return s.reject("must be a decimal integer")
	}
	if n < lo || n > hi {
		return s.reject(fmt.Sprintf("must be between %d and %d", lo, hi))
	}
	return nil
}

func (s tokenSpec) reject(reason string) *result.Error {
	return result.ValidationError("invalid_"+s.field, s.subject+" "+reason, s.field, s.allowed)
}
