package ngrok

import (
	"context"
	"os"
	"testing"
	"time"

	"cornus/pkg/tunnel"
)

// TestNgrokRegistered proves the backend registers itself under "ngrok" on
// import (the blank import in cmd/cornus relies on this).
func TestNgrokRegistered(t *testing.T) {
	p, err := tunnel.Open("ngrok")
	if err != nil {
		t.Fatalf("tunnel.Open(ngrok): %v", err)
	}
	if p == nil {
		t.Fatal("tunnel.Open(ngrok) returned nil provider")
	}
}

// TestNgrokMissingToken proves Start fails fast without a credential, without
// touching the network.
func TestNgrokMissingToken(t *testing.T) {
	if _, err := (provider{}).Start(context.Background(), tunnel.Credential{}, tunnel.Options{}); err == nil {
		t.Fatal("Start with empty authtoken returned nil error, want an error")
	}
}

// TestNgrokLive is an opt-in end-to-end check against the real ngrok service. It
// is skipped unless NGROK_AUTHTOKEN is set, so the default `go test ./...` stays
// offline and daemon-free (mirroring the privileged build integration test).
func TestNgrokLive(t *testing.T) {
	token := os.Getenv("NGROK_AUTHTOKEN")
	if token == "" {
		t.Skip("set NGROK_AUTHTOKEN to run the live ngrok tunnel test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sess, err := (provider{}).Start(ctx, tunnel.Credential{AuthToken: token}, tunnel.Options{Metadata: "cornus test"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Close()
	if sess.URL() == "" {
		t.Fatal("live tunnel returned an empty URL")
	}
	t.Logf("live tunnel URL: %s", sess.URL())
}
