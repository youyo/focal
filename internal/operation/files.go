package operation

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// Files is the files operation: the names, types, sizes and modification
// times of what one allowed directory holds, one level deep (ADR 0012). It
// holds only the directory, already matched against the administrator's
// allow list.
//
// Two commands run, both built by ssh.FilesCommands from that one path. The
// first checks that every directory above the target is root's and writable
// by nobody else, so no one but root can swap a part of the path for a symlink
// between the check and the listing. The second lists the directory, and runs
// only when the first found nothing to refuse.
//
// Remote output is used in one direction only. The check's answer can stop a
// listing; it never adds to what is allowed and never reaches an argv. The
// listing's answer is read into four fields per entry; nothing else of either
// command's stdout or stderr is returned.
type Files struct {
	dir vo.FilesPath
}

var _ Operation = Files{}

// NewFiles builds the operation for one directory vo.ParseFilesPath accepted.
func NewFiles(dir vo.FilesPath) Files { return Files{dir: dir} }

// Name returns the operation name.
func (o Files) Name() string { return "files" }

// The fixed words a refused check reports in Data["reason"]. They are the
// only thing chosen by reading the check's answer that the caller sees.
const (
	reasonNotDirectory = "not_directory"
	reasonNotRootOwned = "not_root_owned"
	reasonACL          = "acl_present"
	reasonWritable     = "group_or_other_writable"
)

// Execute runs the check and, when it passes, the listing.
func (o Files) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	check, list, cmdErr := ssh.FilesCommands(p, o.dir)
	if cmdErr != nil {
		return result.Envelope{}, cmdErr
	}

	checkOut, checkErr := ex.Execute(ctx, t, check)
	checkPart := quietPart("check", checkOut, checkErr)
	verdict := judgeChain(o.dir.Chain(), checkOut, checkErr)
	if verdict.failed {
		return o.envelope(t, []result.Part{checkPart}, checkPart, map[string]string{
			"path":     o.dir.String(),
			"complete": "false",
		}, nil, result.ExecutionError("path_check_failed",
			"the check that the path to "+o.dir.String()+" is controlled by root alone did not complete or could not be read; nothing was listed")), nil
	}
	if verdict.reason != "" {
		return o.envelope(t, []result.Part{checkPart}, checkPart, map[string]string{
			"path":     o.dir.String(),
			"complete": "false",
			"prefix":   verdict.prefix,
			"reason":   verdict.reason,
		}, nil, result.ExecutionError("path_not_root_controlled",
			fmt.Sprintf("%s cannot be listed: %s on the path to it is %s; nothing was listed", o.dir.String(), verdict.prefix, verdict.reason))), nil
	}

	lsOut, lsErr := ex.Execute(ctx, t, list)
	lsPart := quietPart("ls", lsOut, lsErr)
	parts := []result.Part{checkPart, lsPart}
	data := map[string]string{"path": o.dir.String(), "complete": "false"}
	if !listingUsable(lsOut, lsErr) {
		return o.envelope(t, parts, lsPart, data, nil, nil), nil
	}
	l, ok := parseListing(lsOut.Stdout, lsOut.Truncated)
	if !ok {
		return o.envelope(t, parts, lsPart, data, nil, result.ExecutionError("unexpected_listing",
			"the listing of "+o.dir.String()+" does not have the shape of a directory listing; nothing is returned")), nil
	}
	complete := lsOut.ExitCode == 0 && !lsOut.Truncated && l.terminated && l.skipped == 0
	data["complete"] = strconv.FormatBool(complete)
	data["skipped_lines"] = strconv.Itoa(l.skipped)
	return o.envelope(t, parts, lsPart, data, l.items, nil), nil
}

