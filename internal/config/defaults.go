package config

import "time"

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

	// logsOperation is the only operation that reads a stream long enough
	// to need line and age limits.
	logsOperation = "logs"

	defaultLogsMaxLines = "1000"
	defaultLogsMaxSince = "24h"
)

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
}
