package server

import (
	"net/http"
	"testing"
)

// TestBuildAttachAuthz proves GET /.cornus/v1/build/attach is gated on the "build"
// API-policy action just like POST /.cornus/v1/build: a disallowed identity is denied
// with 403 before any upgrade, while an allowed identity gets past the gate (it
// then fails only because a plain GET is not a WebSocket upgrade, i.e. it is NOT
// a 403). Without the gate the attach path silently bypassed the build policy.
func TestBuildAttachAuthz(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	clearAuthEnv(t)
	t.Setenv("CORNUS_JWT_HS256_SECRET", string(secret))
	t.Setenv("CORNUS_API_POLICY", `{"ci-bot":["build"]}`)

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	get := func(sub string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/.cornus/v1/build/attach", nil)
		req.Header.Set("Authorization", "Bearer "+jwtFor(t, secret, sub))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := get("stranger"); code != http.StatusForbidden {
		t.Fatalf("stranger GET /.cornus/v1/build/attach: code=%d, want 403", code)
	}
	if code := get("ci-bot"); code == http.StatusForbidden {
		t.Fatalf("ci-bot GET /.cornus/v1/build/attach: got 403, want past the build authz gate")
	}
}