// envelope assembles the answer. decisive is the Part whose Status, ExitCode
// and Error are reported; they are the executor's own, not recomputed. An
// error of the operation's own replaces decisive's Error, and a Status of ok
// becomes failed beside it, the exit status staying what the command returned.
func (o Files) envelope(t vo.Target, parts []result.Part, decisive result.Part, data map[string]string, items []map[string]string, own *result.Error) result.Envelope {
	env := result.Envelope{
		Operation: o.Name(),
		Host:      t.String(),
		Status:    decisive.Status,
		ExitCode:  decisive.ExitCode,
		Data:      data,
		Items:     items,
		Error:     decisive.Error,
		Parts:     parts,
	}
	if own != nil {
		env.Error = own
		if env.Status == result.StatusOK {
			env.Status = result.StatusFailed
		}
	}
	for _, part := range parts {
		env.DurationMs += part.DurationMs
		env.Truncated = env.Truncated || part.Truncated
	}
	return env
}

// quietPart is buildPart without the command's output: the check's answer
// names owners and modes, the listing's names symlink targets, and ls's stderr
// names entries; none of it is returned.
func quietPart(name string, out ssh.Output, execErr error) result.Part {
	part := buildPart(name, out, execErr)
	part.Stdout, part.Stderr = "", ""
	return part
}

// chainVerdict is the check's outcome: failed when it did not complete or its
// answer could not be read, refused (a non-empty reason) at the shallowest
// leading part that gave a reason, or neither when the listing may run.
type chainVerdict struct {
	failed bool
	prefix string
	reason string
}

// judgeChain reads the answer to ls -d -n -b --time-style=full-iso <chain...>.
//
// It works in three steps, each over the whole answer before the next starts:
// the command must have exited 0, untruncated, with its output ending in a
// newline; every line must parse, and the names the lines answer for must be
// exactly the chain — each leading part once, nothing else, in any order,
// since ls sorts its arguments; only then is each part judged, shallowest
// first. A line is matched to a leading part by its whole name, and a
// symlink's line by the name before its " -> "; a leading part holds no space,
// so the first " -> " is the separator.
func judgeChain(chain []string, out ssh.Output, execErr error) chainVerdict {
	failed := chainVerdict{failed: true}
	if execErr != nil || out.ExitCode != 0 || out.Truncated || !strings.HasSuffix(out.Stdout, "\n") {
		return failed
	}
	lines := strings.Split(strings.TrimSuffix(out.Stdout, "\n"), "\n")
	if len(lines) != len(chain) {
		return failed
	}
	byName := make(map[string]chainEntry, len(lines))
	for _, line := range lines {
		e, ok := parseChainLine(line)
		if !ok || !slices.Contains(chain, e.name) {
			return failed
		}
		if _, dup := byName[e.name]; dup {
			return failed
		}
		byName[e.name] = e
	}
	for i, prefix := range chain {
		if reason := byName[prefix].refusal(i == len(chain)-1); reason != "" {
			return chainVerdict{prefix: prefix, reason: reason}
		}
	}
	return chainVerdict{}
}

// chainEntry is one line of the check's answer, reduced to what is judged.
type chainEntry struct {
	name     string
	kind     byte
	rootUID  bool
	acl      bool
	writable bool
}

// refusal returns the first reason, in the fixed order not_directory,
// not_root_owned, acl_present, group_or_other_writable, that this leading part
// cannot be on the path. The directory itself only has to be a directory: an
// application may own the directory it writes to, and that directory can
// still be listed — what an application can change is what lies under it.
func (e chainEntry) refusal(target bool) string {
	switch {
	case e.kind != 'd':
		return reasonNotDirectory
	case target:
		return ""
	case !e.rootUID:
		return reasonNotRootOwned
	case e.acl:
		return reasonACL
	case e.writable:
		return reasonWritable
	}
	return ""
}

// modePositions is what each of the nine permission characters of a mode may
// be: read, write, then execute with setuid, setgid or sticky, for owner,
// group and others.
var modePositions = [9]string{"r-", "w-", "xsS-", "r-", "w-", "xsS-", "r-", "w-", "xtT-"}

