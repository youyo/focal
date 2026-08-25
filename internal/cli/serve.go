package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/youyo/focal/internal/config"
	"github.com/youyo/focal/internal/mcp"
	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// This file is `focal serve`: the same operations the command line offers,
// offered to a coding agent over MCP instead.
//
// Authenticating a user is not focal's job and is not going to become one —
// that belongs in front of it, in an authenticating reverse proxy that
// terminates OAuth and forwards only what it has already vouched for. What
// focal owes in return is a default that cannot be exposed by accident, which
// is why it binds to loopback and refuses anything else unless the operator
// says, in as many words, that something else is in front of it.
//
// What focal does offer is a boundary on the hop between that proxy and
// itself, in either of two forms. A Unix domain socket draws it in the
// filesystem rather than in the network: the socket is created 0600, so what
// may reach focal is whatever runs as the user focal runs as, and reaching it
// from another machine is not a thing that can be arranged. A shared secret
// (--upstream-token, see auth.go) draws it in the request instead: focal
// answers nothing that does not carry the token it was started with. Neither
// knows who the caller is, both compose with the other, and either is enough
// to make a non-loopback bind something the operator chose rather than
// something that happened.
//
// Everything below the HTTP surface is internal/cli's, not internal/mcp's:
// the identity aliases, the connection options, the ssh(1) client. The MCP
// layer builds an operation and a target and hands them back here.

const (
	// defaultListenAddress is loopback because focal serve is
	// unauthenticated. Port 8080 is the conventional upstream port for the
	// proxy that fronts it.
	defaultListenAddress = "127.0.0.1:8080"

	// unixListenScheme marks a --listen value as the path of a Unix domain
	// socket rather than a host:port.
	unixListenScheme = "unix:"
	// exampleUnixListenAddress is the socket form as the help and the
	// rejections spell it: a path under a directory of focal's own, not a
	// shared one.
	exampleUnixListenAddress = unixListenScheme + "/run/focal/focal.sock"
	// maxUnixSocketPathLen is the longest socket path focal accepts.
	// sun_path holds 104 bytes on darwin and 108 on Linux, one of which is
	// the terminating NUL, so 103 is what fits wherever focal runs. The
	// kernel's own complaint about a longer path is "invalid argument",
	// which names neither the path nor the limit.
	maxUnixSocketPathLen = 103
	// unixSocketMode is what makes a socket a boundary at all: the user
	// focal runs as, and nobody else.
	unixSocketMode = 0o600
	// unixSocketUmask is that same permission expressed as the mask in
	// force while the socket is created, so it is never briefly wider.
	unixSocketUmask = 0o177
	// staleSocketDialTimeout bounds the connection that decides whether a
	// socket already on disk still has a server behind it. Connecting to a
	// socket on this machine either succeeds or fails immediately; a
	// timeout here means the question was not answered, not that the answer
	// was no.
	staleSocketDialTimeout = 100 * time.Millisecond

	// serveReadHeaderTimeout bounds how long a client may take to send its
	// request headers, so a connection that never finishes one cannot hold
	// a slot open.
	serveReadHeaderTimeout = 10 * time.Second
	// serveReadTimeout bounds the whole request. MCP requests are small
	// JSON documents; nothing legitimate takes longer.
	serveReadTimeout = 30 * time.Second
	// serveIdleTimeout bounds how long a kept-alive connection may sit
	// unused between requests.
	serveIdleTimeout = 2 * time.Minute
	// serveWriteFactor turns the per-command execution ceiling into the
	// ceiling on one whole response. A composite operation runs several
	// commands in sequence, so the response outlives any single one of
	// them; the factor is larger than the number of commands any operation
	// runs, so this deadline can only fire on a response that was never
	// going to arrive.
	serveWriteFactor = 12
	// serveMaxInFlight is how many requests focal will work on at once.
	//
	// A request is not a cheap thing to accept: one `inspect` fans out to
	// six sub-operations and seventeen separate ssh(1) processes, each with
	// its own connection and handshake and its own output budget. Nothing
	// below this bounds that, so without a ceiling here a caller that simply
	// keeps asking turns into hundreds of concurrent ssh processes against
	// the inspected host and a matching pile of buffered output on this one.
	//
	// Eight is chosen to be comfortably more than an agent asking questions
	// one after another needs, and far less than what makes focal look like
	// a source of trouble to the host it is pointed at. Requests over the
	// ceiling wait rather than fail, so a burst is slowed instead of being
	// turned into errors the agent has to reason about.
	serveMaxInFlight = 8
)

