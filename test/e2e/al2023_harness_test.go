//go:build e2e

package e2e

import (
	"fmt"
	"sync"
	"testing"
)

// al2023Start is the AL2023 fixture while and after it comes up. TestMain's
// setup starts it in the background, so its image build and boot overlap the
// ubuntu fixture's, and fills in configure once the shared pieces (focal
// binary, key, ssh wrapper) exist. The first test that needs it waits for the
// start to finish and builds a harness from it exactly once.
type al2023Start struct {
	done chan struct{} // closed when the compose start has returned
	port int           // the mapped SSH port; valid after done when err is nil
	err  error         // why the start failed; valid after done
	down func()        // tears the fixture down; valid after done

	// configure turns the mapped port into a harness sharing the ubuntu
	// harness's binary, key and environment. Set by setup before any test
	// runs.
	configure func(port int) (*harness, error)

	once sync.Once
	h    *harness
	herr error
}

// al2023Pending is the started fixture, or nil when setup did not get as far
// as starting it.
var al2023Pending *al2023Start

// startAL2023 begins bringing up the AL2023 fixture in the background and
// returns at once.
func startAL2023(root, pubKey string) *al2023Start {
	s := &al2023Start{done: make(chan struct{})}
	go func() {
		defer close(s.done)
		s.port, s.down, s.err = al2023Fixture.up(root, pubKey)
	}()
	return s
}

// teardown waits for the start to finish, whatever its outcome, and then
// removes whatever it created.
func (s *al2023Start) teardown() {
	<-s.done
	if s.down != nil {
		s.down()
	}
}

// requireAL2023 returns a harness pointed at the AL2023 container. It skips
// only for the reason the whole suite skips — docker is not available here.
// When docker is available and the container did not come up, or focal cannot
// reach it, the calling test fails: this fixture is never optional on a host
// that can run the suite, CI included.
func requireAL2023(t *testing.T) *harness {
	t.Helper()
	requireHarness(t)
	s := al2023Pending
	if s == nil {
		t.Fatal("AL2023 fixture was never started")
	}
	s.once.Do(func() {
		<-s.done
		if s.err != nil {
			s.herr = fmt.Errorf("AL2023 container did not start: %w", s.err)
			return
		}
		h, err := s.configure(s.port)
		if err != nil {
			s.herr = err
			return
		}
		if err := waitReady(h); err != nil {
			s.herr = fmt.Errorf("AL2023 container: %w", err)
			return
		}
		s.h = h
	})
	if s.herr != nil {
		t.Fatal(s.herr)
	}
	return s.h
}
