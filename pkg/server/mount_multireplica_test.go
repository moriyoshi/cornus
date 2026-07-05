package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"cornus/pkg/api"
	"cornus/pkg/config"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/hub"
	"cornus/pkg/storage"
	"cornus/pkg/wire"
)

// newMountReplicaServer builds a *Server over the given deploy backend, wrapped in
// httptest, with its hub swapped for a RedisStore over mr — like
// newMultiReplicaServer (hub_multireplica_test.go), but with a caller-chosen
// backend so a replica can host deploy-attach mount sessions.
func newMountReplicaServer(t *testing.T, mr *miniredis.Miniredis, replicaID string, backend deploy.Backend) (*httptest.Server, *hub.RedisStore) {
	t.Helper()
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dir}, st)
	if err != nil {
		t.Fatal(err)
	}
	s.newBackend = func() (deploy.Backend, error) { return backend, nil }
	ts := httptest.NewServer(s.Handler())

	wsBase := "ws" + strings.TrimPrefix(ts.URL, "http")
	store, err := hub.NewRedisStore(context.Background(), "redis://"+mr.Addr(), replicaID, wsBase)
	if err != nil {
		ts.Close()
		t.Fatalf("redis store %s: %v", replicaID, err)
	}
	// Swap the in-memory Registry for the shared RedisStore. Handlers read s.hub
	// through the interface at request time, and no request has arrived yet.
	s.hub = store
	t.Cleanup(func() { _ = store.Close(); ts.Close() })
	return ts, store
}

// TestMountRelayCrossReplica is the key artifact for cross-replica mount
// forwarding: two Server replicas share one Redis. The deploy-attach session (the
// caller's live 9P export) attaches to replica A; the pod's caretaker opens its
// mount stream on replica B. B does not hold the session, so it resolves the
// session's routing record to A's forward URL and forwards the stream to A's
// /.cornus/v1/mount/forward, which bridges to the caller's export — proving
// caretaker -> B -> A -> caller end to end over real 9P. It also checks the
// catalog surface stays clean of the internal routing record, and that closing
// the CLI session unregisters it (a later relay via B errors cleanly).
func TestMountRelayCrossReplica(t *testing.T) {
	mr := miniredis.RunT(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("CROSS-REPLICA"), 0o644); err != nil {
		t.Fatal(err)
	}

	fb := &fakeMountingBackend{mounts: make(chan []deploy.AttachMount, 1)}
	tsA, _ := newMountReplicaServer(t, mr, "replicaA", fb)
	tsB, storeB := newMountReplicaServer(t, mr, "replicaB", &fakeBackend{})

	wsA := "ws" + strings.TrimPrefix(tsA.URL, "http")
	wsB := "ws" + strings.TrimPrefix(tsB.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsA)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	as := deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:   "web",
			Image:  "img",
			Mounts: []api.Mount{{Source: "/client/x", Target: "/data", ReadOnly: true}},
		},
		LocalMounts: []deploywire.LocalMount{{Index: 0, Name: "m0", ReadOnly: true}},
	}
	attachCtx, attachCancel := context.WithCancel(ctx)
	defer attachCancel()
	go func() {
		_ = deploywire.Serve(attachCtx, wsA+"/.cornus/v1/deploy/attach", as,
			map[string]string{"m0": dir}, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var mounts []deploy.AttachMount
	select {
	case mounts = <-fb.mounts:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithMounts")
	}
	session := mounts[0].Session

	// Play the pod's caretaker AGAINST REPLICA B (the wrong replica): the mount
	// stream must be forwarded to A and still reach the caller's export.
	mux, err := wire.Dial(ctx, wsB+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach on B: %v", err)
	}
	defer mux.Close()
	if got := readMarkerOverMux(t, mux, session, "m0"); got != "CROSS-REPLICA" {
		t.Errorf("mount via wrong replica = %q, want CROSS-REPLICA", got)
	}

	// The routing record exists in the shared store (B resolved it) but must be
	// hidden from the catalog surface — it is internal state, not a service.
	if !hasMountRecord(storeB.Catalog()) {
		t.Error("expected a mount-session routing record in the raw store catalog")
	}
	if names := fetchCatalog(t, tsA.URL); hasMountRecord(names) {
		t.Errorf("/.cornus/v1/hub/catalog leaked a mount routing record: %v", names)
	}

	// Teardown: the CLI disconnects, the session dies, and A unregisters the
	// record. A later mount stream via B must be closed cleanly, not hang.
	attachCancel()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := storeB.Lookup(mountServiceName(session)); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mount routing record never unregistered after session close")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stream, err := wire.OpenTagged(mux, wire.TagMount)
	if err != nil {
		t.Fatalf("open post-teardown mount stream: %v", err)
	}
	defer stream.Close()
	if _, err := io.WriteString(stream, session+"\nm0\n"); err != nil {
		t.Fatalf("write post-teardown session/name: %v", err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	if _, err := stream.Read(buf); err == nil {
		t.Fatal("expected the mount stream to be closed after the session ended")
	}
}

// hasMountRecord reports whether names contains a reserved mount routing record.
func hasMountRecord(names []string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, mountServicePrefix) {
			return true
		}
	}
	return false
}

// fetchCatalog reads /.cornus/v1/hub/catalog and returns the service names.
func fetchCatalog(t *testing.T, baseURL string) []string {
	t.Helper()
	resp, err := http.Get(baseURL + "/.cornus/v1/hub/catalog")
	if err != nil {
		t.Fatalf("get catalog: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Services []string `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	return body.Services
}

// countingHubStore wraps a hub.Store and counts Lookup calls, so a test can assert
// the local-session fast path never consults the store.
type countingHubStore struct {
	hub.Store
	lookups atomic.Int64
}

func (c *countingHubStore) Lookup(name string) (hub.Target, bool) {
	c.lookups.Add(1)
	return c.Store.Lookup(name)
}

// TestMountRelayLocalFastPath proves a mount stream for a session held by the SAME
// replica is bridged without a single store Lookup: the local registry is checked
// first, so single-replica relay behavior involves no store round-trips. (The
// wrapper is not the in-memory *hub.Registry, so the server treats the store as
// distributed — the strictest case for the assertion.)
func TestMountRelayLocalFastPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("LOCAL-FAST"), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	st, err := storage.Open(context.Background(), dataDir, dataDir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dataDir}, st)
	if err != nil {
		t.Fatal(err)
	}
	fb := &fakeMountingBackend{mounts: make(chan []deploy.AttachMount, 1)}
	s.newBackend = func() (deploy.Backend, error) { return fb, nil }
	counting := &countingHubStore{Store: hub.NewRegistry()}
	s.hub = counting
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	wsBase := "ws" + strings.TrimPrefix(ts.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	as := deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:   "web",
			Image:  "img",
			Mounts: []api.Mount{{Source: "/client/x", Target: "/data", ReadOnly: true}},
		},
		LocalMounts: []deploywire.LocalMount{{Index: 0, Name: "m0", ReadOnly: true}},
	}
	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", as,
			map[string]string{"m0": dir}, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var mounts []deploy.AttachMount
	select {
	case mounts = <-fb.mounts:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithMounts")
	}
	session := mounts[0].Session

	mux, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach: %v", err)
	}
	defer mux.Close()
	if got := readMarkerOverMux(t, mux, session, "m0"); got != "LOCAL-FAST" {
		t.Errorf("local mount = %q, want LOCAL-FAST", got)
	}

	if n := counting.lookups.Load(); n != 0 {
		t.Errorf("local-session relay consulted the store %d times, want 0", n)
	}
}