// serveOptions is what `focal serve` was started with.
type serveOptions struct {
	listen               string
	allowUnauthenticated bool
	identities           []string
	user                 string
	config               string
	// upstreamToken is --upstream-token as it was given, which is not
	// necessarily the token in force: see upstreamToken in auth.go, which
	// falls back to the environment.
	upstreamToken string
}

// newServeCommand builds the serve subcommand. Its flags describe where to
// listen, which keys to offer and who to log in as — never what to run, which
// stays fixed in focal's own operation table.
func newServeCommand(a *app) *cobra.Command {
	var o serveOptions
	cmd := &cobra.Command{
		Use:   "serve [flags]",
		Short: "Serve focal's operations to a coding agent over MCP",
		Long: "focal serve exposes the same read-only inspections the command line\n" +
			"offers as MCP tools, over stateless HTTP.\n\n" +
			"focal authenticates no user of its own: put an authenticating proxy in\n" +
			"front of it. It binds to loopback and refuses any other address unless\n" +
			"--allow-unauthenticated-listen says one is there, or an upstream token\n" +
			"is set.\n\n" +
			"--upstream-token, or " + upstreamTokenEnv + " in preference to it, makes focal\n" +
			"answer only requests carrying that token as Authorization: Bearer\n" +
			"<token>. It is the shared secret for the hop from the proxy, not a\n" +
			"login: it says nothing about who is calling, and it encrypts nothing.\n\n" +
			"--listen " + exampleUnixListenAddress + " serves on a Unix domain socket\n" +
			"instead. The socket is created with permission 0600, so only the user\n" +
			"focal runs as can reach it; nothing off this machine can, and\n" +
			"--allow-unauthenticated-listen does not apply. A token set alongside it\n" +
			"is still demanded.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.serve(cmd.Context(), o); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.SetOut(a.stdout)
	cmd.SetErr(a.stderr)

	flags := cmd.Flags()
	flags.StringVar(&o.listen, "listen", defaultListenAddress,
		"address to listen on, as host:port, or as "+exampleUnixListenAddress+" for a Unix domain socket")
	flags.BoolVar(&o.allowUnauthenticated, "allow-unauthenticated-listen", false,
		"permit binding an address other than loopback, where focal is reachable without authentication")
	flags.StringVar(&o.upstreamToken, "upstream-token", "",
		"shared secret every request must carry as Authorization: Bearer <token>; "+
			upstreamTokenEnv+" sets the same thing without putting it in the process list")
	flags.StringArrayVar(&o.identities, "identity", nil,
		"register a private key as alias=path, repeatable; a tool call may name only the alias")
	flags.StringVar(&o.user, "user", "", "default user to log in as, when a tool call and its host name none")
	flags.StringVar(&o.config, "config", "", "configuration file to read instead of the default location")

	return cmd
}

// serve is one focal serve invocation: build the service, bind, run until the
// context ends.
//
// The order matters. Everything that can be refused — the configuration, the
// key registrations, the address itself — is refused before a socket exists,
// so a misconfigured server never reaches a state where an agent could reach
// it.
func (a *app) serve(ctx context.Context, o serveOptions) *cliError {
	handler, err := a.serveHandler(o)
	if err != nil {
		return err
	}
	ln, err := a.listen(o)
	if err != nil {
		return err
	}
	return a.serveOn(ctx, ln, handler)
}

// serveHandler is the MCP HTTP surface for one invocation, together with the
// write deadline the configured execution ceiling implies for it.
type serveHandler struct {
	http.Handler
	write time.Duration
}

// serveHandler loads the configuration, registers the identity aliases and
// builds the MCP handler over them, behind the token check when there is one.
func (a *app) serveHandler(o serveOptions) (*serveHandler, *cliError) {
	cfg, cfgErr := loadConfigFrom(o.config)
	if cfgErr != nil {
		return nil, rejected(cfgErr)
	}
	ids, idsErr := newIdentities(o.identities)
	if idsErr != nil {
		return nil, usageError(idsErr.Code, idsErr.Message, idsErr.Field, idsErr.Allowed)
	}
	auth, authErr := newUpstreamAuth(upstreamToken(o))
	if authErr != nil {
		return nil, usageError(authErr.Code, authErr.Message, authErr.Field, authErr.Allowed)
	}
	runner := &serveRunner{cfg: cfg, identities: ids, newExecutor: a.newExecutor}
	handler := mcp.NewHandler(cfg, runner, mcp.Options{
		Version:     buildVersion(),
		DefaultUser: o.user,
	})
	// A browser the operator did not ask for is as much a caller as the
	// proxy is, and on a loopback bind it is the likelier one: any page the
	// operator visits can reach 127.0.0.1. CrossOriginProtection refuses a
	// request a browser has labelled cross-site, and lets through the ones
	// that carry no such label at all, which is every non-browser MCP client.
	// The SDK's own rebinding guard only compares Host against the listener,
	// so it lapses exactly when --allow-unauthenticated-listen is used; this
	// does not depend on the address focal bound.
	handler = http.NewCrossOriginProtection().Handler(handler)
	handler = limitInFlight(handler, serveMaxInFlight)
	if auth != nil {
		handler = auth.wrap(handler)
		if o.upstreamToken != "" {
			// The flag works, and focal takes it, but an argument
			// is readable by every process on the machine for as
			// long as focal runs. Saying so once at startup is the
			// only thing focal can do about that.
			fmt.Fprintf(a.stderr,
				"focal serve: warning: --upstream-token puts the shared secret in this machine's\n"+
					"process list, where any local process can read it. Set %s instead.\n",
				upstreamTokenEnv)
		}
	}
	return &serveHandler{Handler: handler, write: cfg.Timeout() * serveWriteFactor}, nil
}

// limitInFlight caps how many requests are being worked on at once, holding the
// rest at the door.
//
// It sits inside the shared-secret check, so a request that never proved it may
// be here does not get to occupy one of the slots — otherwise the ceiling would
// be a way to keep the ones that did prove it waiting.
//
// A waiting request that gives up before its turn is dropped rather than run:
// by then the client is gone, and starting seventeen ssh processes for an answer
// nobody will read is the exact thing the ceiling exists to prevent.
func limitInFlight(next http.Handler, n int) http.Handler {
	slots := make(chan struct{}, n)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			next.ServeHTTP(w, r)
		case <-r.Context().Done():
			http.Error(w, "server is busy\n", http.StatusServiceUnavailable)
		}
	})
}