// parseChainLine reads mode, links, uid, gid, size, date, time and offset,
// then exactly one space and the name. Only this check reads a mode strictly:
// ten characters, or eleven with an SELinux "." or an ACL "+" last; a mode it
// cannot read refuses the whole answer. Write permission is read from the
// group's and the others' write positions alone, whatever the group is. The
// size column is not read: a BLOCK_SIZE the host sets can turn it into "4.0K".
func parseChainLine(line string) (chainEntry, bool) {
	fields, rest, ok := cutColumns(line, 8)
	if !ok {
		return chainEntry{}, false
	}
	mode, links, uid, gid := fields[0], fields[1], fields[2], fields[3]
	if !digits(links) || !digits(uid) || !digits(gid) {
		return chainEntry{}, false
	}
	e := chainEntry{kind: mode[0], rootUID: uid == "0"}
	switch len(mode) {
	case 10:
	case 11:
		switch mode[10] {
		case '.':
		case '+':
			e.acl = true
		default:
			return chainEntry{}, false
		}
	default:
		return chainEntry{}, false
	}
	for i, allowed := range modePositions {
		if !strings.ContainsRune(allowed, rune(mode[i+1])) {
			return chainEntry{}, false
		}
	}
	e.writable = mode[5] == 'w' || mode[8] == 'w'
	if e.kind == 'l' {
		name, _, found := strings.Cut(rest, " -> ")
		if !found {
			return chainEntry{}, false
		}
		rest = name
	}
	e.name = rest
	return e, true
}

