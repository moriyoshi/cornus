package openaiproxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cornus/pkg/creddelivery/internal/authproxy"
	"cornus/pkg/credential"
)

func TestOpenAIProxyBearer(t *testing.T) {
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer up.Close()

	ep := &authproxy.Endpoint{Upstream: up.URL, BaseURLEnv: "OPENAI_BASE_URL", Inject: inject}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ep.Serve(ctx, ln, func(context.Context) (credential.Credential, error) {
		return credential.Credential{Values: map[string]string{"api_key": "sk-openai-xyz"}}, nil
	})
	base := "http://" + ln.Addr().String()

	req, _ := http.NewRequest("POST", base+"/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer APP-BOGUS")
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got.Get("Authorization") != "Bearer sk-openai-xyz" {
		t.Fatalf("upstream Authorization = %q, want the real key (override the client's)", got.Get("Authorization"))
	}
}

func TestOpenAIProxyEnv(t *testing.T) {
	ep := &authproxy.Endpoint{Upstream: "https://api.openai.com", BaseURLEnv: "OPENAI_BASE_URL", Inject: inject}
	if ep.Env("oai", "127.0.0.1:19101")["OPENAI_BASE_URL"] != "http://127.0.0.1:19101" {
		t.Fatal("bad env")
	}
}