// listen applies focal's bind policy and returns the bound listener.
func (a *app) listen(o serveOptions) (net.Listener, *cliError) {
	if path, ok := strings.CutPrefix(o.listen, unixListenScheme); ok {
		return a.listenUnix(path)
	}
	loopback, addrErr := listensOnLoopback(o.listen)
	if addrErr != nil {
		return nil, usageError(addrErr.Code, addrErr.Message, addrErr.Field, addrErr.Allowed)
	}
	if !loopback {
		if err := a.allowBeyondLoopback(o); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("tcp", o.listen)
	if err != nil {
		return nil, rejected(result.ExecutionError("listen_failed", "cannot listen: "+err.Error()))
	}
	return ln, nil
}

// allowBeyondLoopback decides whether focal may bind an address something off
// this machine can reach, and says what is still true about it if so.
//
// There are two ways to have answered for such an address, and they answer
// different things. A token means focal itself will refuse whatever does not
// carry it, so the address being reachable is no longer the whole story — but
// the connection is still plaintext, and a shared secret sent in the clear is
// a shared secret anyone on the path now has. --allow-unauthenticated-listen
// means the operator says something in front of focal is doing the refusing,
// which focal cannot verify and so states as the assumption it is. Neither
// case is silent, because in neither case is there nothing left to say.
func (a *app) allowBeyondLoopback(o serveOptions) *cliError {
	switch {
	case upstreamToken(o) != "":
		fmt.Fprintf(a.stderr,
			"focal serve: warning: listening on %s, which is reachable from beyond this machine.\n"+
				"The upstream token is demanded of every request, but nothing here is encrypted: the\n"+
				"token and every answer cross the network in the clear. Terminate TLS in front of focal.\n",
			o.listen)
	case o.allowUnauthenticated:
		fmt.Fprintf(a.stderr,
			"focal serve: warning: listening on %s, which is reachable from beyond this machine.\n"+
				"focal has no authentication of its own; anything that can reach this address can inspect\n"+
				"every host reachable from it. Put an authenticating proxy in front of focal.\n",
			o.listen)
	default:
		return usageError(
			"unauthenticated_listen_address",
			fmt.Sprintf("%s is reachable from beyond this machine, and focal has no authentication of its own", o.listen),
			"--listen",
			[]string{
				"a loopback address such as " + defaultListenAddress,
				"--allow-unauthenticated-listen",
				"--upstream-token, or " + upstreamTokenEnv,
			},
		)
	}
	return nil
}

// listenUnix binds the Unix domain socket at path.
//
// The bind policy the TCP path applies is deliberately not consulted here.
// --allow-unauthenticated-listen answers one question — can something beyond
// this machine reach this address — and a socket in the filesystem has no
// address for anything beyond this machine to reach. What answers the question
// instead is the socket's own permission, 0600, and so everything that could
// undermine that permission is refused before the socket exists: a path whose
// directory another user could write focal's socket out from under, and a file
// already sitting at the path that focal cannot establish is nobody's.
//
// Removing the socket afterwards is net.UnixListener's own: it unlinks the
// path it created when it is closed, and http.Server.Shutdown closes it.
func (a *app) listenUnix(path string) (net.Listener, *cliError) {
	if err := checkUnixSocketPath(path); err != nil {
		return nil, usageError(err.Code, err.Message, err.Field, err.Allowed)
	}
	if err := clearStaleSocket(path); err != nil {
		return nil, err
	}
	return listenUnixSocket(path)
}

// invalidUnixSocketPath is one rejection of a --listen socket path. Every one
// of them offers the same alternative, because there is only one: a path focal
// can create a 0600 socket at and keep it 0600.
func invalidUnixSocketPath(format string, args ...any) *result.Error {
	return result.ValidationError(
		"invalid_listen_address",
		fmt.Sprintf(format, args...),
		"--listen",
		[]string{fmt.Sprintf(
			"an absolute path of at most %d bytes, in a directory no other user can write, e.g. %s",
			maxUnixSocketPathLen, exampleUnixListenAddress)},
	)
}

// checkUnixSocketPath decides whether a socket at path could be a boundary,
// which is a question about the directory holding it as much as about the path
// itself: a socket is only as private as the directory a stranger would have
// to get past to replace it. A sticky directory such as /tmp passes, because
// there only a file's owner may replace it.
func checkUnixSocketPath(path string) *result.Error {
	// A socket path is a POSIX path on every platform that has one, so it
	// is judged as one rather than by the local filesystem's rules.
	if !strings.HasPrefix(path, "/") {
		return invalidUnixSocketPath("%q is not an absolute path", path)
	}
	if len(path) > maxUnixSocketPathLen {
		return invalidUnixSocketPath(
			"a socket path may be at most %d bytes, and %q is %d",
			maxUnixSocketPathLen, path, len(path))
	}
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return invalidUnixSocketPath("cannot use the directory %q: %s", dir, err.Error())
	}
	if !info.IsDir() {
		return invalidUnixSocketPath("%q is not a directory", dir)
	}
	// Checking only the immediate parent is not enough: whoever can write to
	// any directory on the way to it can rename that directory aside and put
	// their own in its place, socket and all, and the parent focal then
	// inspects is the one they built. So every ancestor is judged by the same
	// rule as the parent.
	//
	// The chain is resolved first rather than each component being refused for
	// being a symlink, because a legitimate one is normal — /tmp is a symlink
	// to /private/tmp on macOS. Resolving is what makes the components below
	// the real ones; refusing a writable one is what keeps them from changing
	// between here and the bind.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return invalidUnixSocketPath("cannot use the directory %q: %s", dir, err.Error())
	}
	for cur := resolved; ; cur = filepath.Dir(cur) {
		info, err := os.Lstat(cur)
		if err != nil {
			return invalidUnixSocketPath("cannot use the directory %q: %s", cur, err.Error())
		}
		if mode := info.Mode(); mode&os.ModeSticky == 0 && mode.Perm()&0o022 != 0 {
			return invalidUnixSocketPath(
				"%q can be written by users other than its owner, who could replace the socket in it", cur)
		}
		// A directory owned by someone else is theirs to replace whatever
		// its mode says, so the mode alone does not settle it. root is
		// accepted because every path starts under directories root owns.
		if err := checkDirOwner(cur, info); err != nil {
			return err
		}
		if parent := filepath.Dir(cur); parent != cur {
			continue
		}
		return nil
	}
}