// cutColumns splits the first n columns off line. Columns are separated by
// runs of 0x20 and nothing else: not a tab, not a no-break space, not any
// other character a locale might print. The rest — the name column — must
// follow the last column after exactly one space and be non-empty, so a name
// can never begin with a space that was taken as part of the separator.
func cutColumns(line string, n int) (fields []string, rest string, ok bool) {
	for range n {
		i := strings.IndexByte(line, ' ')
		if i <= 0 {
			return nil, "", false
		}
		fields = append(fields, line[:i])
		line = line[i:]
		if len(fields) < n {
			line = strings.TrimLeft(line, " ")
		}
	}
	if len(line) < 2 || line[0] != ' ' || line[1] == ' ' {
		return nil, "", false
	}
	return fields, line[1:], true
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// listingUsable reports whether the listing's exit status lets its lines be
// read at all: 0; 1, which GNU ls returns when some entries could not be
// stat'ed; or the -1 of a run killed at max_output. Any other status, a -1
// that was not a cut, and an executor error return no entries.
func listingUsable(out ssh.Output, execErr error) bool {
	if execErr != nil {
		return false
	}
	switch out.ExitCode {
	case 0, 1:
		return true
	case -1:
		return out.Truncated
	}
	return false
}

// listing is what parseListing read.
type listing struct {
	items []map[string]string
	// skipped counts the lines after the summary line that could not be read
	// as an entry. Their content is not returned.
	skipped int
	// terminated is whether the output ended with a newline.
	terminated bool
}

// parseListing reads ls -A -b -g -o --time-style=full-iso --block-size=1.
//
// When the output was cut — Truncated, or not ending in a newline whatever
// Truncated says — the last line is dropped before anything is read, so a half
// line is never taken for an entry. The first line must be the summary line:
// two words separated by spaces, the second all digits (its first word is
// translated by some locales, so it is not compared). Without it nothing is
// returned: ls was not shown a directory. Every other line becomes an entry
// or is counted in skipped.
func parseListing(stdout string, truncated bool) (listing, bool) {
	l := listing{terminated: strings.HasSuffix(stdout, "\n")}
	lines := strings.Split(stdout, "\n")
	if l.terminated {
		lines = lines[:len(lines)-1]
	}
	if (truncated || !l.terminated) && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 || !summaryLine(lines[0]) {
		return listing{}, false
	}
	for _, line := range lines[1:] {
		item, ok := parseListingLine(line)
		if !ok {
			l.skipped++
			continue
		}
		l.items = append(l.items, item)
	}
	return l, true
}

func summaryLine(line string) bool {
	word, number, found := strings.Cut(line, " ")
	if !found || word == "" {
		return false
	}
	return digits(strings.TrimLeft(number, " "))
}

var (
	listingDate   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	listingTime   = regexp.MustCompile(`^[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{9}$`)
	listingOffset = regexp.MustCompile(`^[+-][0-9]{4}$`)
)

// maxSizeDigits bounds the size column before it is parsed: a uint64 has at
// most twenty decimal digits.
const maxSizeDigits = 20

// parseListingLine reads mode, links, size, date, time and offset, then one
// space and the name. The type is the mode's first character alone, and the
// rest of the mode is not read here, so an SELinux "." or an ACL "+" after it
// does not cost the entry. The time is rebuilt as RFC 3339 to the second with
// the host's offset, "+00:00" included; the remote text is never passed on.
func parseListingLine(line string) (map[string]string, bool) {
	fields, rest, ok := cutColumns(line, 6)
	if !ok {
		return nil, false
	}
	mode, links, size, date, clock, offset := fields[0], fields[1], fields[2], fields[3], fields[4], fields[5]
	kind := entryKind(mode[0])
	if kind == "" || !digits(links) || !digits(size) || len(size) > maxSizeDigits {
		return nil, false
	}
	n, err := strconv.ParseUint(size, 10, 64)
	if err != nil {
		return nil, false
	}
	if !listingDate.MatchString(date) || !listingTime.MatchString(clock) || !listingOffset.MatchString(offset) {
		return nil, false
	}
	mtime, err := time.Parse("2006-01-02 15:04:05.000000000 -0700", date+" "+clock+" "+offset)
	if err != nil {
		return nil, false
	}
	name, ok := entryName(rest, kind == "symlink")
	if !ok {
		return nil, false
	}
	return map[string]string{
		"name":  name,
		"type":  kind,
		"size":  strconv.FormatUint(n, 10),
		"mtime": mtime.Format("2006-01-02T15:04:05-07:00"),
	}, true
}

func entryKind(c byte) string {
	switch c {
	case '-':
		return "file"
	case 'd':
		return "directory"
	case 'l':
		return "symlink"
	case 'c', 'b', 'p', 's', 'D', 'M', 'n', 'P', '?':
		return "other"
	}
	return ""
}

// entryName reads the name column as ls -b wrote it. A backslash and the byte
// after it are one unit, so only a space that is not escaped separates. A name
// that is not a symlink's may not contain one; a symlink's name ends at the
// first, which must open " -> " and a non-empty target, and the target is
// dropped. Splitting at the first " -> " in the text instead would cut a
// symlink named "a ->" — written "a\ -> -> target" — after "a\".
//
// The name is returned with its escapes as ls wrote them: decoding "\n" would
// put the control characters -b took out back into the answer. A name holding
// a raw control byte or any byte at or above 0x80 is not returned at all: -b
// escapes those only in the C locale, and the remote locale is not Focal's to
// fix.
func entryName(rest string, symlink bool) (string, bool) {
	end := -1
	for i := 0; i < len(rest) && end < 0; i++ {
		switch rest[i] {
		case '\\':
			if i+1 >= len(rest) {
				return "", false
			}
			i++
		case ' ':
			if !symlink || !strings.HasPrefix(rest[i:], " -> ") || len(rest) == i+len(" -> ") {
				return "", false
			}
			end = i
		}
	}
	name := rest
	switch {
	case symlink && end < 0:
		return "", false
	case end >= 0:
		name = rest[:end]
	}
	if name == "" {
		return "", false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c >= 0x7f {
			return "", false
		}
	}
	return name, true
}
