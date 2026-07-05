package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegistryPushAuthz proves per-identity /v2 push authorization: registry WRITES
// require the "push" action; reads and non-registry paths are not gated here, and an
// unconfigured policy is a pure pass-through.
func TestRegistryPushAuthz(t *testing.T) {
	s := &Server{apiPolicy: newAPIPolicy(map[string][]string{"ci-bot": {"push"}})}
	h := s.registryAuthz(okHandler())

	do := func(method, path, identity string) int {
		req := httptest.NewRequest(method, path, nil)
		if identity != "" {
			req = req.WithContext(withSubject(req.Context(), identity))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// Push (write) — allowed only for a permitted identity.
	for _, m := range []string{http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		if code := do(m, "/v2/app/manifests/latest", "ci-bot"); code != http.StatusOK {
			t.Fatalf("ci-bot %s /v2: code = %d, want 200", m, code)
		}
		if code := do(m, "/v2/app/manifests/latest", "stranger"); code != http.StatusForbidden {
			t.Fatalf("stranger %s /v2: code = %d, want 403", m, code)
		}
		// Anonymous (no identity) is denied a push under a configured policy.
		if code := do(m, "/v2/app/blobs/uploads/", ""); code != http.StatusForbidden {
			t.Fatalf("anonymous %s /v2: code = %d, want 403", m, code)
		}
	}

	// Reads (pull) are NOT gated by push authz — allowed regardless of identity.
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if code := do(m, "/v2/app/manifests/latest", "stranger"); code != http.StatusOK {
			t.Fatalf("stranger %s /v2 (read): code = %d, want 200 (pull not push-gated)", m, code)
		}
	}

	// Non-registry paths pass through (deploy/build authz is enforced in handlers).
	if code := do(http.MethodPost, "/.cornus/v1/deploy", "stranger"); code != http.StatusOK {
		t.Fatalf("stranger POST /.cornus/v1/deploy through registryAuthz: code = %d, want 200 (not gated here)", code)
	}

	// Unconfigured policy: pure pass-through (registryAuthz returns next unchanged).
	open := (&Server{apiPolicy: nil}).registryAuthz(okHandler())
	req := httptest.NewRequest(http.MethodPut, "/v2/app/manifests/latest", nil)
	rec := httptest.NewRecorder()
	open.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unconfigured policy: code = %d, want 200 (pass-through)", rec.Code)
	}
}

