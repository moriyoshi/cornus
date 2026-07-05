package socks5

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// Serve runs on a listener the caller bound. This is what lets the bind happen
// under the conduit rendezvous lock, and lets a replica of the socket be handed to
// joiners so ownership of the address can move without the address going down.
func TestServeRunsOnACallerBoundListener(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	claimed := &recordingDialer{name: "claimed"}
	r.Claim(AliasSpec{Label: "web", Deployment: "demo-web", Dialer: claimed})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	p, err := Serve(context.Background(), &recordingDialer{name: "proxy"}, r, ln)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer p.Close()

	connectVia(t, ln.Addr().String(), "web.cornus.internal", 8080)
	if got, want := claimed.calls(), "demo-web:8080/tcp"; len(got) != 1 || got[0] != want {
		t.Errorf("dialer saw %v, want [%s]", got, want)
	}
}

// THE ownership contract: closing the proxy must leave the caller's listener
// usable. The socket is meant to outlive any one proxy — a successor serves it
// after a takeover — so a proxy that closed it would take the address down at
// exactly the moment it is supposed to survive.
//
// Two things have to hold, and the second is easy to miss: the socket must still
// be OPEN, and it must still be ACCEPTABLE. Shutdown wakes the accept loop with a
// past deadline, which is the only way to unblock Accept without closing, and a
// deadline persists — so leaving it set would hand back a listener that is open but
// fails every Accept instantly.
func TestClosingAServeProxyLeavesTheListenerUsable(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	p, err := Serve(context.Background(), &recordingDialer{name: "proxy"}, r, ln)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()

	// Still bound.
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("the address went down when the proxy closed: %v", err)
	}
	defer c.Close()

	// And still acceptable by its owner — WITHOUT the owner setting a deadline
	// first. Setting one here would be the exact workaround a caller needs if the
	// proxy handed the listener back with its shutdown deadline still in place, so
	// it would mask the defect this assertion exists to catch. The timeout instead
	// comes from outside, via a goroutine, which distinguishes "returned an error
	// immediately" (a stale deadline) from "blocked waiting for a connection"
	// (correct).
	type accepted struct {
		c   net.Conn
		err error
	}
	res := make(chan accepted, 1)
	go func() {
		ac, aerr := ln.Accept()
		res <- accepted{ac, aerr}
	}()
	select {
	case got := <-res:
		if got.err != nil {
			t.Fatalf("the owner can no longer Accept after the proxy closed: %v", got.err)
		}
		_ = got.c.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("Accept never returned, although a connection was waiting")
	}
}

// A proxy that bound its own listener still closes it, so nothing leaks in the
// ordinary standalone case.
func TestClosingAStartProxyClosesItsOwnListener(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), &recordingDialer{name: "proxy"}, r, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := p.Addr()
	p.Close()

	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = c.Close()
		t.Errorf("%s still accepts after the proxy that bound it closed", addr)
	}
}

// The non-loopback refusal applies however the socket came to exist — an
// unauthenticated proxy that dials arbitrary destinations is an open proxy
// off-host either way. But Serve must not close a listener it did not open, even
// while refusing it.
func TestServeRefusesNonLoopbackWithoutClosingTheListener(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skipf("cannot bind a wildcard address here: %v", err)
	}
	defer ln.Close()

	_, err = Serve(context.Background(), &recordingDialer{name: "proxy"}, r, ln)
	if err == nil {
		t.Fatal("Serve accepted a non-loopback listener without the opt-in")
	}
	if !strings.Contains(err.Error(), "refusing to serve") {
		t.Errorf("error %q does not say what was refused", err)
	}
	// The caller's listener is untouched: Serve did not open it, so it must not have
	// closed it.
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second); err != nil {
		t.Errorf("Serve closed a listener it was only lent: %v", err)
	} else {
		_ = c.Close()
	}

	// With the opt-in it serves.
	p, err := Serve(context.Background(), &recordingDialer{name: "proxy"}, r, ln, WithAllowNonLoopback(true))
	if err != nil {
		t.Fatalf("Serve with the opt-in: %v", err)
	}
	p.Close()
}
