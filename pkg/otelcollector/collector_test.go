//go:build otelcol

package otelcollector

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// freePort returns a currently-free loopback TCP port. There is a small window
// between close and re-bind, acceptable for a test.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestRun_StartsBindsAndShutsDown launches the curated Collector, confirms the
// OTLP gRPC receiver actually binds (which proves the whole factory + config +
// pipeline wiring is valid — NewCollector/Run fail loudly on a bad config), then
// cancels ctx and asserts a clean shutdown. Full telemetry pass-through is
// covered by the E2E scenario, not here.
func TestRun_StartsBindsAndShutsDown(t *testing.T) {
	grpcPort := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", grpcPort)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			GRPCEndpoint: addr,
			// A bogus exporter target is fine: the otlp exporter connects lazily and
			// does not block startup, so the receiver still binds.
			ExporterEndpoint: "127.0.0.1:1",
			ExporterInsecure: true,
			Signals:          []string{"traces"},
		})
	}()

	// Wait for the receiver to accept connections.
	deadline := time.Now().Add(15 * time.Second)
	bound := false
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			bound = true
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Run returned before binding: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !bound {
		cancel()
		t.Fatalf("collector did not bind %s within timeout", addr)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
