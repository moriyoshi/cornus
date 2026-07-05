package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"
)

// TestHTTPBridgeDoesNotLeakGoroutines pins the fix for a leak that unit tests
// could not otherwise catch: the bridge ran http.Server.Serve over a listener
// nothing closed, so Serve blocked in Accept forever and every inbound tunnel
// connection left a goroutine (and an http.Server) behind for the life of the
// process. It grew with traffic on every ingress tunnel.
func TestHTTPBridgeDoesNotLeakGoroutines(t *testing.T) {
	bridge := httpBridge(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))

	// Settle before measuring: earlier tests may still be winding down.
	time.Sleep(200 * time.Millisecond)
	before := runtime.NumGoroutine()

	const conns = 20
	for i := 0; i < conns; i++ {
		visitor, edge := net.Pipe()
		done := make(chan struct{})
		go func() { bridge(context.Background(), edge); close(done) }()

		fmt.Fprint(visitor, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
		if _, err := io.ReadAll(visitor); err != nil {
			t.Fatalf("reading the tunneled response: %v", err)
		}
		visitor.Close()

		// The bridge must RETURN once its connection is done — that is the whole
		// property under test.
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("bridge did not return after its connection closed")
		}
	}

	// Goroutines exit asynchronously; give them a moment before counting.
	deadline := time.Now().Add(5 * time.Second)
	var after int
	for {
		after = runtime.NumGoroutine()
		if after-before <= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if after-before > 2 {
		t.Errorf("%d goroutines survived %d completed tunnel connections (before=%d after=%d)",
			after-before, conns, before, after)
	}
}

// TestHTTPBridgeServesKeepAliveRequests proves the leak fix did not cost
// keep-alive: several requests must ride one tunnel connection, since that is
// what makes a browser session over a tunnel usable.
func TestHTTPBridgeServesKeepAliveRequests(t *testing.T) {
	var served int
	bridge := httpBridge(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		fmt.Fprintf(w, "req%d", served)
	}))

	visitor, edge := net.Pipe()
	done := make(chan struct{})
	go func() { bridge(context.Background(), edge); close(done) }()
	defer func() {
		visitor.Close()
		<-done
	}()

	br := bufio.NewReader(visitor)
	for i := 1; i <= 3; i++ {
		fmt.Fprint(visitor, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("reading response %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if want := fmt.Sprintf("req%d", i); string(body) != want {
			t.Fatalf("response %d = %q, want %q (keep-alive should reuse the connection)", i, body, want)
		}
	}
}

// TestHTTPBridgeReleasesOnContextCancel proves tearing a tunnel down releases its
// connections promptly. http.Server watches no context, so without an explicit
// close an idle keep-alive connection held its bridge goroutine until IdleTimeout
// (two minutes) after the tunnel it belonged to was already gone.
func TestHTTPBridgeReleasesOnContextCancel(t *testing.T) {
	bridge := httpBridge(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))

	ctx, cancel := context.WithCancel(context.Background())
	visitor, edge := net.Pipe()
	defer visitor.Close()
	done := make(chan struct{})
	go func() { bridge(ctx, edge); close(done) }()

	// One keep-alive request, then leave the connection idle and open.
	fmt.Fprint(visitor, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	if _, err := http.ReadResponse(bufio.NewReader(visitor), nil); err != nil {
		t.Fatalf("reading the tunneled response: %v", err)
	}

	select {
	case <-done:
		t.Fatal("the bridge returned while its connection was still live")
	case <-time.After(100 * time.Millisecond):
	}

	cancel() // tunnel torn down

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the bridge outlived its session context; teardown must close the connection")
	}
}
