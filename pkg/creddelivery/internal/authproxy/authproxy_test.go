package authproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"cornus/pkg/credential"
)

// serve runs ep on a fresh loopback listener and returns its "host:port".
func serve(t *testing.T, ep *Endpoint) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ep.Serve(ctx, ln, func(context.Context) (credential.Credential, error) {
		return credential.Credential{Values: map[string]string{"value": "tok"}}, nil
	})
	addr := ln.Addr().String()
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("proxy never came up")
	return ""
}

// noInject is a no-op Injector for tests about response handling.
func noInject(credential.Credential, *http.Request) {}

// TestRewriteUpstreamURLsRewritesLinkAndLocation is the pagination guard: a
// paginated upstream hands back a rel="next" Link (and a redirect Location)
// naming ITSELF, and a client that follows either would leave the proxy and
// reach the upstream with no credential. With the flag on, both must come back
// pointing at the proxy.
func TestRewriteUpstreamURLsRewritesLinkAndLocation(t *testing.T) {
	var upURL string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<`+upURL+`/user/repos?page=2>; rel="next", <`+upURL+`/user/repos?page=9>; rel="last"`)
		w.Header().Set("Location", upURL+"/repos/new/name")
		w.WriteHeader(301)
	}))
	defer up.Close()
	upURL = up.URL

	addr := serve(t, &Endpoint{Upstream: up.URL, BaseURLEnv: "X_BASE", Inject: noInject, RewriteUpstreamURLs: true})
	proxyBase := "http://" + addr

	// Do NOT follow the redirect — we want to inspect the header itself.
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(proxyBase + "/user/repos")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	wantLink := `<` + proxyBase + `/user/repos?page=2>; rel="next", <` + proxyBase + `/user/repos?page=9>; rel="last"`
	if got := resp.Header.Get("Link"); got != wantLink {
		t.Fatalf("Link = %q, want %q", got, wantLink)
	}
	if got, want := resp.Header.Get("Location"), proxyBase+"/repos/new/name"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

// TestRewriteUpstreamURLsOffByDefault pins that the zero value changes nothing,
// so the anthropic/openai providers keep their exact behaviour.
func TestRewriteUpstreamURLsOffByDefault(t *testing.T) {
	var upURL string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<`+upURL+`/next>; rel="next"`)
	}))
	defer up.Close()
	upURL = up.URL

	addr := serve(t, &Endpoint{Upstream: up.URL, BaseURLEnv: "X_BASE", Inject: noInject})
	resp, err := http.Get("http://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got, want := resp.Header.Get("Link"), `<`+upURL+`/next>; rel="next"`; got != want {
		t.Fatalf("Link = %q, want it untouched (%q)", got, want)
	}
}

// TestUpstreamPrefixIncludesBasePath covers the GitHub Enterprise shape, whose
// links carry the /api/v3 base path. Trailing slashes must not double up.
func TestUpstreamPrefixIncludesBasePath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://api.github.com", "https://api.github.com"},
		{"https://api.github.com/", "https://api.github.com"},
		{"https://ghe.corp/api/v3", "https://ghe.corp/api/v3"},
		{"https://ghe.corp/api/v3/", "https://ghe.corp/api/v3"},
	} {
		u, err := url.Parse(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := upstreamPrefix(u); got != tc.want {
			t.Fatalf("upstreamPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestExtraEnvMerges proves ExtraEnv adds to Env without displacing the base URL
// or the placeholder key, and that a nil ExtraEnv is inert.
func TestExtraEnvMerges(t *testing.T) {
	base := &Endpoint{Upstream: "https://x", BaseURLEnv: "X_BASE", KeyEnv: "X_KEY", Inject: noInject}
	if env := base.Env("c", "127.0.0.1:1"); len(env) != 2 || env["X_BASE"] != "http://127.0.0.1:1" || env["X_KEY"] != PlaceholderValue {
		t.Fatalf("nil ExtraEnv changed Env: %v", env)
	}

	base.ExtraEnv = func(addr string) map[string]string { return map[string]string{"X_GQL": "http://" + addr + "/graphql"} }
	env := base.Env("c", "127.0.0.1:1")
	if env["X_BASE"] != "http://127.0.0.1:1" || env["X_KEY"] != PlaceholderValue {
		t.Fatalf("ExtraEnv displaced the base keys: %v", env)
	}
	if env["X_GQL"] != "http://127.0.0.1:1/graphql" {
		t.Fatalf("ExtraEnv not merged: %v", env)
	}
}
