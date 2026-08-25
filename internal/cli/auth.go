package cli

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/youyo/focal/internal/result"
)

// This file is the one thing focal will check about a caller: whether the
// request carries the shared secret focal serve was started with.
//
// It is deliberately not authentication in any larger sense. There is no
// identity behind the token, no expiry, no revocation and no per-caller
// anything — a single fixed secret says only "whoever sent this was given the
// secret", which is exactly as much as a hop between a proxy and the service
// behind it needs. Anything richer belongs in the authenticating proxy focal
// has always expected in front of it, and putting it here would make focal a
// second place to get authorization wrong.
//
// The check lives in internal/cli rather than internal/mcp for the same reason
// everything else about reaching a host does: internal/mcp is the protocol and
// nothing else. This is an ordinary http.Handler wrapped around the one it
// builds.

const (
	// upstreamTokenEnv is where focal prefers to be given the token: an
	// environment variable is not in the process list the way an argument
	// is.
	upstreamTokenEnv = "FOCAL_UPSTREAM_TOKEN" //nolint:gosec // the name of the variable, not a secret
	// minUpstreamTokenLen is the shortest shared secret focal will accept,
	// in bytes. A long-lived bearer token is guessed offline at whatever
	// rate the attacker's hardware allows, so the figure is the 256-bit
	// recommendation for exactly this kind of secret rather than anything
	// about what is comfortable to type.
	minUpstreamTokenLen = 32
	// bearerScheme is the authorization scheme focal accepts, matched
	// without regard to case as RFC 9110 requires.
	bearerScheme = "bearer"
	// unauthorizedBody is the whole of what a refused request is told. A
	// caller that has the token does not need a reason, and one that does
	// not must not be given a way to tell which part was wrong.
	unauthorizedBody = "unauthorized\n"
)

// upstreamToken is the shared secret this invocation was started with, or the
// empty string when it was started with none.
//
// The layering is the one every other override in focal uses: what the command
// line said wins, and the environment fills in for a command line that said
// nothing. Reading it here rather than storing it on serveOptions keeps the
// secret out of the struct that gets passed around and logged in test
// failures.
func upstreamToken(o serveOptions) string {
	if o.upstreamToken != "" {
		return o.upstreamToken
	}
	token := os.Getenv(upstreamTokenEnv)
	// Every ssh(1) focal starts inherits this process's environment, and
	// focal deliberately honours the operator's own ~/.ssh/config — a
	// "SendEnv FOCAL_*" there would carry the shared secret to the very
	// hosts focal is inspecting. The variable has been read, so drop it:
	// nothing downstream reads it again, and what is not in the environment
	// cannot be forwarded or read out of /proc/<pid>/environ.
	os.Unsetenv(upstreamTokenEnv)
	return token
}

// upstreamAuth is the check itself: the expected token, reduced once at
// startup to the digest every request is compared against.
type upstreamAuth struct {
	// digest is SHA-256 of the expected token. The comparison is made on
	// digests rather than on the tokens themselves because
	// subtle.ConstantTimeCompare returns early when the two lengths differ
	// (golang/go#18936), which would time-leak the length of the secret to
	// anyone allowed to guess at it. Two digests are always the same
	// length, so there is nothing left for the early return to say.
	digest [sha256.Size]byte
}

// newUpstreamAuth builds the check for token, or reports why focal will not
// accept token as a shared secret. An empty token is not a rejection: it is an
// invocation that asked for no authentication, and it gets no checker.
//
// Both refusals are made at startup, before anything is bound, and neither
// repeats the token back — a rejected secret is still a secret, and focal's
// errors are written to a stderr that ends up in logs.
func newUpstreamAuth(token string) (*upstreamAuth, *result.Error) {
	if token == "" {
		return nil, nil
	}
	if len(token) < minUpstreamTokenLen {
		return nil, invalidUpstreamToken(
			"the upstream token is %d bytes, and focal requires at least %d",
			len(token), minUpstreamTokenLen)
	}
	// Printable ASCII, checked byte by byte rather than rune by rune: what
	// has to survive is the token's trip through an HTTP header, and a
	// header field value is bytes.
	for i := range len(token) {
		if token[i] < 0x21 || token[i] > 0x7e {
			return nil, invalidUpstreamToken(
				"the upstream token contains a byte at offset %d that cannot be sent in an Authorization header", i)
		}
	}
	return &upstreamAuth{digest: sha256.Sum256([]byte(token))}, nil
}

// invalidUpstreamToken is one rejection of a shared secret, in the shape every
// other startup rejection in this package takes.
func invalidUpstreamToken(format string, args ...any) *result.Error {
	return result.ValidationError(
		"invalid_upstream_token",
		fmt.Sprintf(format, args...),
		"--upstream-token",
		[]string{fmt.Sprintf(
			"at least %d bytes of printable ASCII, e.g. the output of `openssl rand -hex 32`, "+
				"preferably given in %s rather than on the command line",
			minUpstreamTokenLen, upstreamTokenEnv)},
	)
}

// authorize reports whether the request headers carry the expected token.
func (u *upstreamAuth) authorize(h http.Header) bool {
	values := h.Values("Authorization")
	// Exactly one. Two headers are not a request focal can read a single
	// intention from, and accepting a request because one of them was right
	// would let a caller hide a second credential behind a first — the kind
	// of ambiguity that becomes a bypass once something in front of focal
	// reads the other one.
	if len(values) != 1 {
		return false
	}
	scheme, presented, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, bearerScheme) || presented == "" {
		return false
	}
	got := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(got[:], u.digest[:]) == 1
}

// wrap returns next behind the token check.
func (u *upstreamAuth) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !u.authorize(r.Header) {
			// Bearer alone. The MCP specification allows a
			// resource_metadata parameter here so a client can
			// discover an authorization server, and focal has none
			// to discover: naming one would send clients off to
			// negotiate an OAuth flow that does not exist.
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, unauthorizedBody)
			return
		}
		next.ServeHTTP(w, r)
	})
}
