//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// composeFixture names one container the suite runs focal against: a compose
// file, relative to the repo root, and the service in it that exposes port 22.
// Each fixture is a compose project of its own, so one that fails to come up
// never takes another down with it.
type composeFixture struct {
	file    string // compose file, relative to the repo root
	service string // the service whose port 22 focal connects to
	prefix  string // project name prefix; pid and start time are appended

	// upTimeout, when non-zero, bounds the whole `compose up` — build, start
	// and the wait for healthy. Zero leaves the start unbounded.
	upTimeout time.Duration
}

var (
	// ubuntuFixture is the plain sshd container, without systemd.
	ubuntuFixture = composeFixture{file: "test/e2e/docker-compose.yml", service: "sshd", prefix: "focal-e2e"}

	// al2023Fixture is the Amazon Linux 2023 container with systemd as PID 1.
	// Its start is bounded: a boot that never finishes would otherwise block
	// its tests until go test's own timeout killed the binary, taking the
	// ubuntu tests and the teardown down with it. Five minutes leaves room for
	// a cold image build on CI and for the rest of the suite inside go test's
	// default ten.
	al2023Fixture = composeFixture{
		file:      "test/e2e/systemd/docker-compose.yml",
		service:   "al2023",
		prefix:    "focal-e2e-al2023",
		upTimeout: 5 * time.Minute,
	}
)

// startContainer brings up the ubuntu sshd fixture and returns the ephemeral
// loopback host port docker mapped to its port 22, registering the teardown
// that tears it all down again.
func startContainer(root, pubKey string) (int, error) {
	port, down, err := ubuntuFixture.up(root, pubKey)
	addTeardown(down)
	return port, err
}

// up builds the fixture's image with the freshly generated public key, waits
// for the container's healthcheck to pass, and reads back the mapped port. The
// returned down is non-nil on error too, so a caller can always tear down what
// a failed start left behind. A failed start carries the containers' own logs
// in its error, since a container that never became healthy is otherwise
// opaque, on CI above all.
func (f composeFixture) up(root, pubKey string) (port int, down func(), err error) {
	composeFile := filepath.Join(root, f.file)
	project := fmt.Sprintf("%s-%d-%d", f.prefix, os.Getpid(), time.Now().UnixNano())

	// The public key must be present for `down` too: the compose file marks
	// FOCAL_E2E_PUBKEY required with :?, which is evaluated whenever the file
	// is parsed, teardown included.
	env := append(os.Environ(), "FOCAL_E2E_PUBKEY="+pubKey)
	compose := func(ctx context.Context, args ...string) *exec.Cmd {
		full := append([]string{"compose", "-p", project, "-f", composeFile}, args...)
		cmd := exec.CommandContext(ctx, "docker", full...)
		cmd.Env = env
		return cmd
	}
	bg := context.Background()

	down = func() {
		out, err := compose(bg, "down", "-v", "--remove-orphans").CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e: docker compose down failed: %v: %s\n", err, out)
		}
	}

	upCtx := bg
	if f.upTimeout > 0 {
		var cancel context.CancelFunc
		upCtx, cancel = context.WithTimeout(bg, f.upTimeout)
		defer cancel()
	}
	if out, err := compose(upCtx, "up", "-d", "--build", "--wait").CombinedOutput(); err != nil {
		if upCtx.Err() != nil {
			err = fmt.Errorf("not healthy within %s: %w", f.upTimeout, err)
		}
		logs, _ := compose(bg, "logs", "--no-color", "--tail", "200").CombinedOutput()
		return 0, down, fmt.Errorf("docker compose up %s: %v: %s\ncontainer logs:\n%s", f.file, err, out, logs)
	}

	out, err := compose(bg, "port", f.service, "22").Output()
	if err != nil {
		return 0, down, fmt.Errorf("docker compose port: %w", err)
	}
	port, err = parseHostPort(string(out))
	if err != nil {
		return 0, down, fmt.Errorf("read mapped port from %q: %w", strings.TrimSpace(string(out)), err)
	}
	return port, down, nil
}

// parseHostPort reads the "host:port" line `docker compose port` prints and
// returns the port. docker may print more than one line (IPv4 and IPv6); the
// first that parses wins.
func parseHostPort(s string) (int, error) {
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		_, portStr, err := net.SplitHostPort(line)
		if err != nil {
			continue
		}
		if port, err := strconv.Atoi(portStr); err == nil {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no host:port found")
}
