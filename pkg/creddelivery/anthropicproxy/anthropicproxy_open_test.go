package anthropicproxy_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cornus/pkg/creddelivery"
	_ "cornus/pkg/creddelivery/anthropicproxy"
	"cornus/pkg/credential"
)

// TestUpstreamOverrideThroughOpen opens the provider via the public registry with
// a cfg["upstream"] pointing at a MOCK upstream, and proves the proxy injects the
// OAuth credential and forwards to that mock (not the real Anthropic API) — the
// hermetic path the E2E scenario relies on.
func TestUpstreamOverrideThroughOpen(t *testing.T) {
	var got http.Header
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		gotPath = r.URL.Path
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	ep, err := creddelivery.Open("anthropic-proxy", map[string]string{"upstream": up.URL})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ep.Serve(ctx, ln, func(context.Context) (credential.Credential, error) {
		return credential.Credential{Values: map[string]string{"oauth_token": "sk-ant-oat-mock"}}, nil
	})
	base := "http://" + ln.Addr().String()
	dialUntil(t, ln.Addr().String())

	req, _ := http.NewRequest("POST", base+"/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer APP-BOGUS") // must be overridden
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if gotPath != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages (proxy preserved it)", gotPath)
	}
	if got.Get("Authorization") != "Bearer sk-ant-oat-mock" {
		t.Fatalf("upstream Authorization = %q, want the injected OAuth token", got.Get("Authorization"))
	}
	if got.Get("Anthropic-Beta") != "oauth-2025-04-20" {
		t.Fatalf("upstream anthropic-beta = %q", got.Get("Anthropic-Beta"))
	}
}

// TestDefaultUpstreamWhenNoConfig confirms a nil/empty cfg keeps the real API as
// the target (the override is opt-in).
func TestDefaultUpstreamWhenNoConfig(t *testing.T) {
	if _, err := creddelivery.Open("anthropic-proxy", nil); err != nil {
		t.Fatalf("open with nil cfg: %v", err)
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
