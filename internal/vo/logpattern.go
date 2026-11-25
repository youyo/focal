package vo

import (
	"errors"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"

	"github.com/youyo/focal/internal/result"
)

// LogPattern and LogRegex carry the text a caller narrows journal entries by.
// Unlike every other value object in this package they are never placed in a
// remote argv: Focal reads the entries back and matches them itself, so the
// text may hold spaces and shell metacharacters that no argv token could. What
// is still refused is what would make a message or a log line misleading to
// read back — control characters — and an unbounded length.

const maxLogPatternLen = 256

// logPatternCheck applies the shared length and control-character rule. The
// rejected text is never copied into the message, the same rule tokenSpec
// follows.
func logPatternCheck(v, field, subject string, allowed []string) *result.Error {
	reject := func(reason string) *result.Error {
		return result.ValidationError("invalid_"+field, subject+" "+reason, field, allowed)
	}
	switch {
	case v == "":
		return reject("must not be empty")
	case len(v) > maxLogPatternLen:
		return reject("must be at most 256 bytes")
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return reject("contains a control character at byte " + strconv.Itoa(i))
		}
	}
	return nil
}

var logPatternAllowed = []string{
	"1 to 256 bytes of text without control characters",
	"matched as fixed text, case-sensitively",
}

var logRegexAllowed = []string{
	"1 to 256 bytes without control characters",
	"a Go RE2 regular expression (no backreferences or lookaround)",
}

// LogPattern is fixed text an entry must contain.
type LogPattern struct {
	s string
}

// String returns the pattern exactly as it was accepted.
func (p LogPattern) String() string { return p.s }

// Matches reports whether text contains the pattern, byte for byte. The zero
// value matches nothing.
func (p LogPattern) Matches(text string) bool {
	return p.s != "" && strings.Contains(text, p.s)
}

// ParseLogPattern accepts 1 to 256 bytes with no control character.
func ParseLogPattern(v string) (LogPattern, *result.Error) {
	if err := logPatternCheck(v, "log_pattern", "log pattern", logPatternAllowed); err != nil {
		return LogPattern{}, err
	}
	return LogPattern{s: v}, nil
}

// LogRegex is an RE2 regular expression an entry must match somewhere.
type LogRegex struct {
	s  string
	re *regexp.Regexp
}

// String returns the expression exactly as it was accepted.
func (r LogRegex) String() string { return r.s }

// Matches reports whether the expression matches anywhere in text. The zero
// value matches nothing.
func (r LogRegex) Matches(text string) bool {
	return r.re != nil && r.re.MatchString(text)
}

// ParseLogRegex accepts what ParseLogPattern accepts, provided Go's regexp
// package compiles it. RE2 runs in time linear in the text, so no expression a
// caller writes can make matching a large journal read take longer than
// reading it. A compile error is reported by its kind only: the message
// regexp builds quotes the expression, which is the rejected value.
func ParseLogRegex(v string) (LogRegex, *result.Error) {
	if err := logPatternCheck(v, "log_regex", "log regex", logRegexAllowed); err != nil {
		return LogRegex{}, err
	}
	re, err := regexp.Compile(v)
	if err != nil {
		reason := "is not a valid regular expression"
		var serr *syntax.Error
		if errors.As(err, &serr) {
			reason += ": " + string(serr.Code)
		}
		return LogRegex{}, result.ValidationError("invalid_log_regex", "log regex "+reason, "log_regex", logRegexAllowed)
	}
	return LogRegex{s: v, re: re}, nil
}
