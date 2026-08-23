package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testToken is a shared secret of the shape focal accepts: printable ASCII,
// comfortably longer than the minimum, and nothing like a password anyone
// would type.
const testToken = "3f9c1d7ae05b48c2a6f0e83d17b94c2ea1d6f8b3" //nolint:gosec // a fixture, and focal grants nothing anywhere

// mustUpstreamAuth builds the authenticator for token, failing the test if
// focal would have refused it at startup.
func mustUpstreamAuth(t *testing.T, token string) *upstreamAuth {
	t.Helper()
	auth, err := newUpstreamAuth(token)
	if err != nil {
		t.Fatalf("newUpstreamAuth: %v", err)
	}
	if auth == nil {
		t.Fatal("newUpstreamAuth built no authenticator for a token that was set")
	}
	return auth
}

// TestUpstreamTokenPrefersTheFlagOverTheEnvironment fixes the same layering
// the connection options use: what the command line said wins, and the
// environment fills in for a command line that said nothing.
func TestUpstreamTokenPrefersTheFlagOverTheEnvironment(t *testing.T) {
	tests := []struct {
		name string
		flag string
		env  string
		want string
	}{
		{"flag only", testToken, "", testToken},
		{"environment only", "", testToken, testToken},
		{"flag wins", testToken, "env-" + testToken, testToken},
		{"neither", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(upstreamTokenEnv, tt.env)

			if got := upstreamToken(serveOptions{upstreamToken: tt.flag}); got != tt.want {
				t.Errorf("upstreamToken = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUpstreamTokenIsRefusedBeforeAnythingIsBound collects what focal will not
// accept as a shared secret: one short enough to guess, and one it could never
// put in an Authorization header in the first place. Both are refused at
// startup, and neither refusal repeats the token back.
func TestUpstreamTokenIsRefusedBeforeAnythingIsBound(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"one byte short", strings.Repeat("a", minUpstreamTokenLen-1)},
		{"a space", strings.Repeat("a", minUpstreamTokenLen-1) + " "},
		{"a newline", strings.Repeat("a", minUpstreamTokenLen-1) + "\n"},
		{"a control byte", strings.Repeat("a", minUpstreamTokenLen-1) + "\x7f"},
		{"not ASCII", strings.Repeat("a", minUpstreamTokenLen-1) + "é"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth, err := newUpstreamAuth(tt.token)
			if err == nil {
				t.Fatalf("%q was accepted as a token (auth = %v)", tt.name, auth)
			}
			if err.Code != "invalid_upstream_token" {
				t.Errorf("code = %q, want invalid_upstream_token", err.Code)
			}
			if strings.Contains(err.Error(), tt.token) {
				t.Errorf("the refusal repeated the token back: %s", err.Error())
			}
		})
	}
}

// TestNoUpstreamTokenLeavesFocalUnauthenticated keeps the feature opt-in: an
// invocation that named no token gets no authenticator, and so no middleware.
func TestNoUpstreamTokenLeavesFocalUnauthenticated(t *testing.T) {
	auth, err := newUpstreamAuth("")
	if err != nil {
		t.Fatalf("newUpstreamAuth: %v", err)
	}
	if auth != nil {
		t.Error("focal built an authenticator without a token")
	}
}

// TestBearerAuthorization is the whole of what focal asks of a request when a
// token is set: one Authorization header, the Bearer scheme however it was
// capitalised, and the token itself.
func TestBearerAuthorization(t *testing.T) {
	auth := mustUpstreamAuth(t, testToken)

	tests := []struct {
		name   string
		values []string
		want   int
	}{
		{"no header", nil, http.StatusUnauthorized},
		{"another token", []string{"Bearer " + strings.Repeat("z", len(testToken))}, http.StatusUnauthorized},
		{"a prefix of the token", []string{"Bearer " + testToken[:len(testToken)-1]}, http.StatusUnauthorized},
		{"another scheme", []string{"Basic " + testToken}, http.StatusUnauthorized},
		{"the token alone", []string{testToken}, http.StatusUnauthorized},
		{"the scheme alone", []string{"Bearer"}, http.StatusUnauthorized},
		{"the scheme and nothing", []string{"Bearer "}, http.StatusUnauthorized},
		{"two headers", []string{"Bearer " + testToken, "Bearer " + testToken}, http.StatusUnauthorized},
		{"two headers, one right", []string{"Basic nonsense", "Bearer " + testToken}, http.StatusUnauthorized},
		{"the token", []string{"Bearer " + testToken}, http.StatusOK},
		{"a lowercase scheme", []string{"bearer " + testToken}, http.StatusOK},
		{"an uppercase scheme", []string{"BEARER " + testToken}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			handler := auth.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, "/", nil)
			for _, v := range tt.values {
				req.Header.Add("Authorization", v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if want := tt.want == http.StatusOK; reached != want {
				t.Errorf("reached the handler = %v, want %v", reached, want)
			}
		})
	}
}

// TestUnauthorizedSaysOnlyThatItIsUnauthorized pins the refusal itself: it
// names the scheme a client should use and nothing else — no discovery
// parameter that would send a client looking for an OAuth server focal does
// not have, and no trace of the request or the secret.
func TestUnauthorizedSaysOnlyThatItIsUnauthorized(t *testing.T) {
	auth := mustUpstreamAuth(t, testToken)
	handler := auth.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an unauthorized request reached the handler")
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"method":"tools/call"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want %q", got, "Bearer")
	}
	if body := rec.Body.String(); body != unauthorizedBody {
		t.Errorf("body = %q, want %q", body, unauthorizedBody)
	}
	for _, leak := range []string{testToken, "wrong", "tools/call", "internal/cli"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("the refusal leaked %q: %s", leak, rec.Body.String())
		}
	}
}

