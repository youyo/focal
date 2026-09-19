package config

import (
	"time"

	"github.com/youyo/focal/internal/vo"
)

// The bounds and fallbacks Focal applies to a config file. Every one of these
// is a limit on what an administrator may ask for, so they live together here
// rather than next to the code that happens to check them.
const (
	defaultTimeout   = 30 * time.Second
	defaultMaxOutput = 10 * 1024 * 1024

	minTimeout = 1 * time.Second
	maxTimeout = 10 * time.Minute

	minMaxOutput = 1 << 10
	maxMaxOutput = 1 << 30

	minPort = 1
	maxPort = 65535

	// maxConfigBytes bounds the document before the YAML parser sees it.
	// Anchor and alias expansion turns a small file into a large object
	// graph, so the ceiling has to be applied to the bytes on disk.
	maxConfigBytes = 1 << 20
)

// defaultTransports is what focal runs with when the config file names no
// execution.transports: ssh, and ssh alone. An administrator must opt a host
// into ssm explicitly — the setting can only add to this set, and only to
// the values vo.ParseTransport accepts, so a typo is a startup error rather
// than a transport nobody asked for being silently unavailable or, worse,
// silently available.
func defaultTransports() map[vo.Transport]bool {
	return map[vo.Transport]bool{vo.TransportSSH: true}
}

// lineStreamLimits declares which operations read a stream long enough to need
// line and age limits, and the ceilings Focal applies when the config file
// names none. Membership is the whole rule: an operation listed here accepts
// max_lines and max_since, and one that is absent rejects both keys rather than
// ignoring them, so the two journal readers are the only place those limits can
// be set.
var lineStreamLimits = map[string]struct {
	maxLines string
	maxSince string
}{
	"logs":   {maxLines: "1000", maxSince: "24h"},
	"kernel": {maxLines: "1000", maxSince: "24h"},
}

// defaultOperations is what Focal runs with when no config file exists: every
// read-only inspection available, kernel details off, and — because no row
// carries a sudo mode — no privilege anywhere. A config file can turn rows on
// or off and narrow their sudo mode, but the set of operations that exist is
// fixed here and in internal/policy's capability table.
//
// This table must name only operations internal/policy declares; Load resolves
// every row through policy.Resolve, so a name that drifted out of the
// capability table fails at startup rather than silently.
var defaultOperations = []struct {
	name    string
	enabled bool
}{
	{"system", true},
	{"cpu", true},
	{"memory", true},
	{"storage", true},
	{"network", true},
	{"processes", true},
	{"service", true},
	{"logs", true},
	{"kernel", false},
	{"inspect", true},
}
