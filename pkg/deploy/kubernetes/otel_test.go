package kubernetes

import (
	"net/http"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// TestWrapTransportOTel verifies the rest.Config transport wrapper layers
// otelhttp instrumentation on top of any existing wrapper without replacing it,
// so client-go API calls become client spans while a pre-existing WrapTransport
// still runs.
func TestWrapTransportOTel(t *testing.T) {
	base := http.DefaultTransport

	// Without an existing wrapper the result is an otelhttp transport wrapping base.
	rt := wrapTransportOTel(nil)(base)
	if rt == nil {
		t.Fatal("wrapTransportOTel(nil) produced a nil RoundTripper")
	}
	if _, ok := rt.(*otelhttp.Transport); !ok {
		t.Fatalf("expected *otelhttp.Transport, got %T", rt)
	}

	// An existing wrapper is still invoked (composition, not replacement).
	called := false
	existing := func(inner http.RoundTripper) http.RoundTripper {
		called = true
		return inner
	}
	rt = wrapTransportOTel(existing)(base)
	if !called {
		t.Error("existing WrapTransport was not invoked")
	}
	if _, ok := rt.(*otelhttp.Transport); !ok {
		t.Fatalf("expected *otelhttp.Transport with existing wrapper, got %T", rt)
	}
}
