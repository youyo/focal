//go:build !windows

package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// socketDir returns a directory to put a test socket in, short enough that the
// path under it still fits sun_path (104 bytes on darwin). t.TempDir() derives
// its name from the test's own, and TMPDIR can be long before that, so between
// them they can exhaust the limit this package exists to check.
func socketDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if len(base)+len("/focal1234567890/focal.sock") > maxUnixSocketPathLen {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "focal")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// socketPath returns an unused socket path in a directory of its own.
func socketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(socketDir(t), "focal.sock")
}

// mkdirMode makes a directory whose permission is exactly mode, umask and all.
// The permissions the tests below need are the loose ones focal is supposed to
// object to, which is why the linter's objection to them is expected here.
func mkdirMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil { //nolint:gosec // the loose permission is the fixture
		t.Fatalf("chmod: %v", err)
	}
}

// currentUmask reads the process umask, which the C interface only offers by
// setting it, so it is set back at once.
func currentUmask() int {
	old := syscall.Umask(0)
	syscall.Umask(old)
	return old
}

// TestListenUnixCreatesASocketOnlyItsOwnerCanReach is the whole point of the
// Unix socket path: the boundary is the file's permission, so the file has to
// be a socket and has to be 0600.
func TestListenUnixCreatesASocketOnlyItsOwnerCanReach(t *testing.T) {
	s := newServeApp(t)
	path := socketPath(t)

	ln, err := s.app.listen(serveOptions{listen: unixListenScheme + path})
	if err != nil {
		t.Fatalf("listen: %v", err.err)
	}
	defer ln.Close()

	info, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatalf("lstat: %v", statErr)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Errorf("mode = %v, want a socket", info.Mode())
	}
	if perm := info.Mode().Perm(); perm != unixSocketMode {
		t.Errorf("permission = %#o, want %#o", perm, unixSocketMode)
	}
}

// TestListenUnixDoesNotApplyTheBindPolicy pins the decision that the two are
// orthogonal: --allow-unauthenticated-listen answers whether something off
// this machine can reach the address, and a socket on the filesystem has no
// such address. So a Unix socket binds without the flag and without the
// warning that flag exists to accompany.
func TestListenUnixDoesNotApplyTheBindPolicy(t *testing.T) {
	s := newServeApp(t)

	ln, err := s.app.listen(serveOptions{listen: unixListenScheme + socketPath(t)})
	if err != nil {
		t.Fatalf("listen: %v", err.err)
	}
	defer ln.Close()

	if got := s.stderr.String(); got != "" {
		t.Errorf("stderr = %q, want nothing for a Unix socket bind", got)
	}
}

// TestListenUnixRestoresTheUmask keeps the narrowed umask to the one call that
// needs it: it is process-wide, so anything else creating a file while it is
// in force would silently get focal's permissions.
func TestListenUnixRestoresTheUmask(t *testing.T) {
	s := newServeApp(t)
	before := currentUmask()

	ln, err := s.app.listen(serveOptions{listen: unixListenScheme + socketPath(t)})
	if err != nil {
		t.Fatalf("listen: %v", err.err)
	}
	defer ln.Close()

	if after := currentUmask(); after != before {
		t.Errorf("umask = %#o after listen, want %#o", after, before)
	}
}

// TestListenUnixRefusesASocketSomethingIsListeningOn is the first of the three
// things that can already be at the path: a live server, which focal must not
// take the socket away from.
func TestListenUnixRefusesASocketSomethingIsListeningOn(t *testing.T) {
	s := newServeApp(t)
	path := socketPath(t)

	other, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer other.Close()

	ln, cliErr := s.app.listen(serveOptions{listen: unixListenScheme + path})
	if cliErr == nil {
		_ = ln.Close()
		t.Fatal("focal took over a socket another process was listening on")
	}
	if cliErr.err.Code != "listen_address_in_use" {
		t.Errorf("code = %q, want listen_address_in_use", cliErr.err.Code)
	}
	if _, statErr := os.Lstat(path); statErr != nil {
		t.Errorf("the other server's socket was removed: %v", statErr)
	}
}

// TestListenUnixReplacesAStaleSocket is the second: a socket left behind by a
// process that died, which nothing is behind and focal may take.
func TestListenUnixReplacesAStaleSocket(t *testing.T) {
	s := newServeApp(t)
	path := socketPath(t)

	dead, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Closing normally would unlink the file; a crash does not, and a crash
	// is the case being tested.
	dead.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := dead.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ln, cliErr := s.app.listen(serveOptions{listen: unixListenScheme + path})
	if cliErr != nil {
		t.Fatalf("a stale socket was not replaced: %v", cliErr.err)
	}
	defer ln.Close()

	info, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatalf("lstat: %v", statErr)
	}
	if perm := info.Mode().Perm(); perm != unixSocketMode {
		t.Errorf("permission = %#o, want %#o", perm, unixSocketMode)
	}
}

