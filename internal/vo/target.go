package vo

import (
	"fmt"
	"net"
	"strings"

	"github.com/youyo/focal/internal/result"
)

// Target is a validated SSH destination in [user@]host form. Its zero value
// is not a usable target and renders as the empty string.
type Target struct {
	s string
}

// String returns the destination exactly as it was accepted, ready to be
// placed in an ssh argv after the "--" separator.
func (t Target) String() string { return t.s }

const (
	maxTargetLen   = 320
	maxTargetUser  = 32
	maxTargetHost  = 255
	maxTargetLabel = 63
	minIPv6Literal = 2
	maxIPv6Literal = 45
)

// targetAllowed is the accepted form reported to the caller on every target
// rejection. A port is deliberately absent: it is configured once in
// execution.port and passed as ssh -p, never carried inside the destination.
var targetAllowed = []string{
	"[user@]host",
	"user: 1-32 bytes of A-Za-z0-9._- starting with a letter or digit",
	"host: dot-separated labels of A-Za-z0-9_-, each 1-63 bytes starting with a letter or digit, 255 bytes total",
	"IPv6 literal in square brackets, e.g. [2001:db8::1]",
}

var (
	targetUserSpec = tokenSpec{
		field:   "target",
		subject: "target user",
		allowed: targetAllowed,
		chars:   userChars,
		first:   alnumChars,
		maxLen:  maxTargetUser,
	}
	targetLabelSpec = tokenSpec{
		field:   "target",
		subject: "target host label",
		allowed: targetAllowed,
		chars:   hostLabelChar,
		first:   alnumChars,
		maxLen:  maxTargetLabel,
	}
	// targetSpec bounds the destination as a whole before it is split, so a
	// pathological input is refused before any parsing work is done.
	targetSpec = tokenSpec{
		field:   "target",
		subject: "target",
		allowed: targetAllowed,
		chars:   newCharSet("A-Za-z0-9._:[]@-"),
		// "[" is the only non-alphanumeric leading byte, and only because a
		// bracketed IPv6 literal starts with it.
		first:  newCharSet("A-Za-z0-9["),
		maxLen: maxTargetLen,
	}
)

// ParseTarget accepts an SSH destination of the form [user@]host. The host is
// a dot-separated name, an IPv4 address, an .ssh/config Host alias, or a
// bracketed IPv6 literal. Everything else is rejected, including host:port:
// the port belongs in execution.port, not in the destination.
func ParseTarget(v string) (Target, *result.Error) {
	if err := targetSpec.check(v); err != nil {
		return Target{}, err
	}
	user, host := "", v
	if at := strings.IndexByte(v, '@'); at >= 0 {
		user, host = v[:at], v[at+1:]
		if strings.IndexByte(host, '@') >= 0 {
			return Target{}, targetSpec.reject(`must contain at most one "@"`)
		}
		if err := targetUserSpec.check(user); err != nil {
			return Target{}, err
		}
	}
	if err := checkTargetHost(host); err != nil {
		return Target{}, err
	}
	return Target{s: v}, nil
}

func checkTargetHost(host string) *result.Error {
	if host != "" && host[0] == '[' {
		return checkIPv6Literal(host)
	}
	if len(host) > maxTargetHost {
		return targetSpec.reject(fmt.Sprintf("host must be at most %d bytes", maxTargetHost))
	}
	// An empty host, a leading or trailing dot, and the ".." of a traversal
	// attempt all surface here as an empty label.
	for _, label := range strings.Split(host, ".") {
		if err := targetLabelSpec.check(label); err != nil {
			return err
		}
	}
	return nil
}

// checkIPv6Literal accepts only the bracketed form. The inner text is
// additionally required to contain a colon, so a bracketed IPv4 address —
// which net.ParseIP would otherwise accept — is refused rather than given a
// second, redundant spelling.
func checkIPv6Literal(host string) *result.Error {
	if !strings.HasSuffix(host, "]") {
		return targetSpec.reject(`IPv6 literal must be enclosed in "[" and "]"`)
	}
	inner := host[1 : len(host)-1]
	if len(inner) < minIPv6Literal || len(inner) > maxIPv6Literal {
		return targetSpec.reject(fmt.Sprintf("IPv6 literal must be %d-%d bytes", minIPv6Literal, maxIPv6Literal))
	}
	for i := 0; i < len(inner); i++ {
		if !ipv6Chars[inner[i]] {
			return targetSpec.reject(fmt.Sprintf("IPv6 literal contains a character that is not allowed at byte %d", i))
		}
	}
	if strings.IndexByte(inner, ':') < 0 {
		return targetSpec.reject("IPv6 literal must contain a colon")
	}
	if ip := net.ParseIP(inner); ip == nil || ip.To16() == nil {
		return targetSpec.reject("IPv6 literal is not a valid address")
	}
	return nil
}
