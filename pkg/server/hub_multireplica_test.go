package server

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hashicorp/yamux"

	"cornus/pkg/caretaker"
	"cornus/pkg/config"
	"cornus/pkg/deploy"
	"cornus/pkg/hub"
	"cornus/pkg/storage"
)

// aliveTTLForTest mirrors the RedisStore alive TTL for the liveness fast-forward.
const aliveTTLForTest = 15 * time.Second

// newMultiReplicaServer builds a *Server wrapped in httptest, then swaps its hub for
// a RedisStore over mr. forwardAddr is the ws base of the SERVER'S OWN httptest URL,
// so a peer replica can dial its /.cornus/v1/hub/forward. Returns the httptest server and
// the store (for liveness manipulation / Lookup assertions).
func newMultiReplicaServer(t *testing.T, mr *miniredis.Miniredis, replicaID string) (*httptest.Server, *hub.RedisStore) {
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
	s.newBackend = func() (deploy.Backend, error) { return &fakeBackend{}, nil }
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

func startEcho(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

// reachEcho opens a hub data stream to name over sess, writes a probe, and returns
// the echoed line, retrying while the cross-replica registration converges.
func reachEcho(t *testing.T, sess *yamux.Session, name string) string {
	t.Helper()
	for i := 0; i < 150; i++ {
		stream, err := hub.OpenTo(sess, name)
		if err != nil {
			t.Fatalf("open data stream: %v", err)
		}
		_, _ = stream.Write([]byte("ping\n"))
		_ = stream.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		line, err := bufio.NewReader(stream).ReadString('\n')
		stream.Close()
		if err == nil && line == "ping\n" {
			return line
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ""
}

// TestHubMultiReplicaCrossReplicaDelivery is the key artifact: two Server replicas
// share one Redis. A hosting spoke registers a DELIVERY service on replica A; a
// reaching spoke on replica B opens a stream to it by name. B's Lookup resolves the
// service to A's forwardAddr, so B forwards the relay to A's /.cornus/v1/hub/forward, and A
// opens the ingress stream to its own spoke, which dials the local echo — proving
// cross-replica delivery (reaching spoke -> B -> A -> hosting spoke).
func TestHubMultiReplicaCrossReplicaDelivery(t *testing.T) {
	mr := miniredis.RunT(t)

	echo := startEcho(t)
	tsA, _ := newMultiReplicaServer(t, mr, "replicaA")
	tsB, _ := newMultiReplicaServer(t, mr, "replicaB")

	// Hosting spoke -> replica A: register "greeter" for DELIVERY (no Addr), backed
	// by a local echo. caretaker.Run services the ingress-delivery streams.
	hostCtx, hostCancel := context.WithCancel(context.Background())
	defer hostCancel()
	go func() {
		_ = caretaker.Run(hostCtx, caretaker.Config{Hub: &caretaker.HubRole{
			Server:   tsA.URL,
			Register: []caretaker.HubService{{Name: "greeter", Target: echo.Addr().String()}},
		}})
	}()

	// Reaching spoke -> replica B: reach "greeter" through the hub.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sessB, err := hub.Dial(ctx, "ws"+strings.TrimPrefix(tsB.URL, "http")+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("reaching dial B: %v", err)
	}
	defer sessB.Close()

	if got := reachEcho(t, sessB, "greeter"); got != "ping\n" {
		t.Fatalf("cross-replica delivery echo = %q, want %q", got, "ping\n")
	}
}

// TestHubMultiReplicaCrossReplicaDialDirect proves the shared registry alone makes
// dial-direct multi-replica work: replica A registers a direct Addr; a reaching spoke
// on replica B dials it directly (no forwarding needed) and the echo returns.
func TestHubMultiReplicaCrossReplicaDialDirect(t *testing.T) {
	mr := miniredis.RunT(t)

	echo := startEcho(t)
	tsA, _ := newMultiReplicaServer(t, mr, "replicaA")
	tsB, _ := newMultiReplicaServer(t, mr, "replicaB")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Register "direct-echo" -> a hub-dialable Addr on replica A.
	sessA, err := hub.Dial(ctx, "ws"+strings.TrimPrefix(tsA.URL, "http")+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("register dial A: %v", err)
	}
	defer sessA.Close()
	ctl, err := hub.Register(sessA, hub.Registration{Services: []hub.Service{
		{Name: "direct-echo", Addr: echo.Addr().String()},
	}})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer ctl.Close()

	// Reach it via replica B: B reads the shared Addr and dials it directly.
	sessB, err := hub.Dial(ctx, "ws"+strings.TrimPrefix(tsB.URL, "http")+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("reaching dial B: %v", err)
	}
	defer sessB.Close()

	if got := reachEcho(t, sessB, "direct-echo"); got != "ping\n" {
		t.Fatalf("cross-replica dial-direct echo = %q, want %q", got, "ping\n")
	}
}

// TestHubMultiReplicaLiveness proves a dead replica's providers vanish from a peer's
// view: A registers "greeter", then A's liveness key is expired (FastForward past the
// TTL). B's Lookup must no longer resolve it.
func TestHubMultiReplicaLiveness(t *testing.T) {
	mr := miniredis.RunT(t)

	tsA, _ := newMultiReplicaServer(t, mr, "replicaA")
	_, storeB := newMultiReplicaServer(t, mr, "replicaB")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessA, err := hub.Dial(ctx, "ws"+strings.TrimPrefix(tsA.URL, "http")+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("register dial A: %v", err)
	}
	defer sessA.Close()
	// A delivery registration (no Addr): B's Lookup resolves it to A's forwardAddr
	// while A is live, and drops it once A's liveness lapses.
	ctl, err := hub.Register(sessA, hub.Registration{Services: []hub.Service{
		{Name: "greeter"},
	}})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer ctl.Close()

	// B sees the provider once A's registration lands.
	resolved := false
	for i := 0; i < 150; i++ {
		if _, ok := storeB.Lookup("greeter"); ok {
			resolved = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !resolved {
		t.Fatal("B never saw A's greeter provider")
	}

	// Expire keys past the alive TTL so A's liveness lapses. miniredis does not run
	// the heartbeat's wall-clock refresh, so after fast-forwarding A reads as dead.
	mr.FastForward(aliveTTLForTest + time.Second)

	if _, ok := storeB.Lookup("greeter"); ok {
		t.Fatal("B still resolves greeter after A's liveness expired, want gone")
	}
}
