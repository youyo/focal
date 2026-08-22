package policy

import (
	"fmt"
	"slices"

	"github.com/youyo/focal/internal/result"
)

// Policy is the resolved decision for one operation: which operation it is and
// which sudo mode actually applies to it. Every field is unexported and Resolve
// is the only constructor, so no package outside internal/policy can produce a
// Policy carrying a sudo mode the capability table did not approve. The zero
// value is a valid, powerless Policy: no operation, no sudo.
type Policy struct {
	operation string
	sudo      SudoMode
}

// Operation returns the operation name this policy was resolved for, or the
// empty string for the zero value.
func (p Policy) Operation() string { return p.operation }

// Sudo returns the effective sudo mode, SudoNever for the zero value.
func (p Policy) Sudo() SudoMode { return p.sudo }

// Resolve intersects the operation's capability with the administrator's
// configured sudo mode. It fails, rather than falling back to a weaker mode,
// when name is not a declared operation or when configured is outside that
// operation's capability, so a mistaken config is reported at startup instead
// of silently changing what Focal does. The returned Policy is the zero value
// whenever an error is returned.
func Resolve(name string, configured SudoMode) (Policy, *result.Error) {
	allowed, ok := capabilities[name]
	if !ok {
		return Policy{}, result.PolicyError(
			"unknown_operation",
			fmt.Sprintf("unknown operation %q", name),
			"operations",
			operationNames(),
		)
	}
	if !slices.Contains(allowed, configured) {
		return Policy{}, result.PolicyError(
			"sudo_not_allowed",
			fmt.Sprintf("operation %q does not allow sudo mode %q", name, configured),
			fmt.Sprintf("operations.%s.sudo", name),
			modeNames(allowed),
		)
	}
	return Policy{operation: name, sudo: configured}, nil
}