// clearStaleSocket makes path ready to bind.
//
// A socket file outlives the process that made it, so one already at the path
// means either a running server, which focal must not take the socket from, or
// the remains of one that died, which focal may remove. Connecting tells them
// apart: on this machine the attempt returns at once either way. Anything
// focal cannot read as one of those two answers leaves the file alone — a
// socket kept is a server that fails to start, a socket wrongly removed is a
// server someone else loses.
func clearStaleSocket(path string) *cliError {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return rejected(result.ExecutionError("listen_failed",
			fmt.Sprintf("cannot examine %q: %s", path, err.Error())))
	}
	// Lstat, not Stat: a symlink is not a socket, whatever it points at,
	// and following it would let the link's author choose what focal
	// removes.
	if info.Mode()&os.ModeSocket == 0 {
		e := invalidUnixSocketPath("%q already exists and is not a socket, so focal will not remove it", path)
		return usageError(e.Code, e.Message, e.Field, e.Allowed)
	}

	conn, dialErr := net.DialTimeout("unix", path, staleSocketDialTimeout)
	if dialErr == nil {
		_ = conn.Close()
		return rejected(result.ExecutionError("listen_address_in_use",
			fmt.Sprintf("something is already listening on %q", path)))
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
		return rejected(result.ExecutionError("listen_failed",
			fmt.Sprintf("cannot tell whether %q is still in use: %s", path, dialErr.Error())))
	}
	if err := os.Remove(path); err != nil {
		return rejected(result.ExecutionError("listen_failed",
			fmt.Sprintf("cannot remove the socket left at %q: %s", path, err.Error())))
	}
	return nil
}

