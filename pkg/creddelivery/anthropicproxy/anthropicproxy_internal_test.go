package anthropicproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cornus/pkg/creddelivery/internal/authproxy"
	"cornus/pkg/credential"
)

// runProxy stands up the real anthropic-proxy Endpoint pointed at upstream and
// returns its loopback base URL.
func runProxy(t *testing.T, upstream string, cred credential.Credential) string {
	t.Helper()
	ep := &authproxy.Endpoint{Upstream: upstream, BaseURLEnv: "ANTHROPIC_BASE_URL", Inject: inject}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ep.Serve(ctx, ln, func(context.Context) (credential.Credential, error) { return cred, nil })
	return "http://" + ln.Addr().String()
}

func TestAnthropicProxyAPIKey(t *testing.T) {
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(200)
	}))
	defer up.Close()

	base := runProxy(t, up.URL, credential.Credential{Values: map[string]string{"value": "sk-ant-api-xyz"}})

	// The app sends a bogus key; the proxy must override it.
	req, _ := http.NewRequest("POST", base+"/v1/messages", nil)
	req.Header.Set("X-Api-Key", "APP-SENT-BOGUS")
	req.Header.Set("Authorization", "Bearer bogus")
	dialUntil(t, base)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got.Get("X-Api-Key") != "sk-ant-api-xyz" {
		t.Fatalf("upstream x-api-key = %q, want the real key", got.Get("X-Api-Key"))
	}
	if got.Get("Authorization") != "" {
		t.Fatalf("client-sent Authorization leaked to upstream: %q", got.Get("Authorization"))
	}
}

func TestAnthropicProxyOAuth(t *testing.T) {
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer up.Close()

	base := runProxy(t, up.URL, credential.Credential{Values: map[string]string{"oauth_token": "sk-ant-oat-abc"}})
	dialUntil(t, base)
	resp, err := http.Get(base + "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got.Get("Authorization") != "Bearer sk-ant-oat-abc" {
		t.Fatalf("upstream Authorization = %q", got.Get("Authorization"))
	}
	if got.Get("Anthropic-Beta") != oauthBeta {
		t.Fatalf("upstream anthropic-beta = %q, want %q", got.Get("Anthropic-Beta"), oauthBeta)
	}
	if got.Get("X-Api-Key") != "" {
		t.Fatalf("x-api-key should be absent for OAuth, got %q", got.Get("X-Api-Key"))
	}
}

func TestAnthropicProxyOAuthPrefixAutodetect(t *testing.T) {
	// A bare "value" starting with sk-ant-oat is auto-detected as OAuth.
	cred := credential.Credential{Values: map[string]string{"value": "sk-ant-oat-zzz"}}
	if v, oauth := token(cred); !oauth || v != "sk-ant-oat-zzz" {
		t.Fatalf("token() = %q,%v; want oauth", v, oauth)
	}
	// An sk-ant-api value is an API key.
	if _, oauth := token(credential.Credential{Values: map[string]string{"value": "sk-ant-api-1"}}); oauth {
		t.Fatal("api key misdetected as oauth")
	}
}

func TestAddBetaMerges(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://x/", nil)
	req.Header.Set("anthropic-beta", "feature-a")
	addBeta(req, oauthBeta)
	if got := req.Header.Get("anthropic-beta"); got != "feature-a,"+oauthBeta {
		t.Fatalf("merged beta = %q", got)
	}
	addBeta(req, oauthBeta) // idempotent
	if got := req.Header.Get("anthropic-beta"); got != "feature-a,"+oauthBeta {
		t.Fatalf("addBeta not idempotent: %q", got)
	}
}

func TestAnthropicProxyEnv(t *testing.T) {
	ep := &authproxy.Endpoint{Upstream: "https://api.anthropic.com", BaseURLEnv: "ANTHROPIC_BASE_URL", Inject: inject}
	env := ep.Env("claude", "127.0.0.1:19100")
	if env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:19100" {
		t.Fatalf("env = %v", env)
	}
	if ep.WellKnownAddr() != "" {
		t.Fatal("proxy should have no well-known addr")
	}
}

// dialUntil waits for the proxy listener to accept connections.
func dialUntil(t *testing.T, base string) {
	t.Helper()
	addr := base[len("http://"):]
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("proxy never came up")
}