// TestMountForwardUnknownSession confirms the inter-replica forward endpoint fails
// closed: a session this replica does not hold closes the stream (it never
// re-forwards, so a stale record cannot cause a loop).
func TestMountForwardUnknownSession(t *testing.T) {
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := wire.DialConn(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/.cornus/v1/mount/forward")
	if err != nil {
		t.Fatalf("dial mount forward: %v", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "nope\nm0\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected the forward stream to be closed for an unknown session")
	}
}

// TestMountForwardAuth proves /.cornus/v1/mount/forward mirrors /.cornus/v1/hub/forward's trust
// model: it requires a FULL credential — the scoped caretaker token (which the
// in-pod sidecar carries) must be rejected, while the server's own full token
// (what dialForward sends between replicas) is accepted.
func TestMountForwardAuth(t *testing.T) {
	t.Setenv("CORNUS_AUTH_TOKEN", "full-secret")
	t.Setenv("CORNUS_CARETAKER_TOKEN", "caretaker-secret")
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/mount/forward"

	caretakerHdr := http.Header{}
	caretakerHdr.Set("Authorization", "Bearer caretaker-secret")
	if conn, err := wire.DialConnControlHeader(ctx, url, nil, caretakerHdr); err == nil {
		conn.Close()
		t.Fatal("caretaker-scoped token must be rejected on /.cornus/v1/mount/forward")
	}

	fullHdr := http.Header{}
	fullHdr.Set("Authorization", "Bearer full-secret")
	conn, err := wire.DialConnControlHeader(ctx, url, nil, fullHdr)
	if err != nil {
		t.Fatalf("full credential rejected on /.cornus/v1/mount/forward: %v", err)
	}
	conn.Close()
}