// serveOn runs the HTTP server on ln until ctx ends, then shuts it down.
func (a *app) serveOn(ctx context.Context, ln net.Listener, handler *serveHandler) *cliError {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: serveReadHeaderTimeout,
		ReadTimeout:       serveReadTimeout,
		WriteTimeout:      handler.write,
		IdleTimeout:       serveIdleTimeout,
	}
	fmt.Fprintf(a.stderr, "focal serve: listening on %s\n", ln.Addr())

	idle := make(chan struct{})
	go func() {
		<-ctx.Done()
		// The shutdown gets its own deadline: the context that ended is
		// the reason to stop, so it cannot also be the time allowed for
		// stopping cleanly. WithoutCancel keeps everything else it
		// carried and drops only the cancellation.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), serveReadTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		close(idle)
	}()

	err := srv.Serve(ln)
	if !errors.Is(err, http.ErrServerClosed) {
		return rejected(result.ExecutionError("serve_failed", "the MCP server stopped: "+err.Error()))
	}
	<-idle
	return nil
}

// listensOnLoopback reports whether every address --listen could bind is a
// loopback address.
//
// The judgement is made on the parsed address rather than on how it was
// written: [::1] and ::ffff:127.0.0.1 are as much loopback as 127.0.0.1 is,
// and a string comparison would have to know all three spellings and would
// still be wrong about the fourth. An empty host is every interface, so it is
// not loopback however it was spelled.
//
// A name is loopback only when everything it resolves to is, and a name that
// cannot be resolved is not loopback: whether an address is safe to bind
// without authentication is not a question to answer optimistically.
func listensOnLoopback(addr string) (bool, *result.Error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false, result.ValidationError(
			"invalid_listen_address",
			fmt.Sprintf("cannot read %q as a listen address: %s", addr, err.Error()),
			"--listen",
			[]string{"host:port, e.g. " + defaultListenAddress},
		)
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return false, result.ValidationError(
			"invalid_listen_address",
			fmt.Sprintf("cannot read %q as a port", port),
			"--listen",
			[]string{"host:port, e.g. " + defaultListenAddress},
		)
	}
	if host == "" {
		return false, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback(), nil
	}
	ips, lookupErr := net.LookupIP(host)
	if lookupErr != nil || len(ips) == 0 {
		return false, nil
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false, nil
		}
	}
	return true, nil
}

// serveRunner is internal/mcp's Runner: everything about reaching a host that
// the MCP layer is deliberately kept from knowing.
//
// It is where the identity alias becomes a key file, where the configured
// timeout, output ceiling and port are layered under it, and where the
// operation's resolved policy is read — all the things that would otherwise
// have to be handed across the interface as an ssh.Options, which is the
// dependency that interface exists to avoid.
type serveRunner struct {
	cfg         config.Config
	identities  identities
	newExecutor executorFactory
}

var _ mcp.Runner = (*serveRunner)(nil)

func (r *serveRunner) Run(ctx context.Context, op operation.Operation, target vo.Target, identity string) (result.Envelope, *result.Error) {
	opts := ssh.Options{
		Timeout:      r.cfg.Timeout(),
		MaxOutput:    r.cfg.MaxOutput(),
		IdentityFile: r.cfg.IdentityFile(),
		Port:         r.cfg.Port(),
	}
	// A registered alias overrides the configured key; naming none leaves
	// whatever the configuration file said, which may itself be nothing.
	path, err := r.identities.resolve(identity)
	if err != nil {
		return result.Envelope{}, err
	}
	if path != "" {
		opts.IdentityFile = path
	}

	executor, err := r.newExecutor(opts)
	if err != nil {
		return result.Envelope{}, err
	}

	// The operation was built from this same configuration, so its
	// settings are known to be here.
	opCfg, _ := r.cfg.Operation(op.Name())
	env, runErr := op.Execute(ctx, executor, target, opCfg.Policy())
	if runErr != nil {
		return result.Envelope{}, asResultError(runErr)
	}
	return env, nil
}
