package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.starlark.net/starlark"
)

// callHTTPGet drives bHTTPGet the way the Starlark runtime would, returning the
// result dict's "status" (or -1 when the builtin errored).
func callHTTPGet(t *testing.T, h *Harness, kwargs []starlark.Tuple) (int, error) {
	t.Helper()
	v, err := h.bHTTPGet(nil, nil, starlark.Tuple{}, kwargs)
	if err != nil {
		return -1, err
	}
	dict := v.(*starlark.Dict)
	sv, _, _ := dict.Get(starlark.String("status"))
	n, _ := sv.(starlark.Int).Int64()
	return int(n), nil
}

func kw(pairs ...any) []starlark.Tuple {
	var out []starlark.Tuple
	for i := 0; i < len(pairs); i += 2 {
		var val starlark.Value
		switch v := pairs[i+1].(type) {
		case string:
			val = starlark.String(v)
		case bool:
			val = starlark.Bool(v)
		case int:
			val = starlark.MakeInt(v)
		}
		out = append(out, starlark.Tuple{starlark.String(pairs[i].(string)), val})
	}
	return out
}

// TestHTTPGetRetry5xx asserts that retry_5xx retries a transient upstream 5xx
// (as the ingress-emulation proxy returns while its backend starts) and returns
// the eventual 200, while the default behavior returns the first 5xx verbatim.
func TestHTTPGetRetry5xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// First two requests 502 (backend warming up), then 200.
		if atomic.AddInt32(&hits, 1) <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := &Harness{ctx: context.Background()}

	// retry_5xx=True rides out the transient 502s and sees the 200.
	status, err := callHTTPGet(t, h, kw("url", srv.URL, "retry", "10s", "retry_5xx", true))
	if err != nil {
		t.Fatalf("retry_5xx http_get: %v", err)
	}
	if status != 200 {
		t.Fatalf("retry_5xx status = %d, want 200", status)
	}

	// Default: the very first response (a 502) is returned verbatim, so a real 5xx
	// is never retried away.
	atomic.StoreInt32(&hits, 0)
	status, err = callHTTPGet(t, h, kw("url", srv.URL, "retry", "10s"))
	if err != nil {
		t.Fatalf("default http_get: %v", err)
	}
	if status != http.StatusBadGateway {
		t.Fatalf("default status = %d, want 502 (5xx returned verbatim)", status)
	}
}

// TestHTTPGetRetryUntil asserts retry_until absorbs a transient NON-5xx status,
// which is the exact shape of a real ingress controller answering 404 from its
// default backend for the window before it has ingested a freshly created
// Ingress. retry_5xx cannot cover that (a 404 is not a 5xx), so without
// retry_until the request fails on controller sync latency; the CI kube-ingress
// leg failed this way on ingress-settings-controller.star, 43ms after the
// deployments reported running.
func TestHTTPGetRetryUntil(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The first two requests arrive before the route exists: default backend, 404.
		if atomic.AddInt32(&hits, 1) <= 2 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := &Harness{ctx: context.Background()}

	status, err := callHTTPGet(t, h, kw("url", srv.URL, "retry", "10s", "retry_until", 200))
	if err != nil {
		t.Fatalf("retry_until http_get: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("retry_until status = %d, want 200", status)
	}

	// retry_5xx alone must NOT absorb it — that is the gap retry_until exists to
	// close, and a regression here would silently restore the flake.
	atomic.StoreInt32(&hits, 0)
	status, err = callHTTPGet(t, h, kw("url", srv.URL, "retry", "10s", "retry_5xx", true))
	if err != nil {
		t.Fatalf("retry_5xx http_get: %v", err)
	}
	if status != http.StatusNotFound {
		t.Fatalf("retry_5xx status = %d, want 404 (a non-5xx is returned verbatim)", status)
	}
}

// TestHTTPGetRetryUntilGivesUp asserts retry_until stays bounded and honest: a
// route that never appears returns the LAST status once the deadline passes, so
// the scenario's assertion fails with what the server really said rather than
// hanging or reporting a synthesized value.
func TestHTTPGetRetryUntilGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	h := &Harness{ctx: context.Background()}
	status, err := callHTTPGet(t, h, kw("url", srv.URL, "retry", "500ms", "retry_until", 200))
	if err != nil {
		t.Fatalf("http_get: %v", err)
	}
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (last status after the retry window)", status)
	}
}

// TestHTTPGetRetry5xxGivesUp asserts the retry stays bounded: a backend that never
// recovers returns the last 5xx once the deadline passes, so the assertion in a
// scenario still fails honestly rather than hanging.
func TestHTTPGetRetry5xxGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	h := &Harness{ctx: context.Background()}
	status, err := callHTTPGet(t, h, kw("url", srv.URL, "retry", "500ms", "retry_5xx", true))
	if err != nil {
		t.Fatalf("http_get: %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (last 5xx after the retry window)", status)
	}
}