// registryAuthzDo drives one request through s's registryAuthz middleware with
// the given identity already on the context (as the auth middleware would
// leave it) and returns the status code.
func registryAuthzDo(t *testing.T, s *Server, method, path, identity string) int {
	t.Helper()
	h := s.registryAuthz(okHandler())
	req := httptest.NewRequest(method, path, nil)
	if identity != "" {
		req = req.WithContext(withSubject(req.Context(), identity))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestRegistryPullAuthzOptIn proves the opt-in per-identity pull authorization:
// once ANY rule mentions the "pull" action, registry GET/HEAD reads require the
// caller to be allowed "pull" — except the /v2/ ping — while writes stay on the
// "push" action.
func TestRegistryPullAuthzOptIn(t *testing.T) {
	s := &Server{apiPolicy: newAPIPolicy(map[string][]string{
		"puller": {"pull"},
		"ci-bot": {"push"},
		"admin":  {"*"},
	})}

	for _, m := range []string{http.MethodGet, http.MethodHead} {
		// Allowed: the explicit "pull" grant and the "*" wildcard.
		for _, id := range []string{"puller", "admin"} {
			if code := registryAuthzDo(t, s, m, "/v2/app/manifests/latest", id); code != http.StatusOK {
				t.Fatalf("%s %s /v2 read: code = %d, want 200", id, m, code)
			}
		}
		// Denied: an identity without "pull" (even one holding "push"), an
		// unknown identity, and the empty identity (fail closed — this is what
		// makes the explicit pull policy win over anonymous pull).
		for _, id := range []string{"ci-bot", "stranger", ""} {
			if code := registryAuthzDo(t, s, m, "/v2/app/blobs/sha256:abc", id); code != http.StatusForbidden {
				t.Fatalf("%q %s /v2 read: code = %d, want 403", id, m, code)
			}
		}
		// The /v2/ ping is exempt (clients probe it before authenticating).
		for _, ping := range []string{"/v2", "/v2/"} {
			if code := registryAuthzDo(t, s, m, ping, ""); code != http.StatusOK {
				t.Fatalf("anonymous %s %s (ping): code = %d, want 200 (exempt)", m, ping, code)
			}
		}
	}

	// Writes are still governed by "push": puller cannot push, ci-bot can.
	if code := registryAuthzDo(t, s, http.MethodPut, "/v2/app/manifests/latest", "puller"); code != http.StatusForbidden {
		t.Fatalf("puller PUT /v2: code = %d, want 403", code)
	}
	if code := registryAuthzDo(t, s, http.MethodPut, "/v2/app/manifests/latest", "ci-bot"); code != http.StatusOK {
		t.Fatalf("ci-bot PUT /v2: code = %d, want 200", code)
	}

	// Non-registry reads pass through untouched.
	if code := registryAuthzDo(t, s, http.MethodGet, "/.cornus/v1/deploy", ""); code != http.StatusOK {
		t.Fatalf("GET /.cornus/v1/deploy through registryAuthz: code = %d, want 200", code)
	}
}

// TestRegistryPullAuthzAbsentKeepsOldBehavior proves backward compatibility:
// when no rule anywhere mentions "pull" — including a pure-wildcard policy —
// registry reads stay authn-governed exactly as before the action existed, so
// existing policies cannot suddenly lock out pulls.
func TestRegistryPullAuthzAbsentKeepsOldBehavior(t *testing.T) {
	for name, rules := range map[string]map[string][]string{
		"push-only": {"ci-bot": {"push"}},
		"wildcard":  {"admin": {"*"}},
	} {
		s := &Server{apiPolicy: newAPIPolicy(rules)}
		for _, m := range []string{http.MethodGet, http.MethodHead} {
			for _, id := range []string{"", "stranger"} {
				if code := registryAuthzDo(t, s, m, "/v2/app/manifests/latest", id); code != http.StatusOK {
					t.Fatalf("policy %s: %q %s /v2 read: code = %d, want 200 (pull not enforced)", name, id, m, code)
				}
			}
		}
	}
}

// TestRegistryPullPolicyWinsOverAnonymousPull drives the full middleware stack
// with BOTH CORNUS_REGISTRY_ANONYMOUS_PULL and a pull-mentioning policy set:
// anonymous pull only skips authentication, so the anonymous caller reaches the
// pull authz with no identity and is denied — the explicit pull policy wins —
// while an identity granted "pull" gets through.
func TestRegistryPullPolicyWinsOverAnonymousPull(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	clearAuthEnv(t)
	t.Setenv("CORNUS_JWT_HS256_SECRET", secret)
	t.Setenv("CORNUS_REGISTRY_ANONYMOUS_PULL", "1")
	t.Setenv("CORNUS_API_POLICY", `{"puller":["pull"]}`)

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	// Anonymous read: past authn (anonymous pull), stopped by pull authz.
	resp, err := http.Get(srv.URL + "/v2/app/manifests/latest")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous /v2 read: code = %d, want 403 (pull policy wins)", resp.StatusCode)
	}

	// The ping stays reachable anonymously (docker probes it before login).
	resp, err = http.Get(srv.URL + "/v2/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("anonymous /v2/ ping: code = %d, want it exempt from pull authz", resp.StatusCode)
	}

	// An identity granted "pull" passes the gate (a 404 from the registry for
	// the unknown manifest proves it got past authn and authz).
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v2/app/manifests/latest", nil)
	req.Header.Set("Authorization", "Bearer "+jwtFor(t, []byte(secret), "puller"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("puller /v2 read: code = %d, want past authn+authz", resp.StatusCode)
	}
}

// TestAPIPolicyMentionsAction pins the opt-in detector: only an explicit
// listing counts — the "*" wildcard does not — and a nil policy mentions
// nothing.
func TestAPIPolicyMentionsAction(t *testing.T) {
	var nilPol *apiPolicy
	if nilPol.MentionsAction("pull") {
		t.Fatal("nil policy must mention nothing")
	}
	pol := newAPIPolicy(map[string][]string{
		"admin":  {"*"},
		"puller": {"pull"},
	})
	if !pol.MentionsAction("pull") {
		t.Fatal("explicit pull rule must be mentioned")
	}
	if pol.MentionsAction("exec") {
		t.Fatal("wildcard-only coverage must NOT count as mentioning an action")
	}
}
