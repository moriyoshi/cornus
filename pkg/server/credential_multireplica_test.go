package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"cornus/pkg/config"
	"cornus/pkg/credential"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/hub"
	"cornus/pkg/storage"
	"cornus/pkg/wire"
)

// TestCredentialRelayCrossReplica is the key artifact for cross-replica credential
// forwarding: two Server replicas share one Redis. The deploy-attach session (the
// caller, which holds the credential source) attaches to replica A; the pod's
// caretaker opens its credential stream on replica B. B does not hold the session,
// so it resolves the session's routing record to A's forward URL and forwards the
// stream to A's /.cornus/v1/cred/forward, which bridges to the caller's source —
// proving caretaker -> B -> A -> caller end to end.
func TestCredentialRelayCrossReplica(t *testing.T) {
	mr := miniredis.RunT(t)

	fb := &fakeAttachingBackend{creds: make(chan []deploy.AttachCredential, 1)}
	tsA, _ := newMountReplicaServer(t, mr, "replicaA", fb)
	tsB, _ := newMountReplicaServer(t, mr, "replicaB", &fakeBackend{})

	wsA := "ws" + strings.TrimPrefix(tsA.URL, "http")
	wsB := "ws" + strings.TrimPrefix(tsB.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsA)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	go func() {
		_ = deploywire.Serve(ctx, wsA+"/.cornus/v1/deploy/attach", credAttachSpec(), nil,
			func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var creds []deploy.AttachCredential
	select {
	case creds = <-fb.creds:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithAttachments")
	}
	if len(creds) != 1 || creds[0].Name != "db" {
		t.Fatalf("attach credentials = %+v", creds)
	}
	session := creds[0].Session

	// Play the pod's caretaker AGAINST REPLICA B (the wrong replica): the credential
	// stream must be forwarded to A and still reach the caller's source.
	mux, err := wire.Dial(ctx, wsB+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach on B: %v", err)
	}
	defer mux.Close()

	cred := fetchOverMux(t, mux, session, "db")
	if cred.Values["username"] != "u" || cred.Values["password"] != "p" {
		t.Fatalf("credential via wrong replica = %v", cred.Values)
	}

	// The forwarding replica must not become a way around the owner's allow-list:
	// a name the session never declared is still rejected, at A, over the same hop.
	if _, err := fetchCredOverMux(mux, session, "secret"); err == nil {
		t.Fatal("expected the forwarded relay to drop an undeclared credential name")
	}
}

// TestCredentialRelayLocalFastPath proves a credential stream for a session held by
// the SAME replica is bridged without a single store Lookup: the local registry is
// checked first, so single-replica relay behavior involves no store round-trips.
// (The wrapper is not the in-memory *hub.Registry, so the server treats the store
// as distributed — the strictest case for the assertion.)
func TestCredentialRelayLocalFastPath(t *testing.T) {
	dataDir := t.TempDir()
	st, err := storage.Open(context.Background(), dataDir, dataDir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dataDir}, st)
	if err != nil {
		t.Fatal(err)
	}
	fb := &fakeAttachingBackend{creds: make(chan []deploy.AttachCredential, 1)}
	s.newBackend = func() (deploy.Backend, error) { return fb, nil }
	counting := &countingHubStore{Store: hub.NewRegistry()}
	s.hub = counting
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	wsBase := "ws" + strings.TrimPrefix(ts.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", credAttachSpec(), nil,
			func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var creds []deploy.AttachCredential
	select {
	case creds = <-fb.creds:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithAttachments")
	}
	session := creds[0].Session

	mux, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach: %v", err)
	}
	defer mux.Close()
	if cred := fetchOverMux(t, mux, session, "db"); cred.Values["username"] != "u" {
		t.Fatalf("local credential = %v", cred.Values)
	}

	if n := counting.lookups.Load(); n != 0 {
		t.Errorf("local-session credential relay consulted the store %d times, want 0", n)
	}
}

// TestCredentialForwardOwnerAuthorizes pins requirement that authorization lives at
// the OWNER replica: /.cornus/v1/cred/forward serves a name the session declared and
// closes the stream for one it did not — even though the request arrives on the
// inter-replica hop, which carries no session state of its own. It also covers a
// session this replica does not hold (fail closed, never re-forward).
func TestCredentialForwardOwnerAuthorizes(t *testing.T) {
	fb := &fakeAttachingBackend{creds: make(chan []deploy.AttachCredential, 1)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", credAttachSpec(), nil,
			func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var creds []deploy.AttachCredential
	select {
	case creds = <-fb.creds:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithAttachments")
	}
	session := creds[0].Session
	fwdURL := wsBase + "/.cornus/v1/cred/forward"

	// Declared name: the owner bridges to the caller's source.
	cred, err := fetchCredOverForward(ctx, fwdURL, session, "db")
	if err != nil {
		t.Fatalf("forward fetch of a declared credential: %v", err)
	}
	if cred.Values["password"] != "p" {
		t.Fatalf("forwarded credential = %v", cred.Values)
	}

	// Undeclared name on the RIGHT session: rejected by AllowsCredential at the owner.
	if _, err := fetchCredOverForward(ctx, fwdURL, session, "secret"); err == nil {
		t.Fatal("expected /.cornus/v1/cred/forward to reject an undeclared credential name")
	}

	// Unknown session: fail closed, and never re-forward (loop guard).
	if _, err := fetchCredOverForward(ctx, fwdURL, "nope", "db"); err == nil {
		t.Fatal("expected /.cornus/v1/cred/forward to reject an unknown session")
	}
}

// fetchCredOverForward plays a peer replica: dial /.cornus/v1/cred/forward, write the
// session and name lines, then run the credential request/response exchange.
func fetchCredOverForward(ctx context.Context, url, session, name string) (cred credential.Credential, err error) {
	conn, err := wire.DialConn(ctx, url)
	if err != nil {
		return credential.Credential{}, err
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, session+"\n"+name+"\n"); err != nil {
		return credential.Credential{}, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	return deploywire.FetchCredential(conn, nil)
}

// forwardOnlyStore is a hub.Store that resolves every name to a remote target at a
// fixed forward address. It is not the in-memory *hub.Registry, so the server treats
// it as distributed and takes the cross-replica forwarding path.
type forwardOnlyStore struct {
	hub.Store
	addr string
}

func (f forwardOnlyStore) Lookup(string) (hub.Target, bool) {
	return hub.Target{ForwardAddr: f.addr}, true
}

// TestCredentialRelayUnreachableOwner proves a forward to a dead peer fails with a
// clear error instead of hanging: the caretaker's stream is dropped and the server
// logs a reason, rather than the pod blocking forever on a credential fetch.
func TestCredentialRelayUnreachableOwner(t *testing.T) {
	// A listener bound then closed gives an address nothing answers on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()

	s := newTestServerObj(t)
	s.hub = forwardOnlyStore{Store: hub.NewRegistry(), addr: "ws://" + dead}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.relayCredentialRemote(ctx, server, "session-x", "db") }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error forwarding to an unreachable owner")
		}
		if !strings.Contains(err.Error(), "forward to owning replica failed") {
			t.Fatalf("error = %v, want a forward-failure error", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("relayCredentialRemote hung on an unreachable owner")
	}
}

// TestCredentialRelayNoOwnerRecord covers the distributed miss: the store holds no
// routing record for the session (owner gone, or the CLI disconnected), so the
// relay reports errCredNoOwner instead of dialing anything.
func TestCredentialRelayNoOwnerRecord(t *testing.T) {
	s := newTestServerObj(t)
	s.hub = &countingHubStore{Store: hub.NewRegistry()} // distributed-looking, but empty

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	if err := s.relayCredentialRemote(context.Background(), server, "session-x", "db"); err != errCredNoOwner {
		t.Fatalf("err = %v, want errCredNoOwner", err)
	}
}

// TestCredentialRelaySingleReplicaMiss proves the single-replica path never touches
// the store: with the in-memory Registry no peer can hold a session this process
// does not, so an unknown session is reported as such immediately.
func TestCredentialRelaySingleReplicaMiss(t *testing.T) {
	s := newTestServerObj(t)

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	if err := s.relayCredentialRemote(context.Background(), server, "session-x", "db"); err != errCredUnknownSession {
		t.Fatalf("err = %v, want errCredUnknownSession", err)
	}
}

// TestCredentialForwardAuth proves /.cornus/v1/cred/forward mirrors
// /.cornus/v1/mount/forward's trust model: it requires a FULL credential — the
// scoped caretaker token (which the in-pod sidecar carries) must be rejected, while
// the server's own full token (what dialForward sends between replicas) is accepted.
// This is what keeps a forwarded request from carrying more authority than a local
// one: the hop is authenticated as a replica, and the name is still re-checked.
func TestCredentialForwardAuth(t *testing.T) {
	t.Setenv("CORNUS_AUTH_TOKEN", "full-secret")
	t.Setenv("CORNUS_CARETAKER_TOKEN", "caretaker-secret")
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/cred/forward"

	caretakerHdr := http.Header{}
	caretakerHdr.Set("Authorization", "Bearer caretaker-secret")
	if conn, err := wire.DialConnControlHeader(ctx, url, nil, caretakerHdr); err == nil {
		conn.Close()
		t.Fatal("caretaker-scoped token must be rejected on /.cornus/v1/cred/forward")
	}

	fullHdr := http.Header{}
	fullHdr.Set("Authorization", "Bearer full-secret")
	conn, err := wire.DialConnControlHeader(ctx, url, nil, fullHdr)
	if err != nil {
		t.Fatalf("full credential rejected on /.cornus/v1/cred/forward: %v", err)
	}
	conn.Close()
}
