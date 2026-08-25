//go:build !windows

package cli

import (
	"net"
	"os"
	"syscall"

	"github.com/youyo/focal/internal/result"
)

// checkDirOwner reports whether dir is owned by someone who could replace it
// out from under focal. Only the user focal runs as and root qualify as owners
// that cannot: anyone else may rename the directory aside and offer their own
// socket at the path, which would put them between focal and the proxy in front
// of it — including for the shared secret that proxy sends.
func checkDirOwner(dir string, info os.FileInfo) *result.Error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// Without the underlying stat there is nothing to compare, and
		// guessing in either direction would be worse than the mode
		// check standing on its own.
		return nil
	}
	if uid := uint32(os.Geteuid()); st.Uid != uid && st.Uid != 0 { //nolint:gosec // a uid is what Geteuid returns
		return invalidUnixSocketPath(
			"%q is owned by uid %d, who could replace it and the socket in it", dir, st.Uid)
	}
	return nil
}

// listenUnixSocket creates the socket at path, which the caller has already
// established is a path focal may create one at.
//
// The permission is set twice, and both times are load-bearing. net.Listen
// creates the socket under the process umask, so the umask is narrowed first:
// that is what keeps the file from existing, even briefly, as anything wider
// than 0600. os.Chmod afterwards is what makes 0600 true rather than merely
// likely, since a umask only ever removes bits and cannot add the ones a
// stricter one already took away. Doing only the second leaves the window
// golang/go#11822 describes, in which the socket is already connectable at
// whatever the umask allowed.
//
// The umask is process-wide, so it is put back immediately: any other file
// created while it is narrowed would silently get focal's permissions too.
func listenUnixSocket(path string) (net.Listener, *cliError) {
	old := syscall.Umask(unixSocketUmask)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, rejected(result.ExecutionError("listen_failed", "cannot listen: "+err.Error()))
	}

	// Chmod by path rather than on the descriptor: fchmod is not reliably
	// honoured for sockets.
	if err := os.Chmod(path, unixSocketMode); err != nil {
		_ = ln.Close()
		return nil, rejected(result.ExecutionError("listen_failed",
			"cannot restrict the socket to its owner: "+err.Error()))
	}
	return ln, nil
}
