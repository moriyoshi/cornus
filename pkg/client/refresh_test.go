package client

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// bearerOf returns the credential a request presented.
func bearerOf(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// TestCredentialRefreshOn401 is the wiring test for a credential that the server
// stops accepting BEFORE its stated expiry.
//
// The cached exchanged token honours the expiry the server reported, which is
// right until the server restarts with a new signing key, revokes the token, or
// changes its scope mapping. Then every invocation reads the same dead token from
// the cache and fails — and re-running does not help, for up to the token's full
// lifetime. The closure claimed refresh "on expiry or 401"; expiry was real
// (tokencache.Get drops a lapsed entry), the 401 half did not exist.
func TestCredentialRefreshOn401(t *testing.T) {
	const stale, fresh = "stale-token", "fresh-token"

	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, bearerOf(r))
		if bearerOf(r) != fresh {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"token rejected"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var refreshes atomic.Int32
	c := New(srv.URL,
		WithToken(stale),
		WithCredentialRefresher(func(context.Context) (string, error) {
			refreshes.Add(1)
			return fresh, nil
		}),
	)

	resp, err := c.do(context.Background(), http.MethodGet, "/.cornus/v1/info", nil)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200: the rejected credential was not replaced, so the caller sees a "+
			"failure that re-running cannot fix", resp.StatusCode)
	}
	if got := refreshes.Load(); got != 1 {
		t.Errorf("refresher ran %d times, want exactly 1", got)
	}
	if len(seen) != 2 || seen[0] != stale || seen[1] != fresh {
		t.Errorf("server saw credentials %v, want [%q %q]: exactly one replay, carrying the NEW credential",
			seen, stale, fresh)
	}
}

// TestCredentialRefreshDoesNotLoop pins the bound. A refresher that cannot
// actually produce a working credential must cost ONE extra round trip, not a
// retry storm against an auth endpoint — the failure mode that turns a bad
// credential into a self-inflicted outage.
func TestCredentialRefreshDoesNotLoop(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, WithToken("stale"),
		WithCredentialRefresher(func(context.Context) (string, error) { return "also-bad", nil }))

	resp, err := c.do(context.Background(), http.MethodGet, "/.cornus/v1/info", nil)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want the server's 401 preserved", resp.StatusCode)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("server saw %d requests, want exactly 2 (original + one replay)", got)
	}
}

// TestCredentialRefreshSkippedWhenUnchanged covers the guard that makes the retry
// provably useful rather than merely optimistic: a refresher that hands back the
// SAME credential has fixed nothing, and replaying would produce a second
// identical 401 — one wasted round trip on every auth failure, forever.
func TestCredentialRefreshSkippedWhenUnchanged(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, WithToken("same"),
		WithCredentialRefresher(func(context.Context) (string, error) { return "same", nil }))

	resp, err := c.do(context.Background(), http.MethodGet, "/.cornus/v1/info", nil)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if got := requests.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1: an unchanged credential must not be replayed", got)
	}
}

// TestCredentialRefreshLeavesTheSSHKeyHandshakeAlone is the regression test for
// the collision this feature could have caused.
//
// sshProofRoundTrip POSTs unsigned, EXPECTS a 401 carrying a CornusSSH challenge,
// and re-posts with the signature. That 401 is the protocol, not a stale
// credential. A blanket refresh-on-401 would fire on the first leg of every
// key-auth handshake — re-exchanging a credential that was never rejected, on the
// one path where an extra round trip is least welcome.
//
// The discriminator is the WWW-Authenticate header rather than a list of exempt
// paths, so a new endpoint that adopts the same handshake is covered without
// anyone remembering to add it here.
func TestCredentialRefreshLeavesTheSSHKeyHandshakeAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `CornusSSH challenge="abc123"`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"challenge":"abc123"}`))
	}))
	defer srv.Close()

	var refreshes atomic.Int32
	c := New(srv.URL, WithToken("tok"),
		WithCredentialRefresher(func(context.Context) (string, error) {
			refreshes.Add(1)
			return "unexpected", nil
		}))

	resp, err := c.do(context.Background(), http.MethodPost, "/.cornus/v1/auth/ssh/token",
		bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if got := refreshes.Load(); got != 0 {
		t.Errorf("the refresher ran %d times during an SSH-key challenge. That 401 is the first leg of the "+
			"proof handshake, not a rejected credential — refreshing there re-exchanges on every key-auth "+
			"handshake.", got)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want the 401 passed through untouched so sshProofRoundTrip can read the "+
			"challenge out of it", resp.StatusCode)
	}
}

// TestCredentialRefreshReplaysTheBody proves the replay carries the request body.
// A retry that sent an empty body would turn an auth hiccup into a malformed
// request — succeeding at the transport level and failing at the handler, which
// is a worse outcome than the original 401.
func TestCredentialRefreshReplaysTheBody(t *testing.T) {
	const payload = `{"name":"web","image":"localhost:5000/web:v1"}`
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, len(payload)+16)
		n, _ := r.Body.Read(b)
		bodies = append(bodies, string(b[:n]))
		if bearerOf(r) != "good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, WithToken("bad"),
		WithCredentialRefresher(func(context.Context) (string, error) { return "good", nil }))

	resp, err := c.do(context.Background(), http.MethodPost, "/.cornus/v1/deploy",
		bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if len(bodies) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(bodies))
	}
	if bodies[1] != payload {
		t.Errorf("the replayed request carried %q, want the original body %q", bodies[1], payload)
	}
}

// TestCredentialRefreshErrorKeepsTheServerStatus: when the refresher itself fails,
// the caller must still see the server's 401 rather than a refresher error. "The
// server rejected your credential" is the accurate diagnosis; the refresher's
// failure is a detail behind it, and swapping one for the other sends the reader
// looking in the wrong place.
func TestCredentialRefreshErrorKeepsTheServerStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, WithToken("bad"),
		WithCredentialRefresher(func(context.Context) (string, error) {
			return "", errors.New("exchange endpoint unreachable")
		}))

	resp, err := c.do(context.Background(), http.MethodGet, "/.cornus/v1/info", nil)
	if err != nil {
		t.Fatalf("do returned a transport error %v; the server's 401 must survive a failed refresh", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