// TestListenUnixRefusesToRemoveWhatIsNotASocket is the third: anything that is
// not focal's own kind of file is refused and left exactly where it was, so a
// mistyped path cannot delete something.
func TestListenUnixRefusesToRemoveWhatIsNotASocket(t *testing.T) {
	dir := socketDir(t)
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(dir, "link.sock")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, path := range []string{regular, link, subdir} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			s := newServeApp(t)

			ln, cliErr := s.app.listen(serveOptions{listen: unixListenScheme + path})
			if cliErr == nil {
				_ = ln.Close()
				t.Fatal("a path that was not a socket was accepted")
			}
			if cliErr.code != exitUsage {
				t.Errorf("exit code = %d, want %d", cliErr.code, exitUsage)
			}
			if cliErr.err.Code != "invalid_listen_address" {
				t.Errorf("code = %q, want invalid_listen_address", cliErr.err.Code)
			}
			if _, statErr := os.Lstat(path); statErr != nil {
				t.Errorf("focal removed it anyway: %v", statErr)
			}
		})
	}
}

// TestListenUnixRejectsAnUnusablePath collects the four shapes of path focal
// refuses before it creates anything: one it cannot resolve, one the kernel
// cannot hold, one with nowhere to put the file, and one whose directory
// another user could write focal's socket out from under.
func TestListenUnixRejectsAnUnusablePath(t *testing.T) {
	dir := socketDir(t)

	loose := filepath.Join(dir, "loose")
	mkdirMode(t, loose, 0o777)

	tests := []struct {
		name string
		path string
	}{
		{"relative", "focal.sock"},
		{"too long", "/" + strings.Repeat("s", maxUnixSocketPathLen)},
		{"no such directory", filepath.Join(dir, "absent", "focal.sock")},
		{"directory anyone can write", filepath.Join(loose, "focal.sock")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServeApp(t)

			ln, err := s.app.listen(serveOptions{listen: unixListenScheme + tt.path})
			if err == nil {
				_ = ln.Close()
				t.Fatalf("%q was accepted", tt.path)
			}
			if err.code != exitUsage {
				t.Errorf("exit code = %d, want %d", err.code, exitUsage)
			}
			if err.err.Code != "invalid_listen_address" {
				t.Errorf("code = %q, want invalid_listen_address", err.err.Code)
			}
			if _, statErr := os.Lstat(tt.path); statErr == nil {
				t.Errorf("%q was created despite being refused", tt.path)
			}
		})
	}
}

// TestListenUnixAcceptsAStickyDirectory keeps the directory check to what it
// is for. /tmp is writable by everyone, but its sticky bit means only the
// owner of a file there can replace it, which is the property being demanded.
func TestListenUnixAcceptsAStickyDirectory(t *testing.T) {
	dir := socketDir(t)
	sticky := filepath.Join(dir, "sticky")
	mkdirMode(t, sticky, 0o777|os.ModeSticky)

	s := newServeApp(t)
	ln, err := s.app.listen(serveOptions{listen: unixListenScheme + filepath.Join(sticky, "focal.sock")})
	if err != nil {
		t.Fatalf("a sticky directory was refused: %v", err.err)
	}
	_ = ln.Close()
}

// TestServeOverAUnixSocket is the whole path end to end: a client that can
// only reach focal through the socket file gets its tool call answered, and
// when the server stops the socket file is gone.
func TestServeOverAUnixSocket(t *testing.T) {
	withConfig(t, "")
	s := newServeApp(t)
	path := socketPath(t)
	o := serveOptions{listen: unixListenScheme + path}

	handler, herr := s.app.serveHandler(o)
	if herr != nil {
		t.Fatalf("serveHandler: %v", herr.err)
	}
	ln, lerr := s.app.listen(o)
	if lerr != nil {
		t.Fatalf("listen: %v", lerr.err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *cliError, 1)
	go func() { done <- s.app.serveOn(ctx, ln, handler) }()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
	// The host in the URL is never resolved: every connection goes to the
	// socket path the dialer above closes over.
	content, isError := callToolWith(t, client, "http://focal.invalid/", "inspect_cpu", map[string]any{"host": "web"})
	if isError {
		t.Fatalf("call failed: %s", content)
	}
	if len(s.opts) != 1 {
		t.Fatalf("built %d executors, want 1", len(s.opts))
	}
	client.CloseIdleConnections()

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveOn returned %v after a clean shutdown", err.err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket file outlived the server: %v", err)
	}
}
