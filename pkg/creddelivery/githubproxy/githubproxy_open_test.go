package githubproxy_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cornus/pkg/creddelivery"
	_ "cornus/pkg/creddelivery/githubproxy"
	"cornus/pkg/credential"
)

// runProxy opens the provider through the public registry (so registration under
// "github-proxy" is under test), serves it on loopback, and returns its base URL.
func runProxy(t *testing.T, cfg map[string]string, cred credential.Credential) string {
	t.Helper()
	ep, err := creddelivery.Open("github-proxy", cfg)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ep.Serve(ctx, ln, func(context.Context) (credential.Credential, error) { return cred, nil })
	dialUntil(t, ln.Addr().String())
	return "http://" + ln.Addr().String()
}

// TestUpstreamOverrideThroughOpen proves the proxy injects the credential and
// forwards to the configured mock rather than the real API — the hermetic path
// the E2E scenario relies on.
func TestUpstreamOverrideThroughOpen(t *testing.T) {
	var got http.Header
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		gotPath = r.URL.Path
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	base := runProxy(t, map[string]string{"upstream": up.URL},
		credential.Credential{Values: map[string]string{"token": "gho_mock"}})

	r, _ := http.NewRequest("GET", base+"/user", nil)
	r.Header.Set("Authorization", "Bearer APP-BOGUS") // must be overridden
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if gotPath != "/user" {
		t.Fatalf("upstream path = %q, want /user (proxy preserved it)", gotPath)
	}
	if got.Get("Authorization") != "Bearer gho_mock" {
		t.Fatalf("upstream Authorization = %q, want the injected token", got.Get("Authorization"))
	}
	if got.Get("User-Agent") == "" {
		t.Fatal("upstream saw no User-Agent; GitHub 403s those")
	}
}

// TestPaginationLinkIsRewritten is the decision that makes this provider usable:
// without it, octokit.paginate / PyGithub / go-github follow the upstream's
// ABSOLUTE rel="next" URL, bypass the proxy, and page two arrives with no
// credential. Page one succeeding is not evidence that pagination works.
func TestPaginationLinkIsRewritten(t *testing.T) {
	var upURL string
	var sawAuth []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = append(sawAuth, r.Header.Get("Authorization"))
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", `<`+upURL+`/user/repos?page=2>; rel="next"`)
		}
		io.WriteString(w, "[]")
	}))
	defer up.Close()
	upURL = up.URL

	base := runProxy(t, map[string]string{"upstream": up.URL},
		credential.Credential{Values: map[string]string{"token": "gho_page"}})

	resp, err := http.Get(base + "/user/repos")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	wantLink := `<` + base + `/user/repos?page=2>; rel="next"`
	if got := resp.Header.Get("Link"); got != wantLink {
		t.Fatalf("Link = %q, want it rewritten to the proxy (%q)", got, wantLink)
	}

	// Follow it the way a paginating client would; the second page must still be
	// authenticated.
	resp2, err := http.Get(base + "/user/repos?page=2")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	if len(sawAuth) != 2 {
		t.Fatalf("upstream saw %d requests, want 2", len(sawAuth))
	}
	for i, a := range sawAuth {
		if a != "Bearer gho_page" {
			t.Fatalf("page %d Authorization = %q, want the injected token", i+1, a)
		}
	}
}

// TestEnvDefaultUpstream: an upstream with no base path (api.github.com, and any
// httptest mock) can serve GraphQL at /graphql, so both URLs are advertised —
// and no GITHUB_TOKEN, deliberately.
func TestEnvDefaultUpstream(t *testing.T) {
	ep, err := creddelivery.Open("github-proxy", nil)
	if err != nil {
		t.Fatal(err)
	}
	env := ep.Env("gh", "127.0.0.1:19100")
	if env["GITHUB_API_URL"] != "http://127.0.0.1:19100" {
		t.Fatalf("GITHUB_API_URL = %q (env %v)", env["GITHUB_API_URL"], env)
	}
	if env["GITHUB_GRAPHQL_URL"] != "http://127.0.0.1:19100/graphql" {
		t.Fatalf("GITHUB_GRAPHQL_URL = %q (env %v)", env["GITHUB_GRAPHQL_URL"], env)
	}
	if _, ok := env["GITHUB_TOKEN"]; ok {
		t.Fatalf("env must not set GITHUB_TOKEN: %v", env)
	}
	if ep.WellKnownAddr() != "" {
		t.Fatal("proxy should have no well-known addr")
	}
}

// TestEnvEnterpriseUpstreamOmitsGraphQL: GHES serves REST under /api/v3 and
// GraphQL under the sibling /api/graphql, which this proxy cannot reach. Advertising
// a derived URL would send the client to GHES uncredentialed, so none is set.
func TestEnvEnterpriseUpstreamOmitsGraphQL(t *testing.T) {
	ep, err := creddelivery.Open("github-proxy", map[string]string{"upstream": "https://ghe.corp/api/v3"})
	if err != nil {
		t.Fatal(err)
	}
	env := ep.Env("gh", "127.0.0.1:19100")
	if env["GITHUB_API_URL"] != "http://127.0.0.1:19100" {
		t.Fatalf("GITHUB_API_URL = %q (env %v)", env["GITHUB_API_URL"], env)
	}
	if _, ok := env["GITHUB_GRAPHQL_URL"]; ok {
		t.Fatalf("GHES upstream must not advertise a GraphQL URL: %v", env)
	}
}

// TestEnterprisePathPrefixIsJoined covers the GHES request shape end to end: the
// app calls /repos/o/r and the upstream must see /api/v3/repos/o/r.
func TestEnterprisePathPrefixIsJoined(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer up.Close()

	base := runProxy(t, map[string]string{"upstream": up.URL + "/api/v3"},
		credential.Credential{Values: map[string]string{"token": "t"}})
	resp, err := http.Get(base + "/repos/o/r")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotPath != "/api/v3/repos/o/r" {
		t.Fatalf("upstream path = %q, want /api/v3/repos/o/r", gotPath)
	}
}

func TestBadUpstreamIsAConfigError(t *testing.T) {
	if _, err := creddelivery.Open("github-proxy", map[string]string{"upstream": "://nope"}); err == nil {
		t.Fatal("expected a config error for an unparseable upstream")
	}
}

func dialUntil(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("proxy never came up")
}
