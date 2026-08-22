// Package ssh owns the only place in Focal where a remote command is built and
// the only place one is run. A Command cannot be constructed outside this
// package: its fields are unexported and newCommand is its sole constructor,
// so every program name and every argument that reaches a remote host has
// passed the allowlist in this file, and the sudo mode carried alongside them
// is always the one policy.Resolve decided. boundary_test.go asserts those
// properties against the package source, so a later change that opens a second
// construction path fails the tests rather than the review.
package ssh

import (
	"fmt"
	"slices"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
)

// Command is a validated remote command with its sudo mode burned in at
// construction time. It is a value: copying one cannot change its sudo mode,
// and Args hands back a copy so a caller cannot rewrite the argv it was given.
// The zero value names no program and carries policy.SudoNever, so a Command
// that was never built by a factory can neither run nor escalate.
type Command struct {
	program string
	args    []string
	sudo    policy.SudoMode
}

// Program returns the program name to run on the remote host.
func (c Command) Program() string { return c.program }

// Args returns a copy of the arguments, in argv order.
func (c Command) Args() []string { return slices.Clone(c.args) }

// Sudo returns the sudo mode the resolved policy granted this command. It is
// the executor's only input for deciding whether to prefix sudo -n.
func (c Command) Sudo() policy.SudoMode { return c.sudo }

// IsZero reports whether c is the zero value, i.e. was not produced by a
// factory in this package.
func (c Command) IsZero() bool { return c.program == "" }

// newCommand validates program and args and burns p.Sudo() into the result.
//
// It takes the whole policy.Policy rather than a policy.SudoMode so that the
// only way to obtain a Command with any privilege is to have gone through
// policy.Resolve, where the operation's capability and the administrator's
// configuration are intersected. A caller holding only the zero Policy gets a
// command that can never be escalated.
func newCommand(p policy.Policy, program string, args ...string) (Command, *result.Error) {
	if err := programSpec.check(program); err != nil {
		return Command{}, err
	}
	for _, arg := range args {
		if err := argSpec.check(arg); err != nil {
			return Command{}, err
		}
	}
	return Command{program: program, args: slices.Clone(args), sudo: p.Sudo()}, nil
}

// maxTokenLen bounds every token of the argv. Nothing Focal runs needs a
// longer one, and the ceiling is what stops a caller-supplied value from
// growing into an argument list the remote host refuses as a whole.
const maxTokenLen = 512

// charSet is a byte-membership table for one accepted character class. This
// package validates its own tokens rather than trusting the value objects that
// produced them: internal/vo guards what enters Focal, and this file guards
// what leaves it for a remote argv, so neither layer alone is load-bearing.
type charSet [256]bool

// newCharSet builds a charSet from a spec of single bytes and inclusive "a-z"
// ranges, e.g. "A-Za-z0-9.-". A "-" that cannot open a range is a literal
// hyphen, which is why the trailing "-" convention is used.
func newCharSet(spec string) charSet {
	var s charSet
	for i := 0; i < len(spec); i++ {
		if i+2 < len(spec) && spec[i+1] == '-' {
			for b := spec[i]; b <= spec[i+2]; b++ {
				s[b] = true
			}
			i += 2
			continue
		}
		s[spec[i]] = true
	}
	return s
}

// tokenSpec is the complete rule one token must satisfy, stated as data so the
// program name and the arguments cannot drift into separately maintained
// rules.
type tokenSpec struct {
	// field names the rejected input in Error.Field, in the same snake_case
	// the JSON envelope uses; Error.Code is derived from it.
	field string
	// subject is the human-readable name used in Error.Message.
	subject string
	// allowed states the accepted form in Error.Allowed.
	allowed []string
	// chars is the set every byte of the token must belong to.
	chars charSet
	// first is the additional set the leading byte must belong to. It is what
	// keeps a token from being read as a flag by the program it is passed to.
	first charSet
}

var (
	// A program name is one lowercase word, so nothing that could be read as a
	// path, an option or a value ever occupies argv[0] of the remote command.
	programSpec = tokenSpec{
		field:   "program",
		subject: "program",
		allowed: []string{"lowercase letters, digits and hyphens, starting with a letter", "at most 512 bytes"},
		chars:   newCharSet("a-z0-9-"),
		first:   newCharSet("a-z"),
	}
	// Arguments accept the wider set a value object may legitimately carry —
	// paths, service names, timestamps, key=value pairs — and nothing else. No
	// whitespace, shell metacharacter, quote or control byte is a member, and a
	// leading hyphen is refused so that a caller-supplied value can never turn
	// into an option of the program it is handed to.
	argSpec = tokenSpec{
		field:   "args",
		subject: "command argument",
		allowed: []string{"A-Za-z0-9 and _@+=:,./- , not starting with a hyphen", "at most 512 bytes"},
		chars:   newCharSet("A-Za-z0-9_@+=:,./-"),
		first:   newCharSet("A-Za-z0-9_@+=:,./"),
	}
)

// check applies the spec to a raw token. The rejected value is never copied
// into the message: an operator reading the error learns which input was
// refused and what would have been accepted, not the payload itself.
func (s tokenSpec) check(v string) *result.Error {
	switch {
	case v == "":
		return s.reject("must not be empty")
	case len(v) > maxTokenLen:
		return s.reject(fmt.Sprintf("must be at most %d bytes", maxTokenLen))
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

func (s tokenSpec) reject(reason string) *result.Error {
	return result.ValidationError("invalid_"+s.field, s.subject+" "+reason, s.field, s.allowed)
}
