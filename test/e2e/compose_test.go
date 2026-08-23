//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// composeFileRel is the compose file, relative to the repo root.
const composeFileRel = "test/e2e/docker-compose.yml"

// startContainer brings up the sshd fixture and returns the ephemeral loopback
// host port docker mapped to its port 22. It builds the image with the freshly
// generated public key, waits for the container's healthcheck to pass, reads
// back the mapped port, and registers the teardown that tears it all down again.
func startContainer(root, pubKey string) (int, error) {
	composeFile := filepath.Join(root, composeFileRel)
	project := fmt.Sprintf("focal-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())

	// The public key must be present for `down` too: the compose file marks
	// FOCAL_E2E_PUBKEY required with :?, which is evaluated whenever the file
	// is parsed, teardown included.
	env := append(os.Environ(), "FOCAL_E2E_PUBKEY="+pubKey)
	compose := func(args ...string) *exec.Cmd {
		full := append([]string{"compose", "-p", project, "-f", composeFile}, args...)
		cmd := exec.Command("docker", full...)
		cmd.Env = env
		return cmd
	}

	addTeardown(func() {
		out, err := compose("down", "-v", "--remove-orphans").CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e: docker compose down failed: %v: %s\n", err, out)
		}
	})

	if out, err := compose("up", "-d", "--build", "--wait").CombinedOutput(); err != nil {
		return 0, fmt.Errorf("docker compose up: %v: %s", err, out)
	}

	out, err := compose("port", "sshd", "22").Output()
	if err != nil {
		return 0, fmt.Errorf("docker compose port: %w", err)
	}
	port, err := parseHostPort(string(out))
	if err != nil {
		return 0, fmt.Errorf("read mapped port from %q: %w", strings.TrimSpace(string(out)), err)
	}
	return port, nil
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
