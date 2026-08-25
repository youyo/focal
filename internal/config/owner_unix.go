//go:build !windows

package config

import (
	"fmt"
	"io/fs"
	"syscall"

	"github.com/youyo/focal/internal/result"
)

// checkConfigOwner refuses a config file that belongs to someone other than the
// user focal runs as.
//
// The mode check next to it settles who may write the file; this settles whose
// file it is, which the mode cannot say. A 0644 config dropped by another user
// into a directory focal reads from passes every permission test and still
// decides which operations are enabled, whether logs and kernel escalate with
// sudo, and which private key focal offers to a host. root is accepted because
// a root-owned config is placed by whoever administers the machine, who is
// already trusted with far more than this.
func checkConfigOwner(path string, info fs.FileInfo) *result.Error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// Nothing to compare against. The mode check stands on its own
		// rather than this guessing in either direction.
		return nil
	}
	uid := uint32(syscall.Geteuid()) //nolint:gosec // a uid is what Geteuid returns
	if st.Uid == uid || st.Uid == 0 {
		return nil
	}
	return result.ValidationError(
		"insecure_config_owner",
		fmt.Sprintf("config file %s is owned by uid %d rather than by uid %d, who is running focal", path, st.Uid, uid),
		"", nil,
	)
}
