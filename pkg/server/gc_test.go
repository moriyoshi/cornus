package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestGCEndpointReturnsSummary drives POST /.cornus/v1/gc against a real storage backend
// over a temp dir (via newTestServer) and asserts it returns the JSON reclamation
// summary. On a fresh registry nothing is reachable-vs-orphaned, so the counts are
// simply well-formed (0, 0); the point is the endpoint runs the CAS GC + localcache
// prune and shapes the response.
func TestGCEndpointReturnsSummary(t *testing.T) {
	clearAuthEnv(t)

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/.cornus/v1/gc", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /.cornus/v1/gc: code = %d, want 200", resp.StatusCode)
	}
	var out gcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode gc response: %v", err)
	}
	if out.BlobsFreed != 0 || out.LocalCacheFreed != 0 {
		t.Fatalf("fresh registry gc = %+v, want zero counts", out)
	}
	if out.LocalCacheError != "" {
		t.Fatalf("localcache prune error on empty dir: %q", out.LocalCacheError)
	}
}

// TestGCEndpointMethodNotAllowed proves only POST is accepted.
func TestGCEndpointMethodNotAllowed(t *testing.T) {
	clearAuthEnv(t)

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.cornus/v1/gc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /.cornus/v1/gc: code = %d, want 405", resp.StatusCode)
	}
}

// TestGCEndpointAuthz proves the "gc" action gates the endpoint under a configured
// CORNUS_API_POLICY: an allowed identity succeeds, a disallowed one is 403.
func TestGCEndpointAuthz(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	clearAuthEnv(t)
	t.Setenv("CORNUS_JWT_HS256_SECRET", string(secret))
	t.Setenv("CORNUS_API_POLICY", `{"admin":["gc"]}`)

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	admin := jwtFor(t, secret, "admin")
	stranger := jwtFor(t, secret, "stranger")

	post := func(t *testing.T, token string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/.cornus/v1/gc", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := post(t, admin); code != http.StatusOK {
		t.Fatalf("admin POST /.cornus/v1/gc: code = %d, want 200", code)
	}
	if code := post(t, stranger); code != http.StatusForbidden {
		t.Fatalf("stranger POST /.cornus/v1/gc: code = %d, want 403", code)
	}
}
