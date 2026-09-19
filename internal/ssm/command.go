package ssm

import (
	"regexp"
	"strings"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
)

// instanceIDPattern is the second layer guarding the destination, the same
// role assertSafeTarget plays in internal/ssh: vo.Target already accepted
// whatever the caller passed as [user@]host, and this package re-checks the
// exact string on its way into SendCommand. A "user@" prefix - meaningful for
// ssh(1) - has no meaning to SSM Run Command and is refused rather than
// silently ignored, so the same host string cannot be read two different ways
// depending on which transport a caller picked.
//
// Only an EC2 instance ID (the "i-" prefix) is accepted. A hybrid-activation
// managed instance ("mi-") is refused: Focal's threat model is an EC2 fleet
// under one IAM policy, and accepting "mi-" here would silently widen that to
// on-premises and other-cloud hosts an administrator's SendCommand policy was
// never reviewed against.
var instanceIDPattern = regexp.MustCompile(`^i-[0-9a-f]{8,17}$`)

var instanceIDAllowed = []string{"an EC2 instance ID: i- followed by 8-17 hex digits, no user@ prefix"}

func checkInstanceID(s string) *result.Error {
	if !instanceIDPattern.MatchString(s) {
		return result.ValidationError("invalid_target", "ssm target must be an EC2 instance ID", "target", instanceIDAllowed)
	}
	return nil
}

// commandLine renders cmd as the single shell line AWS-RunShellScript runs.
// It is the one place this package turns a Command into text, so the quoting
// rule stated here is the only one that exists.
//
// Every token - the program name, each argument, and the literal "sudo"/"-n"
// this package adds - passed ssh.Command's own construction-time allowlist,
// which already excludes the single quote, whitespace, "$", a backtick, a
// backslash, "*" and "?". quoteToken is nonetheless a full POSIX single-quote
// escape rather than a bare wrap, so this layer does not silently depend on
// that allowlist never changing: if a future change let a single quote
// through, the token would still round-trip through the remote shell as one
// argument instead of ending the quoting early.
func commandLine(cmd ssh.Command, sudo bool) string {
	tokens := make([]string, 0, len(cmd.Args())+3)
	if sudo {
		tokens = append(tokens, "sudo", "-n")
	}
	tokens = append(tokens, cmd.Program())
	tokens = append(tokens, cmd.Args()...)

	quoted := make([]string, len(tokens))
	for i, t := range tokens {
		quoted[i] = quoteToken(t)
	}
	return strings.Join(quoted, " ")
}

// quoteToken renders s as a single POSIX shell word using single quotes. Each
// embedded single quote is escaped by closing the current quote, emitting a
// backslash-escaped quote outside it, and reopening the quote. This is the
// standard, complete escape for POSIX shell single-quoting: it is correct for
// any byte s may contain, not only the ones argSpec/literalSpec currently
// allow.
func quoteToken(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
