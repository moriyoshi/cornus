package caretaker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"cornus/pkg/wire"
)

// TestConfigRoundTrip confirms the config the k8s backend marshals into the
// sidecar env var parses back to the same roles.
func TestConfigRoundTrip(t *testing.T) {
	in := Config{Mounts: []MountRole{
		{Server: "ws://relay", Session: "s", Name: "m0", Target: "/cornus/mounts/0"},
		{Server: "ws://relay", Session: "s", Name: "m1", Target: "/cornus/mounts/1", ReadOnly: true},
	}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Mounts) != 2 || out.Mounts[0].Name != "m0" || out.Mounts[1].Name != "m1" || !out.Mounts[1].ReadOnly {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

// TestCooperativeForwards drives the no-privilege data path end to end: an echo
// "peer" on 127.0.0.1:P, a cooperative listener on a distinct loopback address
// on the same port P, and a client that connects to the loopback and gets its
// bytes echoed back — proving the sidecar splices the connection to the peer's
// real address without any redirect or privilege.
func TestCooperativeForwards(t *testing.T) {
	// Echo "peer" — stands in for the peer's real Service.
	peer, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("peer listen: %v", err)
	}
	defer peer.Close()
	go func() {
		for {
			c, err := peer.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	port := peer.Addr().(*net.TCPAddr).Port

	// Cooperative sidecar: listen on 127.0.0.9:port, forward to the peer (same port).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		errc <- runCooperative(ctx, []CoopUpstream{
			{Listen: "127.0.0.9", Forward: "127.0.0.1", Ports: []int{port}},
		}, 0)
	}()

	// The listener binds asynchronously; retry the client dial briefly.
	addr := net.JoinHostPort("127.0.0.9", itoa(port))
	var conn net.Conn
	for i := 0; i < 50; i++ {
		if conn, err = net.DialTimeout("tcp", addr, time.Second); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "ping\n" {
		t.Fatalf("echo = %q, want %q", got, "ping\n")
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("runCooperative: %v", err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestReadyFailsOnMissingMount confirms the readiness check reports not-ready
// when a role's target is not a live mountpoint (the startup probe's contract),
// and treats an empty config as ready (no roles to wait on).
func TestReadyFailsOnMissingMount(t *testing.T) {
	if err := Ready(Config{}); err != nil {
		t.Errorf("empty config should be ready, got %v", err)
	}
	// A path that certainly is not a mountpoint.
	if err := Ready(Config{Mounts: []MountRole{{Name: "m", Target: "/definitely/not/a/mountpoint/xyz"}}}); err == nil {
		t.Error("Ready should fail when a mount target is not a live mountpoint")
	}
}

// TestProxyConfigRoundTrip confirms a Proxy role survives the JSON the k8s
// backend delivers via the env var.
func TestProxyConfigRoundTrip(t *testing.T) {
	in := Config{Proxy: &ProxyRole{ListenPort: 15001, Allow: []string{"web", "db"}}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Proxy == nil || out.Proxy.ListenPort != 15001 || len(out.Proxy.Allow) != 2 {
		t.Fatalf("proxy round-trip mismatch: %+v", out.Proxy)
	}
}

// TestCooperativeConfigRoundTrip confirms the cooperative proxy's upstream table
// survives the env-var JSON.
func TestCooperativeConfigRoundTrip(t *testing.T) {
	in := Config{Proxy: &ProxyRole{Mode: "cooperative", Coop: []CoopUpstream{
		{Listen: "127.0.1.1", Forward: "web.default.svc.cluster.local", Ports: []int{80, 443}},
	}}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Proxy == nil || out.Proxy.Mode != "cooperative" || len(out.Proxy.Coop) != 1 {
		t.Fatalf("cooperative round-trip mismatch: %+v", out.Proxy)
	}
	u := out.Proxy.Coop[0]
	if u.Listen != "127.0.1.1" || u.Forward != "web.default.svc.cluster.local" || len(u.Ports) != 2 {
		t.Fatalf("upstream mismatch: %+v", u)
	}
}

// TestMarkConfigRoundTrip confirms the SO_MARK (proxy+mounts exemption) survives
// the env-var JSON.
func TestMarkConfigRoundTrip(t *testing.T) {
	in := Config{
		Mounts: []MountRole{{Name: "m", Target: "/t", Server: "ws://r", Session: "s"}},
		Proxy:  &ProxyRole{ListenPort: 15001, Allow: []string{"web"}},
		Mark:   1337,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Mark != 1337 || out.Proxy == nil || len(out.Mounts) != 1 {
		t.Fatalf("mark round-trip mismatch: %+v", out)
	}
}

// TestAllowSet checks the membership decision the enforcing proxy makes: only
// IPs of resolved allow-names are permitted, and a total-failure refresh never
// clears a non-empty set (so a DNS blip does not black-hole every peer).
func TestAllowSet(t *testing.T) {
	as := newAllowSet([]string{"web", "db"})
	as.mu.Lock()
	as.ips = map[string]bool{"10.0.0.5": true, "10.0.0.6": true}
	as.mu.Unlock()

	if !as.allowed("10.0.0.5") {
		t.Error("10.0.0.5 should be allowed (a resolved peer IP)")
	}
	if as.allowed("10.0.0.9") {
		t.Error("10.0.0.9 (no peer) must be denied")
	}
	// A refresh where every name fails to resolve keeps the prior set.
	as.names = []string{"nonexistent.invalid."}
	as.refresh(context.Background())
	if !as.allowed("10.0.0.5") {
		t.Error("a total DNS-failure refresh must not clear the allow-set")
	}
}

func TestCaretakerURL(t *testing.T) {
	for _, tc := range []struct{ server, instance, want string }{
		{"http://h:5000", "", "ws://h:5000/.cornus/v1/caretaker/attach"},
		{"https://h:5000/", "", "wss://h:5000/.cornus/v1/caretaker/attach"},
		{"ws://h:5000", "", "ws://h:5000/.cornus/v1/caretaker/attach"},
		{"http://h:5000", "web/0", "ws://h:5000/.cornus/v1/caretaker/attach?instance=web%2F0"},
	} {
		if got := caretakerURL(tc.server, tc.instance); got != tc.want {
			t.Errorf("caretakerURL(%q, %q) = %q, want %q", tc.server, tc.instance, got, tc.want)
		}
	}
}

// TestGroupByServer confirms a pod's mounts and hub role collapse onto one
// connection per server, while distinct servers stay separate.
func TestGroupByServer(t *testing.T) {
	one := groupByServer(Config{
		Mounts: []MountRole{
			{Server: "ws://r", Session: "s", Name: "m0"},
			{Server: "ws://r", Session: "s", Name: "m1"},
		},
		Hub: &HubRole{Server: "ws://r", Register: []HubService{{Name: "web", Addr: "1.2.3.4:80"}}},
	})
	if len(one) != 1 || len(one[0].mounts) != 2 || one[0].hub == nil {
		t.Fatalf("mounts+hub on one server should be one bundle with both, got %+v", one)
	}
	two := groupByServer(Config{Mounts: []MountRole{
		{Server: "ws://a", Session: "s1", Name: "x"},
		{Server: "ws://b", Session: "s2", Name: "y"},
	}})
	if len(two) != 2 {
		t.Fatalf("two servers should be two bundles, got %d", len(two))
	}

	// The pod-wide bearer token is stamped onto every bundle so the handshake to a
	// bearer-auth server is accepted.
	tok := groupByServer(Config{
		Token:  "sekret",
		Mounts: []MountRole{{Server: "ws://a", Name: "x"}, {Server: "ws://b", Name: "y"}},
	})
	for _, sb := range tok {
		if sb.token != "sekret" {
			t.Fatalf("bundle for %s missing token, got %q", sb.server, sb.token)
		}
	}
	// No token configured -> bundles carry none (auth off).
	none := groupByServer(Config{Mounts: []MountRole{{Server: "ws://a", Name: "x"}}})
	if none[0].token != "" {
		t.Fatalf("expected empty token, got %q", none[0].token)
	}
}

// TestRunCaretakerConnCleanShutdownOnCtxCancel proves ordinary pod teardown (ctx
// cancellation) makes runCaretakerConn return nil promptly — the supervisor's
// run() loop treats a nil-vs-non-nil return the same when ctx is already
// cancelled (never restart), but the CALLER (the outer supervisor in Run) only
// needs to redial when the connection genuinely dropped, so this must be
// distinguishable from TestRunCaretakerConnErrorsOnSessionClose below.
func TestRunCaretakerConnCleanShutdownOnCtxCancel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.cornus/v1/caretaker/attach", func(w http.ResponseWriter, r *http.Request) {
		sess, err := wire.Accept(w, r)
		if err != nil {
			return
		}
		<-r.Context().Done() // hold the session open until the client (or test) disconnects
		sess.Close()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runCaretakerConn(ctx, serverBundle{server: srv.URL}, 0) }()

	time.Sleep(100 * time.Millisecond) // let the dial/handshake complete
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runCaretakerConn on ctx cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runCaretakerConn did not return after ctx cancellation")
	}
}

// TestRunCaretakerConnErrorsOnSessionClose proves a connection dropping out from
// under runCaretakerConn (detected via yamux's sess.CloseChan(), not any role's
// own error) makes it return a non-nil error — which is what tells the OUTER
// supervisor (Run) to redial this one connection with backoff, independent of
// a pod's other server connections and roles.
func TestRunCaretakerConnErrorsOnSessionClose(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.cornus/v1/caretaker/attach", func(w http.ResponseWriter, r *http.Request) {
		sess, err := wire.Accept(w, r)
		if err != nil {
			return
		}
		sess.Close() // simulate the connection dropping right after the handshake
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runCaretakerConn(ctx, serverBundle{server: srv.URL}, 0) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("runCaretakerConn should return an error when the session closes out from under it")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runCaretakerConn did not notice the session closing")
	}
}

// TestApplyEnvToken proves a secret-sourced CORNUS_TOKEN overrides an embedded
// config token (the hardened path), and that an unset env leaves the config token
// intact (backward-compatible fallback).
func TestApplyEnvToken(t *testing.T) {
	t.Setenv("CORNUS_TOKEN", "from-secret")
	cfg := Config{Token: "embedded"}
	applyEnvToken(&cfg)
	if cfg.Token != "from-secret" {
		t.Fatalf("token = %q, want the env value to win", cfg.Token)
	}

	t.Setenv("CORNUS_TOKEN", "")
	cfg2 := Config{Token: "embedded"}
	applyEnvToken(&cfg2)
	if cfg2.Token != "embedded" {
		t.Fatalf("token = %q, want the embedded fallback preserved", cfg2.Token)
	}
}
