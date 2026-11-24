package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/youyo/focal/internal/config"
)

// stdioShutdownGrace is how long ServeStdio waits, once its input has ended or
// its context has, for the calls still running to finish before it returns
// without them.
//
// Every call is already told to stop at that moment — its context is
// cancelled, which kills the ssh(1) it started within that executor's own
// WaitDelay and has SSM cancel its command — so this is a ceiling for the
// unexpected, not a wait anything is meant to use. It is below five seconds
// because that is how long a go-sdk client waits after closing a server's
// stdin before it sends SIGTERM: a focal that is still there by then is
// reported to the client as a server that failed to stop.
var stdioShutdownGrace = 3 * time.Second

// ServeStdio serves focal's operations over MCP on in and out — a process's
// stdin and stdout, for focal mcp — until in ends, ctx ends, or the session
// fails.
//
// The tools are the ones NewHandler offers, registered by the same function
// over the same Runner. What stdio adds is the ceiling HTTP puts on requests,
// moved to where stdio needs it: at most opts.MaxInFlight tool calls run at
// once, and the rest wait.
//
// Nothing but JSON-RPC is ever written to out: it is the SDK that writes there,
// and this package has no other use for it.
//
// It returns nil for the two ways a stdio session is meant to end — the client
// closing in after its last complete message, and ctx ending — whether or not a
// call was running at the time, and an error when the session failed: input
// the SDK could not read (a line that is not JSON-RPC, a last message cut off
// by the end of input), or in failing to be read. Either way it returns within
// stdioShutdownGrace of the end, abandoning whatever has not stopped by then.
func ServeStdio(ctx context.Context, cfg config.Config, runner Runner, opts Options, in io.Reader, out io.Writer) error {
	if opts.MaxInFlight <= 0 {
		return fmt.Errorf("mcp: MaxInFlight must be positive, not %d", opts.MaxInFlight)
	}

	input := &watchedInput{r: in, ended: make(chan struct{})}

	// runCtx is what the running calls are stopped with: it ends with ctx,
	// and also the moment the input ends. The SDK cancels the calls in
	// flight when its read loop stops (internal/jsonrpc2/conn.go:552-558 in
	// go-sdk v1.7.0); ending runCtx as well means stopping them does not
	// rest on that alone.
	//
	// Run itself is given ctx, not runCtx. Run returns ctx.Err() whenever
	// its context ends before the session does (mcp/server.go:1297-1302),
	// so a runCtx ended by the input would race the session's own result
	// and could replace the very error sessionEnd has to read.
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	go func() {
		select {
		case <-input.ended:
			stopRun()
		case <-runCtx.Done():
		}
	}()

	srv := newServer(cfg, runner, opts)
	// Only here, not in newServer: over HTTP the same ceiling is already
	// applied to requests, and a second one there would be a second limit
	// nobody chose.
	srv.AddReceivingMiddleware(stopWith(runCtx), limitToolCalls(opts.MaxInFlight))

	// The caller's streams are not the SDK's to close: they are the
	// process's own, and a test's. That the reader's Close does nothing does
	// not keep Run from returning while a read of stdin is still blocked:
	// when the session closes, the SDK closes its own connection
	// (mcp/transport.go:724-729), and the pending Read returns on that
	// rather than on stdin (mcp/transport.go:606-607). Only the goroutine
	// blocked in the read of stdin is left behind, and the process exits.
	transport := &sdk.IOTransport{Reader: io.NopCloser(input), Writer: nopWriteCloser{out}}

	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, transport) }()

	// The SDK returns from Run only once every running call has returned,
	// so the end of the session has to be noticed here, where the input is
	// read, for the wait after it to have a limit.
	select {
	case err := <-done:
		return sessionEnd(ctx, err)
	case <-ctx.Done():
	case <-input.ended:
	}
	timer := time.NewTimer(stdioShutdownGrace)
	defer timer.Stop()
	select {
	case err := <-done:
		return sessionEnd(ctx, err)
	case <-timer.C:
		return abandonedEnd(ctx, input)
	}
}