// TestUnauthorizedNeverReachesSSH is the property the middleware exists for,
// asserted through the real serve handler: a request refused at the HTTP door
// builds no operation and runs no ssh(1).
func TestUnauthorizedNeverReachesSSH(t *testing.T) {
	withConfig(t, "")
	t.Setenv(upstreamTokenEnv, "")
	s := newServeApp(t)

	handler, err := s.app.serveHandler(serveOptions{listen: "127.0.0.1:0", upstreamToken: testToken})
	if err != nil {
		t.Fatalf("serveHandler: %v", err.err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, toolCallRequest(t, ""))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if calls := s.recorder.Calls(); len(calls) != 0 {
		t.Errorf("an unauthorized request still reached the executor: %+v", calls)
	}
}

// TestServeHandlerAcceptsTheToken is the other half: the same request with the
// token set does reach focal's operations.
func TestServeHandlerAcceptsTheToken(t *testing.T) {
	withConfig(t, "")
	t.Setenv(upstreamTokenEnv, testToken)
	s := newServeApp(t)
	url := serveHost(t, s, serveOptions{listen: "127.0.0.1:0"})

	client := &http.Client{Transport: bearing(testToken)}
	if content, isError := callToolWith(t, client, url, "inspect_cpu", map[string]any{"host": "web"}); isError {
		t.Fatalf("call failed: %s", content)
	}
	if len(s.opts) != 1 {
		t.Fatalf("built %d executors, want 1", len(s.opts))
	}
}

// TestServeHandlerWithoutATokenNeedsNoHeader keeps the default as it was: with
// no token configured, focal answers a request that carries no Authorization
// header at all.
func TestServeHandlerWithoutATokenNeedsNoHeader(t *testing.T) {
	withConfig(t, "")
	t.Setenv(upstreamTokenEnv, "")
	s := newServeApp(t)

	handler, err := s.app.serveHandler(serveOptions{listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("serveHandler: %v", err.err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, toolCallRequest(t, ""))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestAUnixSocketStillDemandsTheToken pins that the two boundaries compose:
// the socket's permission decides who may connect, the token decides who may
// be answered, and setting one does not switch the other off. The handler is
// exercised directly because what is being asserted is the middleware, not the
// transport underneath it.
func TestAUnixSocketStillDemandsTheToken(t *testing.T) {
	withConfig(t, "")
	t.Setenv(upstreamTokenEnv, testToken)
	s := newServeApp(t)

	handler, err := s.app.serveHandler(serveOptions{listen: unixListenScheme + "/run/focal/focal.sock"})
	if err != nil {
		t.Fatalf("serveHandler: %v", err.err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, toolCallRequest(t, ""))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if calls := s.recorder.Calls(); len(calls) != 0 {
		t.Errorf("an unauthorized request still reached the executor: %+v", calls)
	}
}

// TestServeHandlerRefusesAWeakTokenAtStartup keeps the validation on the path
// an invocation actually takes: `focal serve` with a token too short to be one
// never gets as far as building a handler.
func TestServeHandlerRefusesAWeakTokenAtStartup(t *testing.T) {
	withConfig(t, "")
	t.Setenv(upstreamTokenEnv, "")
	s := newServeApp(t)

	short := strings.Repeat("a", minUpstreamTokenLen-1)
	_, err := s.app.serveHandler(serveOptions{listen: "127.0.0.1:0", upstreamToken: short})
	if err == nil {
		t.Fatal("a token shorter than the minimum was accepted")
	}
	if err.code != exitUsage {
		t.Errorf("exit code = %d, want %d", err.code, exitUsage)
	}
	if err.err.Code != "invalid_upstream_token" {
		t.Errorf("code = %q, want invalid_upstream_token", err.err.Code)
	}
	assertNoToken(t, s.stderr.String()+err.err.Error(), short)
}

// TestTheTokenFlagAsksToBeAnEnvironmentVariableInstead is the one thing focal
// can do about the flag's real weakness: an argument is visible to every
// process on the machine, so focal accepts it and says so — without printing
// the secret it is warning about.
func TestTheTokenFlagAsksToBeAnEnvironmentVariableInstead(t *testing.T) {
	withConfig(t, "")
	t.Setenv(upstreamTokenEnv, "")
	s := newServeApp(t)

	if _, err := s.app.serveHandler(serveOptions{listen: "127.0.0.1:0", upstreamToken: testToken}); err != nil {
		t.Fatalf("serveHandler: %v", err.err)
	}

	got := s.stderr.String()
	if !strings.Contains(got, "warning") {
		t.Errorf("stderr = %q, want a warning", got)
	}
	if !strings.Contains(got, upstreamTokenEnv) {
		t.Errorf("stderr = %q, want it to name %s", got, upstreamTokenEnv)
	}
	assertNoToken(t, got, testToken)
}

// TestTheEnvironmentVariableNeedsNoWarning is the converse: a token that
// arrived out of the environment is already where the warning would send it,
// so there is nothing to say.
func TestTheEnvironmentVariableNeedsNoWarning(t *testing.T) {
	withConfig(t, "")
	t.Setenv(upstreamTokenEnv, testToken)
	s := newServeApp(t)

	if _, err := s.app.serveHandler(serveOptions{listen: "127.0.0.1:0"}); err != nil {
		t.Fatalf("serveHandler: %v", err.err)
	}

	if got := s.stderr.String(); got != "" {
		t.Errorf("stderr = %q, want nothing for a token out of the environment", got)
	}
}

// assertNoToken is the constraint every message in this file is under: focal
// may say a token is wrong, missing or ill-formed, but never what it was.
func assertNoToken(t *testing.T, got, token string) {
	t.Helper()
	if strings.Contains(got, token) {
		t.Errorf("the token appears in output focal wrote: %q", got)
	}
}

// bearing is a transport that puts token in the Authorization header of every
// request, which is what the proxy in front of focal is expected to do.
func bearing(token string) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultTransport.RoundTrip(req)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// toolCallRequest builds one MCP tools/call request, with the Authorization
// header set to auth when it is not empty.
func toolCallRequest(t *testing.T, auth string) *http.Request {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
		`"io.modelcontextprotocol/clientInfo":{"name":"focal-test","version":"0"},` +
		`"io.modelcontextprotocol/clientCapabilities":{}},` +
		`"name":"inspect_cpu","arguments":{"host":"web"}}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "inspect_cpu")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return req
}
