package deploywire

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/hashicorp/yamux"

	"cornus/pkg/wire"

	_ "cornus/pkg/credential/static"
)

// yamuxPair returns a connected (caller-serves, requester-opens) yamux session
// pair over an in-process pipe — no WebSocket needed.
func yamuxPair(t *testing.T) (caller, requester *yamux.Session) {
	t.Helper()
	c1, c2 := net.Pipe()
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	var err error
	caller, err = yamux.Server(c1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	requester, err = yamux.Client(c2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { caller.Close(); requester.Close() })
	return caller, requester
}

func TestCredentialBackingRoundTrip(t *testing.T) {
	caller, requester := yamuxPair(t)
	sources := []CredentialBacking{{
		Name:    "db",
		Backend: "static",
		Config:  map[string]string{"username": "u", "password": "p"},
	}}
	go wire.ServeBackings(caller, nil, nil, nil, nil, credHandler(context.Background(), sources), nil)

	stream, err := wire.OpenCredBacking(requester, "db")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	cred, err := FetchCredential(stream, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Values["username"] != "u" || cred.Values["password"] != "p" {
		t.Fatalf("round-trip values = %v", cred.Values)
	}
}

func TestCredentialBackingUndeclared(t *testing.T) {
	caller, requester := yamuxPair(t)
	go wire.ServeBackings(caller, nil, nil, nil, nil, credHandler(context.Background(), []CredentialBacking{{Name: "db", Backend: "static", Config: map[string]string{"x": "y"}}}), nil)

	// A name the session never declared: the handler drops the stream, so the
	// fetch fails (no response).
	stream, err := wire.OpenCredBacking(requester, "other")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := FetchCredential(stream, nil); err == nil {
		t.Fatal("expected error fetching an undeclared credential")
	}
}

func TestCredentialBackingSourceError(t *testing.T) {
	caller, requester := yamuxPair(t)
	// A static source with no values fails to construct; the error is reported in
	// the response so the requester surfaces it.
	go wire.ServeBackings(caller, nil, nil, nil, nil, credHandler(context.Background(), []CredentialBacking{{Name: "db", Backend: "static", Config: map[string]string{}}}), nil)

	stream, err := wire.OpenCredBacking(requester, "db")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := FetchCredential(stream, nil); err == nil {
		t.Fatal("expected source-construction error")
	}
}

func TestAllowsCredential(t *testing.T) {
	s := &ServerSession{Spec: DeployAttachSpec{CredentialSources: []CredentialBacking{{Name: "db"}}}}
	if !s.AllowsCredential("db") {
		t.Fatal("db should be allowed")
	}
	if s.AllowsCredential("secret") {
		t.Fatal("undeclared name must not be allowed")
	}
}