// methodCallTool is the one method the concurrency ceiling counts.
const methodCallTool = "tools/call"

// stopWith ties every request to ctx as well as to its own context.
//
// The SDK cancels a request when the client cancels it or the input ends, but
// not when the context Run was given ends: it closes the session and then waits
// for the requests still running. A signal ends ctx, and the calls it should
// stop are exactly those, so each one is cancelled here when ctx ends.
func stopWith(ctx context.Context) sdk.Middleware {
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(reqCtx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			reqCtx, cancel := context.WithCancel(reqCtx)
			defer cancel()
			stop := context.AfterFunc(ctx, cancel)
			defer stop()
			return next(reqCtx, method, req)
		}
	}
}

// limitToolCalls lets at most n tool calls run at once and holds the rest.
//
// It is focal serve's ceiling on requests (see internal/cli) restated for
// stdio, where there is one connection and so no request to hold at the door:
// the unit that costs ssh(1) processes is the tool call, and only tool calls
// are counted. Listing the tools is not work on any host, and an agent has to
// be able to do it while its calls run.
//
// A call that is cancelled before it starts — while it waits for a slot, or
// before it even arrived at one — is answered with an error and never run, as
// focal serve answers a request that gave up waiting with 503: by then nobody
// is waiting for the answer, and starting the ssh(1) processes for it is
// exactly what the ceiling is there to prevent.
func limitToolCalls(n int) sdk.Middleware {
	slots := make(chan struct{}, n)
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method != methodCallTool {
				return next(ctx, method, req)
			}
			// Checked first: when both cases below are ready, select
			// picks either, and a free slot would start a call nobody
			// is waiting for.
			if ctx.Err() != nil {
				return nil, errors.New("the call was cancelled before it started")
			}
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				return next(ctx, method, req)
			case <-ctx.Done():
				return nil, fmt.Errorf(
					"the call was cancelled while it waited to start: %d calls were already running, the most focal runs at once", n)
			}
		}
	}
}

// errServerClosing matches the error the SDK's connection reports when a
// call's response could not be written because the input had already ended
// (go-sdk v1.7.0 internal/jsonrpc2/wire.go:34, code -32004; WireError.Is
// compares codes, wire.go:88-94). Run returns it when stdin closed while a call
// was running (internal/jsonrpc2/conn.go:151-162, 478-497, 749-787).
var errServerClosing error = &jsonrpc.Error{Code: -32004}

// sessionEnd decides what a session that Run ended with err amounts to, from
// err alone.
//
// Run reports the end of input as nil when nothing was running, and as
// errServerClosing when something was. Any other error from the read side —
// a line that is not JSON, a message cut off by the end of input
// (io.ErrUnexpectedEOF) — is returned as it is, and is a failure. So is
// context.Canceled, unless it is ctx that ended: a signal is a clean end, an
// error that merely arrives after one is not.
func sessionEnd(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errServerClosing):
		return nil
	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		return nil
	default:
		return err
	}
}

// abandonedEnd is what a session that Run had not finished within
// stdioShutdownGrace amounts to. There is no error from Run to read, so it is
// decided by what ended it: ctx, or the input reaching its end, is a clean
// end; a failure to read the input is not.
func abandonedEnd(ctx context.Context, input *watchedInput) error {
	if ctx.Err() != nil {
		return nil
	}
	if err := input.readErr(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// watchedInput is the session's input, watched for the moment reading it stops
// working: the end of input, or a failure to read.
type watchedInput struct {
	r     io.Reader
	once  sync.Once
	ended chan struct{}
	// err is the error that ended the input. It is written once, before
	// ended is closed, and read only after.
	err error
}

func (w *watchedInput) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if err != nil {
		w.once.Do(func() {
			w.err = err
			close(w.ended)
		})
	}
	return n, err
}

// readErr is the error that ended the input, or nil while it has not ended.
func (w *watchedInput) readErr() error {
	select {
	case <-w.ended:
		return w.err
	default:
		return nil
	}
}

// nopWriteCloser is an io.WriteCloser whose Close leaves the writer open.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
