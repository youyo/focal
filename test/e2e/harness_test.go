//go:build e2e

// Package e2e drives focal end to end against a real SSH server running in a
// container: the CLI path (the focal binary invoked as a subprocess) and the
// Remote MCP path (focal serve reached with the MCP go-sdk client). It is
// guarded by the e2e build tag, so `go test ./...` never compiles it and the
// ordinary build, test and lint jobs stay free of docker.
//
// Run it with `mise run e2e`, which is `go test -tags=e2e ./test/e2e/...`.
//
// The whole suite shares one harness built in TestMain: a fresh ed25519 key, a
// built focal binary, a focal config file naming the container's port and that
// key, and the running sshd itself. When docker or docker compose is not
// available the harness is not built and every test skips, so a developer
// without docker sees skips rather than failures.
//
// focal reaches a host through its own ssh(1), which it invokes with neither -F
// nor -o: it accepts only an identity file, a port and a user, never an
// ssh_config file or option. The suite therefore hands focal the port and key
// through a focal config file, and for the one thing focal cannot express — a
// throwaway known_hosts so the first connection to the container neither
// prompts nor writes the developer's real ~/.ssh — it puts a tiny ssh wrapper
// first on PATH. The wrapper adds -o UserKnownHostsFile and
// StrictHostKeyChecking=accept-new and execs the real ssh, which is exactly
// what an operator's own ssh_config would do and keeps focal itself untouched.
package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// targetHost is the destination every inspection names: the e2e user on the
// container, reached over loopback. The port is not here — it lives in the
// focal config file, because that is the only place focal takes one for an
// address written without ssh_config.
const targetHost = "e2e@127.0.0.1"

// upstreamToken is the shared secret the MCP tests start focal serve with. It
// is 33 bytes of printable ASCII, comfortably over focal's 32-byte floor.
const upstreamToken = "e2e-secret-token-0123456789abcdef"

// harness is the shared fixture: everything a test needs to invoke focal
// against the container. It is nil when setup was skipped (no docker); tests
// reach it through requireHarness, which skips in that case.
type harness struct {
	focalBin   string   // path to the built focal binary
	configFile string   // focal config naming the container's port and key
	env        []string // environment every focal invocation runs with (PATH has the ssh wrapper first)
}

var (
	shared      *harness
	skipReason  string
	teardownFns []func()
)

func TestMain(m *testing.M) {
	code := func() int {
		if reason, ok := unavailable(); ok {
			skipReason = reason
			return m.Run()
		}
		h, err := setup()
		if err != nil {
			runTeardown()
			fmt.Fprintf(os.Stderr, "e2e setup failed: %v\n", err)
			return 1
		}
		shared = h
		defer runTeardown()
		return m.Run()
	}()
	os.Exit(code)
}

// requireHarness returns the shared harness, or skips the calling test when
// setup was not run because docker was unavailable.
func requireHarness(t *testing.T) *harness {
	t.Helper()
	if shared == nil {
		t.Skipf("e2e harness not available: %s", skipReason)
	}
	return shared
}

// unavailable reports whether the environment lacks something the harness needs
// but a developer may simply not have, so the suite skips instead of failing.
func unavailable() (string, bool) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "docker not found on PATH", true
	}
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		return "docker compose not available", true
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		return "ssh-keygen not found on PATH", true
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		return "docker daemon not reachable", true
	}
	return "", false
}

func addTeardown(fn func()) { teardownFns = append(teardownFns, fn) }

func runTeardown() {
	// Reverse order: undo the most recent setup step first.
	for i := len(teardownFns) - 1; i >= 0; i-- {
		teardownFns[i]()
	}
	teardownFns = nil
}

// setup builds the whole fixture. Every step registers its own teardown before
// the next runs, so a failure half way leaves nothing behind.
func setup() (*harness, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}

	work, err := os.MkdirTemp("", "focal-e2e-*")
	if err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	addTeardown(func() { _ = os.RemoveAll(work) })

	keyPath, pubKey, err := generateKey(work)
	if err != nil {
		return nil, err
	}

	focalBin, err := buildFocal(root, work)
	if err != nil {
		return nil, err
	}

	port, err := startContainer(root, pubKey)
	if err != nil {
		return nil, err
	}

	configFile, err := writeFocalConfig(work, keyPath, port)
	if err != nil {
		return nil, err
	}

	binDir, err := writeSSHWrapper(work)
	if err != nil {
		return nil, err
	}

	h := &harness{
		focalBin:   focalBin,
		configFile: configFile,
		env:        focalEnv(work, binDir),
	}

	if err := waitReady(h); err != nil {
		return nil, err
	}
	return h, nil
}

