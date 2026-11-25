package vo

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/youyo/focal/internal/result"
)

// FilesAllowList is the set of directories the files operation may list, as
// an administrator wrote them under operations.files.paths (ADR 0012). It is
// matched against and nothing else: no entry is ever placed in an argv.
//
// Nothing in its type says it came from the configuration file. That is held
// by guard tests instead: ParseFilesAllowList may be referred to only from
// internal/config, and internal/config hands a list out only through
// accessors that take no argument. The zero value allows nothing.
//
// The entries are held in an array rather than a slice, so every copy of a
// FilesAllowList is a copy of its entries too, and a struct holding one stays
// comparable.
type FilesAllowList struct {
	entries [maxFilesAllowListEntries]string
	n       int
}

// maxFilesAllowListEntries bounds operations.files.paths. A list longer than
// this is no longer something an administrator reviews entry by entry.
const maxFilesAllowListEntries = 32

// forbiddenFilesTrees are the trees no entry may name or sit inside: procfs,
// sysfs and devtmpfs hold magic links to other processes' descriptors and the
// kernel's own state, and over SSM the listing is taken as root.
var forbiddenFilesTrees = []string{"/proc", "/sys", "/dev"}

var filesEntrySpec = tokenSpec{
	field:   "paths",
	subject: "files path entry",
	allowed: []string{
		"an absolute path equal to its path.Clean form, other than /",
		"A-Za-z0-9 and _@+=:,./- only, at most 512 bytes",
		"not /proc, /sys, /dev or anything under them",
	},
	chars:  filesPathChars,
	first:  slashOnly,
	maxLen: maxFilesPathLen,
}

// ParseFilesAllowList validates the configured entries. Every entry must be an
// absolute path that is already in path.Clean form, other than "/" alone,
// made only of the bytes a remote argument may carry, and outside /proc, /sys
// and /dev; no entry may repeat, and there may be at most 32. The slice is
// copied, so the caller cannot change the list afterwards.
//
// An entry is named by its position in an error, never repeated back.
func ParseFilesAllowList(entries []string) (FilesAllowList, *result.Error) {
	if len(entries) > maxFilesAllowListEntries {
		return FilesAllowList{}, result.ValidationError(
			"too_many_files_path_entries",
			fmt.Sprintf("files path entries must be at most %d", maxFilesAllowListEntries),
			filesEntrySpec.field, filesEntrySpec.allowed,
		)
	}
	for i, e := range entries {
		if err := checkFilesEntry(i, e); err != nil {
			return FilesAllowList{}, err
		}
		if slices.Contains(entries[:i], e) {
			return FilesAllowList{}, result.ValidationError(
				"duplicate_files_path_entry",
				fmt.Sprintf("files path entry %d repeats an earlier entry", i),
				filesEntrySpec.field, filesEntrySpec.allowed,
			)
		}
	}
	var held [maxFilesAllowListEntries]string
	copy(held[:], entries)
	return FilesAllowList{entries: held, n: len(entries)}, nil
}

func checkFilesEntry(i int, e string) *result.Error {
	reject := func(code, reason string) *result.Error {
		return result.ValidationError(code,
			fmt.Sprintf("files path entry %d %s", i, reason),
			filesEntrySpec.field, filesEntrySpec.allowed)
	}
	if err := filesEntrySpec.check(e); err != nil {
		return reject("invalid_files_path_entry", "is not an absolute path of allowed characters")
	}
	if path.Clean(e) != e {
		return reject("invalid_files_path_entry", "is not in path.Clean form (no trailing /, //, . or ..)")
	}
	if e == "/" {
		return reject("invalid_files_path_entry", "names the whole filesystem")
	}
	for _, tree := range forbiddenFilesTrees {
		if within(e, tree) {
			return reject("forbidden_files_path_entry", "names procfs, sysfs or devtmpfs, or a path inside one of them")
		}
	}
	return nil
}

// Entries returns a copy of the configured entries, in the order they were
// written.
func (l FilesAllowList) Entries() []string { return slices.Clone(l.entries[:l.n]) }

// allows reports whether p is an entry or lies under one.
func (l FilesAllowList) allows(p string) bool {
	for _, e := range l.entries[:l.n] {
		if within(p, e) {
			return true
		}
	}
	return false
}

// within reports whether p is dir or a path under it. "/var/log2" is not
// within "/var/log".
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// FilesPath is a directory the caller asked the files operation to list that
// has been matched against a FilesAllowList. The only way to obtain one other
// than the zero value is ParseFilesPath, so a FilesPath in hand has passed the
// match; it says nothing about what is at that path on a host, which is what
// the files operation's ownership check establishes before anything is listed.
//
// It is specific to the files operation. An operation that reads file
// contents defines its own type and its own allow list (ADR 0012).
type FilesPath struct {
	s string
}

const maxFilesPathLen = 512

var filesPathSpec = tokenSpec{
	field:   "path",
	subject: "path",
	allowed: []string{
		"an absolute path equal to its path.Clean form (no trailing /, //, . or ..)",
		"A-Za-z0-9 and _@+=:,./- only, at most 512 bytes",
	},
	chars:  filesPathChars,
	first:  slashOnly,
	maxLen: maxFilesPathLen,
}

// ParseFilesPath accepts raw when it is an absolute path already in
// path.Clean form, made only of the bytes a remote argument may carry, and is
// an entry of list or lies under one. It is never normalised: the string the
// caller sent, the string matched and the string a remote argv receives are
// the same bytes.
func ParseFilesPath(raw string, list FilesAllowList) (FilesPath, *result.Error) {
	if err := filesPathSpec.check(raw); err != nil {
		return FilesPath{}, err
	}
	if path.Clean(raw) != raw {
		return FilesPath{}, filesPathSpec.reject("must be in path.Clean form: no trailing /, //, . or ..")
	}
	if !list.allows(raw) {
		return FilesPath{}, result.ValidationError(
			"path_not_allowed",
			"path is neither an allowed directory nor under one",
			filesPathSpec.field, list.Entries(),
		)
	}
	return FilesPath{s: raw}, nil
}

// String returns the path exactly as it was accepted.
func (p FilesPath) String() string { return p.s }

// IsZero reports whether p was never produced by ParseFilesPath.
func (p FilesPath) IsZero() bool { return p.s == "" }

// Chain returns every leading part of the path, shallowest first: "/", then
// each directory down to the path itself. "/var/log" gives "/", "/var" and
// "/var/log". The parts are cut from the accepted string alone, never from
// anything a host answered. The zero value has no chain.
func (p FilesPath) Chain() []string {
	if p.s == "" {
		return nil
	}
	chain := []string{"/"}
	for i := 1; i < len(p.s); i++ {
		if p.s[i] == '/' {
			chain = append(chain, p.s[:i])
		}
	}
	return append(chain, p.s)
}
