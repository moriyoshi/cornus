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
