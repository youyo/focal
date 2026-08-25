//go:build windows

package cli

import (
	"net"
	"os"

	"github.com/youyo/focal/internal/result"
)

// checkDirOwner has nothing to say on Windows, where listenUnixSocket refuses
// the socket form outright and no path is ever checked for one.
func checkDirOwner(_ string, _ os.FileInfo) *result.Error { return nil }

// listenUnixSocket refuses on Windows, where focal cannot make the promise the
// socket form is for.
//
// Windows has Unix domain sockets, but not the two things focal relies on to
// keep one private: there is no umask to narrow before the socket is created,
// and os.Chmod there sets only the read-only attribute rather than POSIX
// permission bits, so neither the narrow creation nor the 0600 afterwards
// would mean anything. A socket focal cannot restrict to its owner is not a
// boundary, and offering one that looks like a boundary is worse than
// offering none — so this refuses instead, and an operator on Windows puts an
// authenticating proxy in front of a loopback address as before.
func listenUnixSocket(_ string) (net.Listener, *cliError) {
	return nil, usageError(
		"unsupported_listen_address",
		"focal cannot restrict a Unix domain socket to its owner on Windows, so it will not serve on one",
		"--listen",
		[]string{"host:port, e.g. " + defaultListenAddress},
	)
}
