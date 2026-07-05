package openaiproxy_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cornus/pkg/creddelivery"
	_ "cornus/pkg/creddelivery/openaiproxy"
	"cornus/pkg/credential"
)

// TestUpstreamOverrideThroughOpen opens openai-proxy via the registry pointed at a
// MOCK upstream and proves the Bearer key is injected and forwarded there (not the
// real OpenAI API).
func TestUpstreamOverrideThroughOpen(t *testing.T) {
	var got http.Header
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		gotPath = r.URL.Path
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	ep, err := creddelivery.Open("openai-proxy", map[string]string{"upstream": up.URL})
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
		return credential.Credential{Values: map[string]string{"api_key": "sk-openai-mock"}}, nil
	})
	base := "http://" + ln.Addr().String()
	for i := 0; i < 100; i++ {
		if c, e := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); e == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	req, _ := http.NewRequest("POST", base+"/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer APP-BOGUS")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if gotPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if got.Get("Authorization") != "Bearer sk-openai-mock" {
		t.Fatalf("upstream Authorization = %q, want the injected key", got.Get("Authorization"))
	}
}
