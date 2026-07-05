package dockerhost

import (
	"net/http"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// TestEngineClientTransportWrappedWhenEnabled asserts that with telemetry
// enabled the hand-rolled Docker Engine HTTP client's transport is wrapped with
// otelhttp (both the unix-socket and TCP paths), and that with telemetry OFF no
// wrapping happens — a strict no-op.
func TestEngineClientTransportWrappedWhenEnabled(t *testing.T) {
	t.Run("unix on", func(t *testing.T) {
		t.Setenv("CORNUS_OTEL", "1")
		t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
		c, err := newEngineClient()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := c.http.Transport.(*otelhttp.Transport); !ok {
			t.Fatalf("unix transport not otel-wrapped: %T", c.http.Transport)
		}
	})

	t.Run("tcp on", func(t *testing.T) {
		t.Setenv("CORNUS_OTEL", "1")
		t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
		c, err := newEngineClient()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := c.http.Transport.(*otelhttp.Transport); !ok {
			t.Fatalf("tcp transport not otel-wrapped: %T", c.http.Transport)
		}
	})

	t.Run("unix off", func(t *testing.T) {
		t.Setenv("CORNUS_OTEL", "")
		t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
		c, err := newEngineClient()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := c.http.Transport.(*http.Transport); !ok {
			t.Fatalf("unix transport should be a plain *http.Transport when off: %T", c.http.Transport)
		}
	})

	t.Run("tcp off", func(t *testing.T) {
		t.Setenv("CORNUS_OTEL", "")
		t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
		c, err := newEngineClient()
		if err != nil {
			t.Fatal(err)
		}
		// TCP path leaves Transport nil when off, so http.Client falls back to
		// http.DefaultTransport exactly as before.
		if c.http.Transport != nil {
			t.Fatalf("tcp transport should be nil when off, got %T", c.http.Transport)
		}
	})
}