// repoRoot is the module root, two levels up from test/e2e. It is resolved from
// the test's own working directory, which `go test` sets to the package dir.
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(filepath.Join(wd, "..", ".."))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("go.mod not found at %s: %w", root, err)
	}
	return root, nil
}

// generateKey makes a fresh ed25519 pair with ssh-keygen and returns the
// private key path and the public key line for authorized_keys. Using
// ssh-keygen keeps the suite free of a new crypto dependency and yields keys in
// exactly the format OpenSSH expects on both ends.
func generateKey(work string) (keyPath, pubKey string, err error) {
	keyPath = filepath.Join(work, "id_ed25519")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", keyPath, "-C", "focal-e2e")
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return "", "", fmt.Errorf("ssh-keygen: %v: %s", runErr, out)
	}
	pub, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return "", "", fmt.Errorf("read public key: %w", err)
	}
	return keyPath, strings.TrimSpace(string(pub)), nil
}

// buildFocal compiles the focal binary under test from the working tree, so the
// suite exercises the code in this checkout rather than whatever is installed.
func buildFocal(root, work string) (string, error) {
	bin := filepath.Join(work, "focal")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/focal")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build focal: %v: %s", err, out)
	}
	return bin, nil
}

// writeFocalConfig writes the focal config that carries the container's port
// and the generated key. Operations are left unset so focal runs on its
// safe-side defaults — every inspection enabled except kernel — which is what
// the tool-list and kernel assertions expect.
func writeFocalConfig(work, keyPath string, port int) (string, error) {
	path := filepath.Join(work, "focal.yaml")
	body := fmt.Sprintf("execution:\n  port: %d\n  identity_file: %s\n", port, keyPath)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// focalEnv is the environment every focal invocation runs with. binDir, holding
// the ssh wrapper, goes first on PATH so focal's exec.LookPath("ssh") finds it;
// XDG_CONFIG_HOME points at an empty dir so a `focal serve` started without
// --config still runs on defaults rather than the developer's own config; and
// any inherited upstream token is dropped so the MCP tests control it.
func focalEnv(work, binDir string) []string {
	strip := map[string]bool{
		"PATH":                 true,
		"XDG_CONFIG_HOME":      true,
		"FOCAL_UPSTREAM_TOKEN": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strip[k] {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"XDG_CONFIG_HOME="+filepath.Join(work, "xdg-empty"),
	)
}

// writeSSHWrapper writes an ssh(1) shim into a fresh bin directory and returns
// that directory. focal invokes "ssh" from PATH with no way to set a
// known_hosts file or relax host-key checking; the shim supplies both with -o
// and execs the real ssh, so the container's host key is trusted on first use
// and recorded in a throwaway file rather than the developer's ~/.ssh.
func writeSSHWrapper(work string) (string, error) {
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		return "", fmt.Errorf("locate ssh: %w", err)
	}
	binDir := filepath.Join(work, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	knownHosts := filepath.Join(work, "known_hosts")
	script := fmt.Sprintf(`#!/bin/sh
exec %q \
  -o UserKnownHostsFile=%q \
  -o GlobalKnownHostsFile=/dev/null \
  -o StrictHostKeyChecking=accept-new \
  "$@"
`, realSSH, knownHosts)
	wrapper := filepath.Join(binDir, "ssh")
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		return "", err
	}
	return binDir, nil
}

// waitReady blocks until focal can complete a real inspection through SSH, so a
// test never runs before the container answers. It exercises the whole path —
// ssh reachability, the key, the host key — rather than just the open port.
func waitReady(h *harness) error {
	deadline := time.Now().Add(60 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		stdout, stderr, code := h.cli(context.Background(), "system")
		if code == 0 && strings.Contains(stdout, `"status":"ok"`) {
			return nil
		}
		last = strings.TrimSpace(stdout + " " + stderr)
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("focal never reached the container within the deadline: %s", last)
}

// cli runs one inspection against the container: focal with the harness config,
// the target host, the operation and its own arguments.
func (h *harness) cli(ctx context.Context, op string, opArgs ...string) (stdout, stderr string, exitCode int) {
	args := append([]string{"--config", h.configFile, targetHost, op}, opArgs...)
	return h.run(ctx, nil, args...)
}

// run invokes the focal binary with args and returns its stdout, stderr and
// exit code. extraEnv is appended after the base environment, so a caller can
// add FOCAL_UPSTREAM_TOKEN for a serve invocation without disturbing the rest.
func (h *harness) run(ctx context.Context, extraEnv []string, args ...string) (stdout, stderr string, exitCode int) {
	cmd := exec.CommandContext(ctx, h.focalBin, args...)
	cmd.Env = append(append([]string{}, h.env...), extraEnv...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	return outBuf.String(), errBuf.String(), code
}
