package client

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"cornus/pkg/api"
)

// TestWithDialerRoutesREST proves the custom dialer is used for the REST transport:
// the client base names an unresolvable host, so the request can only succeed if
// WithDialer's function (which ignores addr and dials the test server) is applied.
func TestWithDialerRoutesREST(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.cornus/v1/info" {
			_ = json.NewEncoder(w).Encode(api.ServerInfo{RegistryHost: "reg.example:5000"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	realAddr := srv.Listener.Addr().String()
	var dialed int
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialed++
		var d net.Dialer
		return d.DialContext(ctx, network, realAddr)
	}

	c := New("http://ignored.invalid:5000", WithDialer(dial))
	if c.clientTransport().DialContext == nil {
		t.Fatal("clientTransport().DialContext is nil; WS surfaces would bypass the tunnel")
	}
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info through custom dialer: %v", err)
	}
	if info.RegistryHost != "reg.example:5000" {
		t.Fatalf("info.RegistryHost = %q", info.RegistryHost)
	}
	if dialed == 0 {
		t.Fatal("custom dialer was not used for the REST transport")
	}
}
