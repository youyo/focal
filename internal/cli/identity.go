package cli

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/youyo/focal/internal/result"
)

// This file is focal serve's identity table: the aliases an agent may name and
// the key files they stand for.
//
// The indirection is the point. An agent that could name a key file could
// search for one, and a Remote MCP server is exactly where that ability must
// not be. So the operator registers the keys once, on the command line that
// starts the server, and a tool call chooses among those registrations by
// name. A name nobody registered is refused, and the refusal names only other
// aliases — never a path — because which keys exist on the host is not
// something a tool call gets to learn.

// defaultIdentityAlias is the registration a call that names no identity gets.
// It is an ordinary alias with an agreed name rather than a separate flag, so
// there is one table and one way to put a key in it.
const defaultIdentityAlias = "default"

// maxIdentityAlias bounds an alias so that a refusal cannot be used to echo an
// arbitrary amount of caller-supplied text back into an agent's context.
const maxIdentityAlias = 64

// identities is the alias→key file table one focal serve invocation was
// started with. The zero value is a usable empty table: it resolves no alias
// and leaves an unnamed identity to the configuration file and ssh(1).
type identities struct {
	paths map[string]string
}

// newIdentities reads the --identity registrations, each written alias=path.
// Every path is checked here, once, under the same rules internal/config
// applies to execution.identity_file — a key focal offers has been through
// the same door as the key focal was configured with, so registering one is
// not a way around those checks.
func newIdentities(specs []string) (identities, *result.Error) {
	ids := identities{paths: make(map[string]string, len(specs))}
	for _, spec := range specs {
		alias, raw, ok := strings.Cut(spec, "=")
		if !ok || raw == "" {
			return identities{}, result.ValidationError(
				"invalid_identity_registration",
				fmt.Sprintf("identity registration %q is not written alias=path", spec),
				"--identity",
				[]string{"alias=path, e.g. default=~/.ssh/id_ed25519"},
			)
		}
		if err := checkIdentityAlias(alias); err != nil {
			return identities{}, err
		}
		if _, dup := ids.paths[alias]; dup {
			return identities{}, result.ValidationError(
				"duplicate_identity_alias",
				fmt.Sprintf("identity alias %q is registered twice", alias),
				"--identity",
				[]string{"one registration per alias"},
			)
		}
		path, err := resolveIdentityFile(raw)
		if err != nil {
			return identities{}, err
		}
		ids.paths[alias] = path
	}
	return ids, nil
}

// resolve answers with the key file an alias stands for. The empty alias is a
// call that named no identity: it resolves to the default registration when
// there is one, and otherwise to the empty string, which leaves the choice to
// the configuration file and then to ssh(1)'s own ~/.ssh/config.
func (i identities) resolve(alias string) (string, *result.Error) {
	if alias == "" {
		return i.paths[defaultIdentityAlias], nil
	}
	path, ok := i.paths[alias]
	if !ok {
		return "", result.ValidationError(
			"unknown_identity",
			"no identity is registered under that name",
			"identity",
			i.aliases(),
		)
	}
	return path, nil
}

// aliases returns the registered alias names, sorted, for the allowed list of
// a refusal and for anything else that reports what this server offers.
func (i identities) aliases() []string {
	return slices.Sorted(maps.Keys(i.paths))
}

// checkIdentityAlias keeps an alias to the shape of a name. It is not a path,
// not a shell word and not free text: it is a label an operator chose and an
// agent repeats back, and anything richer than that would be a way to smuggle
// content through a registration or a refusal.
func checkIdentityAlias(alias string) *result.Error {
	reject := func(reason string) *result.Error {
		return result.ValidationError(
			"invalid_identity_alias",
			"identity alias "+reason,
			"--identity",
			[]string{fmt.Sprintf("1-%d characters of A-Z a-z 0-9 . _ -", maxIdentityAlias)},
		)
	}
	switch {
	case alias == "":
		return reject("must not be empty")
	case len(alias) > maxIdentityAlias:
		return reject(fmt.Sprintf("must be at most %d bytes", maxIdentityAlias))
	}
	for _, r := range alias {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return reject(fmt.Sprintf("must not contain %q", string(r)))
		}
	}
	return nil
}

// resolveIdentityFile expands a leading ~/ the way OpenSSH does and refuses a
// key other local users can read. It mirrors the check internal/config makes
// on execution.identity_file; the two are separate because a registration
// arrives on the command line rather than in the configuration file, and the
// answer must be the same either way.
func resolveIdentityFile(raw string) (string, *result.Error) {
	path, err := expandHome(raw)
	if err != nil {
		return "", err
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return "", result.ValidationError(
			"unreadable_identity_file",
			"cannot read the identity file: "+statErr.Error(),
			"--identity", nil,
		)
	}
	if !info.Mode().IsRegular() {
		return "", result.ValidationError(
			"unreadable_identity_file",
			"identity file is not a regular file",
			"--identity", nil,
		)
	}
	if perm := info.Mode().Perm(); perm&0o044 != 0 {
		return "", result.ValidationError(
			"insecure_identity_file_permissions",
			fmt.Sprintf("identity file mode %#o is readable by group or other", perm),
			"--identity",
			[]string{"0600", "0400"},
		)
	}
	return path, nil
}
