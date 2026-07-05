//go:build unix

package listenerpass

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Two references to one listening socket that BOTH accept do not coordinate: the
// kernel hands each incoming connection to whichever is in accept() first, and
// neither can see the other.
//
// This is not a defect in replication — it is what sharing a socket means — but it
// is the constraint every design built on replication has to respect. If two
// processes each believe they own an address, connections are split between them
// arbitrarily, and each answers from its own state. There is no error, no log
// line, and nothing to notice: roughly half of a user's requests are simply
// resolved against the wrong routing table.
//
// The invariant that follows is that at most ONE holder may accept at a time.
// This test exists to make the reason for that invariant a measured fact rather
// than an assumption.
func TestTwoAcceptersSplitConnectionsSilently(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	a, b := socketpair(t)
	done := make(chan error, 1)
	var replica net.Listener
	go func() {
		var rerr error
		replica, rerr = Receive(b)
		done <- rerr
	}()
	if err := Send(a, ln, Peer{Pid: os.Getpid()}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Receive: %v", err)
	}
	defer replica.Close()

	// Both holders accept, each identifying itself in its reply.
	var serving sync.WaitGroup
	stop := make(chan struct{})
	serve := func(l net.Listener, name string) {
		defer serving.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if tl, ok := l.(*net.TCPListener); ok {
				_ = tl.SetDeadline(time.Now().Add(50 * time.Millisecond))
			}
			c, err := l.Accept()
			if err != nil {
				continue
			}
			_, _ = c.Write([]byte(name))
			_ = c.Close()
		}
	}
	serving.Add(2)
	go serve(ln, "original")
	go serve(replica, "replica")

	counts := map[string]int{}
	const dials = 60
	for range dials {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			continue
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		body, _ := io.ReadAll(c)
		_ = c.Close()
		counts[strings.TrimSpace(string(body))]++
	}
	close(stop)
	serving.Wait()

	t.Logf("of %d connections to one address: %v", dials, counts)
	if counts["original"] == 0 || counts["replica"] == 0 {
		// Not a failure of the code under test — the split is scheduler-dependent —
		// but if it does not reproduce, the warning below has not been demonstrated
		// on this machine and should not be cited as if it had.
		t.Skipf("the split did not reproduce here (%v); the hazard is real but was not observed", counts)
	}
	if total := counts["original"] + counts["replica"]; total != dials {
		t.Errorf("accounted for %d of %d connections", total, dials)
	}
	fmt.Fprintf(os.Stderr, "NOTE: one address, two accepters, %d connections silently split as %v\n", dials, counts)
}
