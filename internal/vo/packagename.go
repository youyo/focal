package vo

import "github.com/youyo/focal/internal/result"

// PackageName is a validated OS package name, the argument of rpm -q and
// dpkg-query -s. Its zero value is not a usable package name and renders as the
// empty string.
//
// It is a type of its own rather than a ServiceName because the two sets
// differ in both directions: a package name may carry "+" (libstdc++6, g++)
// that a unit name never does, and a unit name may carry ":" and "@" that a
// package name must not — dpkg reads ":" as an architecture qualifier and a
// wildcard-free name is what keeps one invocation to one package.
type PackageName struct {
	s string
}

// String returns the package name exactly as it was accepted.
func (n PackageName) String() string { return n.s }

const maxPackageNameLen = 128

var packageNameSpec = tokenSpec{
	field:   "package_name",
	subject: "package name",
	allowed: []string{
		"1-128 bytes of A-Za-z0-9._+- starting with a letter or digit",
		"for example procps, openssh-server, libstdc++6, python3.12",
	},
	chars:  packageChars,
	first:  alnumChars,
	maxLen: maxPackageNameLen,
}

// ParsePackageName accepts one package name such as procps, libstdc++6 or
// python3.12. Wildcards, architecture qualifiers and version constraints are
// not part of the grammar.
func ParsePackageName(v string) (PackageName, *result.Error) {
	if err := packageNameSpec.check(v); err != nil {
		return PackageName{}, err
	}
	return PackageName{s: v}, nil
}
