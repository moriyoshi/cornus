package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/config"
	"cornus/pkg/deploy"
	"cornus/pkg/storage"
	"cornus/pkg/tunnel"
	"cornus/pkg/wire"
)

// fakeTunnelProvider is an in-memory tunnel.Provider: it captures the credential
// it was started with and hands the server test-supplied connections via a
// channel, so a test can drive an inbound "visitor" connection without any
// network or relay.
type fakeTunnelProvider struct {
	mu       sync.Mutex
	lastCred tunnel.Credential
	lastOpts tunnel.Options
	conns    chan net.Conn
	url      string
	startErr error
}

func (p *fakeTunnelProvider) Start(_ context.Context, cred tunnel.Credential, opts tunnel.Options) (tunnel.Session, error) {
	p.mu.Lock()
	p.lastCred, p.lastOpts = cred, opts
	p.mu.Unlock()
	if p.startErr != nil {
		return nil, p.startErr
	}
	return &fakeTunnelSession{conns: p.conns, url: p.url, closed: make(chan struct{})}, nil
}

func (p *fakeTunnelProvider) cred() tunnel.Credential {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastCred
}

type fakeTunnelSession struct {
	conns  chan net.Conn
	url    string
	closed chan struct{}
	once   sync.Once
}

func (s *fakeTunnelSession) URL() string { return s.url }

func (s *fakeTunnelSession) Accept() (net.Conn, error) {
	select {
	case c := <-s.conns:
		return c, nil
	case <-s.closed:
		return nil, net.ErrClosed
	}
}

func (s *fakeTunnelSession) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// newTunnelTestServer builds a Server wired to backend and injects prov as the
// tunnel provider (the default ngrok backend is not linked into server tests, so
// the manager's provider is nil until injected here). It returns both the
// underlying *Server and its httptest.Server.
func newTunnelTestServer(t *testing.T, backend deploy.Backend, prov tunnel.Provider) (*Server, *httptest.Server) {
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
	s.tunnels.backend = prov
	s.tunnels.openErr = nil
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// TestTunnelManagerBridgesToBackend proves the manager hosts a tunnel, bridges an
// inbound connection to the deployment port via Backend.ForwardPort (which the
// fake backend echoes), and tears the tunnel down on stop.
func TestTunnelManagerBridgesToBackend(t *testing.T) {
	fb := &fakeBackend{}
	conns := make(chan net.Conn, 1)
	prov := &fakeTunnelProvider{conns: conns, url: "https://abc.example"}
	m := &tunnelManager{name: "fake", backend: prov, sessions: map[string]*tunnelSession{}}

	st, err := m.start(fb, "web", "secret-token", nil, 8080, "http")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !st.Active || st.URL != "https://abc.example" || st.Port != 8080 {
		t.Fatalf("status = %+v", st)
	}
	if got := prov.cred().AuthToken; got != "secret-token" {
		t.Fatalf("provider got token %q, want secret-token", got)
	}

	// Drive one inbound visitor connection; the fake backend echoes.
	client, server := net.Pipe()
	conns <- server
	go func() { _, _ = client.Write([]byte("ping")) }()
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want ping", buf)
	}
	_ = client.Close()

	fb.mu.Lock()
	port := fb.fwdPort
	fb.mu.Unlock()
	if port != 8080 {
		t.Fatalf("ForwardPort saw port %d, want 8080", port)
	}

	if !m.status("web").Active {
		t.Fatal("tunnel not active before stop")
	}
	m.stop("web")
	if m.status("web").Active {
		t.Fatal("tunnel still active after stop")
	}
}

// fakeUpstreamProvider is an in-memory tunnel.UpstreamProvider. On StartUpstream
// it dials the manager-provided shim address — simulating the backend (e.g.
// cloudflared) forwarding an inbound edge connection to the local upstream — and
// hands that connection to the test.
type fakeUpstreamProvider struct {
	url   string
	dial  chan net.Conn
	creds chan tunnel.Credential
}

func (p *fakeUpstreamProvider) StartUpstream(_ context.Context, cred tunnel.Credential, _ tunnel.Options, upstreamURL string) (tunnel.UpstreamSession, error) {
	addr := strings.TrimPrefix(upstreamURL, "http://")
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	p.creds <- cred
	p.dial <- c
	return &fakeUpstreamSession{url: p.url}, nil
}

type fakeUpstreamSession struct{ url string }

func (s *fakeUpstreamSession) URL() string  { return s.url }
func (s *fakeUpstreamSession) Close() error { return nil }

// TestTunnelManagerUpstreamShim proves the upstream-model path: the manager
// stands up a local shim listener, the backend forwards a connection to it, and
// the manager bridges that connection to Backend.ForwardPort (which echoes).
func TestTunnelManagerUpstreamShim(t *testing.T) {
	fb := &fakeBackend{}
	prov := &fakeUpstreamProvider{url: "https://up.example", dial: make(chan net.Conn, 1), creds: make(chan tunnel.Credential, 1)}
	m := &tunnelManager{name: "fake-upstream", backend: prov, sessions: map[string]*tunnelSession{}}

	st, err := m.start(fb, "web", "", nil, 8080, "http")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !st.Active || st.URL != "https://up.example" {
		t.Fatalf("status = %+v", st)
	}
	<-prov.creds // StartUpstream ran

	client := <-prov.dial // the connection the backend forwarded to the shim
	go func() { _, _ = client.Write([]byte("ping")) }()
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want ping", buf)
	}
	_ = client.Close()

	fb.mu.Lock()
	port := fb.fwdPort
	fb.mu.Unlock()
	if port != 8080 {
		t.Fatalf("ForwardPort saw port %d, want 8080", port)
	}
	m.stop("web")
	if m.status("web").Active {
		t.Fatal("tunnel still active after stop")
	}
}

// countingTunnelProvider is a tunnel.Provider that counts how many sessions it
// opened and how many were closed, so a test can prove no session leaks. Each
// session's Accept blocks until Close, modeling a live relay endpoint.
type countingTunnelProvider struct {
	mu      sync.Mutex
	started int
	closed  int
}

func (p *countingTunnelProvider) Start(_ context.Context, _ tunnel.Credential, _ tunnel.Options) (tunnel.Session, error) {
	p.mu.Lock()
	p.started++
	p.mu.Unlock()
	return &countingTunnelSession{p: p, closed: make(chan struct{})}, nil
}

type countingTunnelSession struct {
	p      *countingTunnelProvider
	closed chan struct{}
	once   sync.Once
}

func (s *countingTunnelSession) URL() string { return "https://counting.example" }

func (s *countingTunnelSession) Accept() (net.Conn, error) {
	<-s.closed
	return nil, net.ErrClosed
}

func (s *countingTunnelSession) Close() error {
	s.once.Do(func() {
		s.p.mu.Lock()
		s.p.closed++
		s.p.mu.Unlock()
		close(s.closed)
	})
	return nil
}

// TestTunnelStartConcurrentReplaceNoLeak proves overlapping start() calls for one
// deployment name never orphan a session: exactly one live tunnel remains in the
// map and every other opened session is torn down (its Close is called), so no
// serve goroutine or public relay endpoint leaks. On the pre-fix code the
// check-and-set of m.sessions[name] was not atomic, so a loser's session was
// overwritten in the map yet never closed (started-closed > 1).
func TestTunnelStartConcurrentReplaceNoLeak(t *testing.T) {
	fb := &fakeBackend{}
	prov := &countingTunnelProvider{}
	m := &tunnelManager{name: "fake", backend: prov, sessions: map[string]*tunnelSession{}}

	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.start(fb, "web", "tok", nil, 8080, "http"); err != nil {
				t.Errorf("start: %v", err)
			}
		}()
	}
	wg.Wait()

	prov.mu.Lock()
	started, closed := prov.started, prov.closed
	prov.mu.Unlock()
	if started-closed != 1 {
		t.Fatalf("started=%d closed=%d: %d live sessions after concurrent starts, want exactly 1 (rest leaked)", started, closed, started-closed)
	}
	if len(m.sessions) != 1 {
		t.Fatalf("sessions map has %d entries, want 1", len(m.sessions))
	}

	// The surviving tunnel tears down cleanly too, leaving nothing open.
	m.stop("web")
	prov.mu.Lock()
	leftover := prov.started - prov.closed
	prov.mu.Unlock()
	if leftover != 0 {
		t.Fatalf("after stop: %d sessions still open, want 0", leftover)
	}
}

// panicAcceptProvider is a tunnel.Provider whose session's Accept panics on its
// first call, modeling a crash in the per-tunnel accept loop. It lets a test
// prove the manager's supervisor recovers that panic (rather than the panic
// tearing down the whole server process).
type panicAcceptProvider struct{ accepts atomic.Int64 }

func (p *panicAcceptProvider) Start(_ context.Context, _ tunnel.Credential, _ tunnel.Options) (tunnel.Session, error) {
	return &panicAcceptSession{p: p, closed: make(chan struct{})}, nil
}

type panicAcceptSession struct {
	p      *panicAcceptProvider
	closed chan struct{}
	once   sync.Once
}

func (s *panicAcceptSession) URL() string { return "https://panic.example" }

func (s *panicAcceptSession) Accept() (net.Conn, error) {
	if s.p.accepts.Add(1) == 1 {
		panic("synthetic panic in the tunnel accept loop")
	}
	<-s.closed
	return nil, net.ErrClosed
}

func (s *panicAcceptSession) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// TestTunnelAcceptLoopSupervisedAcrossPanic proves the per-tunnel accept loop is
// a panic-isolated supervised child: a panic in Accept is recovered instead of
// crashing the server process, and the tunnel still tears down cleanly (stop
// does not hang waiting on a loop that panicked). The loop is RemoveOnExit, not
// Restart — a resource-bounded accept loop should not busy-restart on a dead
// backend — so after the recovered panic Accept is not called again.
func TestTunnelAcceptLoopSupervisedAcrossPanic(t *testing.T) {
	fb := &fakeBackend{}
	prov := &panicAcceptProvider{}
	m := &tunnelManager{name: "fake", backend: prov, sessions: map[string]*tunnelSession{}}

	if _, err := m.start(fb, "web", "tok", nil, 8080, "http"); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for the accept loop to run and panic (recovered by the supervisor).
	deadline := time.Now().Add(5 * time.Second)
	for prov.accepts.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("accept loop never ran")
		}
		time.Sleep(time.Millisecond)
	}

	// stop must complete (not hang): the RemoveOnExit child already self-exited on
	// the recovered panic, so teardown's Remove returns at once. Reaching here at
	// all proves the panic did not crash the test process.
	done := make(chan struct{})
	go func() { m.stop("web"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop hung after the accept loop panicked")
	}
	if m.status("web").Active {
		t.Fatal("tunnel still active after stop")
	}

	// RemoveOnExit, not Restart: the loop exited on the recovered panic and was
	// not relaunched, so Accept was called exactly once.
	if got := prov.accepts.Load(); got != 1 {
		t.Fatalf("Accept called %d times; want exactly 1 (loop must not restart after the recovered panic)", got)
	}
}

// TestDeployTunnelEndpoint exercises POST/GET/DELETE on
// /.cornus/v1/deploy/{name}/tunnel and the request-validation errors.
func TestDeployTunnelEndpoint(t *testing.T) {
	clearAuthEnv(t)
	fb := &fakeBackend{}
	prov := &fakeTunnelProvider{conns: make(chan net.Conn, 1), url: "https://xyz.example"}
	_, srv := newTunnelTestServer(t, fb, prov)

	do := func(method string, body []byte) (int, api.TunnelStatus) {
		t.Helper()
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req, _ := http.NewRequest(method, srv.URL+"/.cornus/v1/deploy/web/tunnel", r)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var st api.TunnelStatus
		_ = json.NewDecoder(resp.Body).Decode(&st)
		return resp.StatusCode, st
	}

	// POST starts the tunnel and echoes the public URL.
	body, _ := json.Marshal(api.TunnelRequest{AuthToken: "tok", Port: 8080, Proto: "http"})
	if code, st := do(http.MethodPost, body); code != http.StatusOK || st.URL != "https://xyz.example" || !st.Active {
		t.Fatalf("POST tunnel: code=%d status=%+v", code, st)
	}
	if got := prov.cred().AuthToken; got != "tok" {
		t.Fatalf("injected token = %q, want tok", got)
	}

	// GET reflects the active tunnel.
	if code, st := do(http.MethodGet, nil); code != http.StatusOK || !st.Active || st.Port != 8080 {
		t.Fatalf("GET tunnel: code=%d status=%+v", code, st)
	}

	// DELETE stops it; GET then reports inactive.
	if code, _ := do(http.MethodDelete, nil); code != http.StatusOK {
		t.Fatalf("DELETE tunnel: code=%d", code)
	}
	if code, st := do(http.MethodGet, nil); code != http.StatusOK || st.Active {
		t.Fatalf("GET after delete: code=%d status=%+v", code, st)
	}

	// Missing token with no server default → 400.
	noTok, _ := json.Marshal(api.TunnelRequest{Port: 8080})
	if code, _ := do(http.MethodPost, noTok); code != http.StatusBadRequest {
		t.Fatalf("POST without token: code=%d, want 400", code)
	}
	// Out-of-range port → 400.
	badPort, _ := json.Marshal(api.TunnelRequest{AuthToken: "tok", Port: 0})
	if code, _ := do(http.MethodPost, badPort); code != http.StatusBadRequest {
		t.Fatalf("POST bad port: code=%d, want 400", code)
	}
}

// TestDeployTunnelServerDefaultToken proves the server-configured default
// credential (CORNUS_TUNNEL_AUTHTOKEN, modeled by the manager's defaultToken) is
// used when the request omits an authtoken.
func TestDeployTunnelServerDefaultToken(t *testing.T) {
	clearAuthEnv(t)
	fb := &fakeBackend{}
	prov := &fakeTunnelProvider{conns: make(chan net.Conn, 1), url: "https://def.example"}
	s, srv := newTunnelTestServer(t, fb, prov)
	s.tunnels.defaultToken = "server-default"

	body, _ := json.Marshal(api.TunnelRequest{Port: 9090}) // no AuthToken
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy/web/tunnel", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST with server default: code=%d, want 200", resp.StatusCode)
	}
	if got := prov.cred().AuthToken; got != "server-default" {
		t.Fatalf("provider got token %q, want server-default", got)
	}
}

// TestDeployTunnelAuthz proves the tunnel endpoint is gated on the `deploy`
// API-policy action: a disallowed identity is 403, while an allowed identity
// gets past the gate (and 400s here only because it supplied no token).
func TestDeployTunnelAuthz(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	clearAuthEnv(t)
	t.Setenv("CORNUS_JWT_HS256_SECRET", string(secret))
	t.Setenv("CORNUS_API_POLICY", `{"ci-bot":["deploy"]}`)

	fb := &fakeBackend{}
	prov := &fakeTunnelProvider{conns: make(chan net.Conn, 1), url: "https://z.example"}
	_, srv := newTunnelTestServer(t, fb, prov)

	post := func(sub string, body []byte) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/.cornus/v1/deploy/web/tunnel", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+jwtFor(t, secret, sub))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	noTok, _ := json.Marshal(api.TunnelRequest{Port: 8080})
	if code := post("stranger", noTok); code != http.StatusForbidden {
		t.Fatalf("stranger POST tunnel: code=%d, want 403", code)
	}
	if code := post("ci-bot", noTok); code != http.StatusBadRequest {
		t.Fatalf("ci-bot POST tunnel (no token): code=%d, want 400 (past authz)", code)
	}
}

// TestTunnelChannelWaitAndClaim proves the ordinary race between a channel dial
// and the following tunnel-start request: waitForChannel polls until
// registerChannel (running concurrently, as the two are independent HTTP
// requests in production) publishes the connection, and release() lets
// registerChannel's blocked call return.
func TestTunnelChannelWaitAndClaim(t *testing.T) {
	m := &tunnelManager{name: "ssh", sessions: map[string]*tunnelSession{}}
	client, srv := net.Pipe()
	defer client.Close()

	registerReturned := make(chan struct{})
	go func() {
		m.registerChannel(context.Background(), "web", "ssh-agent", srv)
		close(registerReturned)
	}()

	conn, release, ok := m.waitForChannel(context.Background(), "web", "ssh-agent")
	if !ok || conn != srv {
		t.Fatalf("waitForChannel: ok=%v conn=%v, want the registered conn", ok, conn)
	}
	release()
	select {
	case <-registerReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("registerChannel did not return after release")
	}
}

// TestTunnelChannelDroppedOnCancel proves an opened-but-unclaimed channel is
// removed from the registry (and its connection closed) once its context is
// cancelled — the same cleanup path the real TTL uses, exercised here via
// cancellation instead of a real 30s wait.
func TestTunnelChannelDroppedOnCancel(t *testing.T) {
	m := &tunnelManager{name: "ssh", sessions: map[string]*tunnelSession{}}
	client, srv := net.Pipe()
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	registerReturned := make(chan struct{})
	go func() {
		m.registerChannel(ctx, "web", "ssh-agent", srv)
		close(registerReturned)
	}()

	// Wait until the channel is actually registered before cancelling, so the
	// test doesn't race registerChannel's own map insert.
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		_, registered := m.channels["web"]["ssh-agent"]
		m.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("channel never appeared in the registry")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-registerReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("registerChannel did not return after context cancellation")
	}
	if pc := m.claimChannel("web", "ssh-agent"); pc != nil {
		t.Fatal("channel still claimable after registerChannel returned on cancellation")
	}
}

// TestDeployTunnelForwardAgent exercises the full ForwardAgent path over HTTP:
// the CLI's channel dial (GET/WS .../tunnel/channel/ssh-agent) followed by the
// tunnel-start POST, proving the provider receives a non-nil Credential.Agent.
func TestDeployTunnelForwardAgent(t *testing.T) {
	clearAuthEnv(t)
	fb := &fakeBackend{}
	prov := &fakeTunnelProvider{conns: make(chan net.Conn, 1), url: "https://agent.example"}
	s, srv := newTunnelTestServer(t, fb, prov)
	s.tunnels.name = "ssh" // ForwardAgent is gated on the ssh backend name

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/deploy/web/tunnel/channel/ssh-agent"
	conn, err := wire.DialConn(context.Background(), wsURL)
	if err != nil {
		t.Fatalf("dialing channel: %v", err)
	}
	defer conn.Close()

	body, _ := json.Marshal(api.TunnelRequest{ForwardAgent: true, Port: 8080, Proto: "http"})
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy/web/tunnel", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST forwardAgent: code=%d body=%s", resp.StatusCode, b)
	}
	if prov.cred().Agent == nil {
		t.Fatal("provider did not receive a forwarded agent")
	}
}

// TestDeployTunnelForwardAgentNoChannel proves a ForwardAgent request that
// never gets a matching channel dial fails explicitly (not a silent fallback
// to token auth, and not a hang).
func TestDeployTunnelForwardAgentNoChannel(t *testing.T) {
	clearAuthEnv(t)
	old := tunnelChannelWaitTimeout
	tunnelChannelWaitTimeout = 200 * time.Millisecond
	t.Cleanup(func() { tunnelChannelWaitTimeout = old })
	fb := &fakeBackend{}
	prov := &fakeTunnelProvider{conns: make(chan net.Conn, 1), url: "https://agent.example"}
	s, srv := newTunnelTestServer(t, fb, prov)
	s.tunnels.name = "ssh"

	body, _ := json.Marshal(api.TunnelRequest{ForwardAgent: true, Port: 8080, Proto: "http"})
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy/web/tunnel", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST forwardAgent with no channel: code=%d, want 400", resp.StatusCode)
	}
}

// TestDeployTunnelForwardAgentWrongBackend proves ForwardAgent is rejected
// outright for a non-ssh backend, rather than silently doing nothing with it.
func TestDeployTunnelForwardAgentWrongBackend(t *testing.T) {
	clearAuthEnv(t)
	fb := &fakeBackend{}
	prov := &fakeTunnelProvider{conns: make(chan net.Conn, 1), url: "https://agent.example"}
	_, srv := newTunnelTestServer(t, fb, prov) // default manager name is not "ssh"

	body, _ := json.Marshal(api.TunnelRequest{ForwardAgent: true, Port: 8080, Proto: "http"})
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy/web/tunnel", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST forwardAgent on non-ssh backend: code=%d, want 400", resp.StatusCode)
	}
}

// TestDeployTunnelChannelUnknownPurpose proves the channel endpoint rejects any
// purpose other than the ones it recognizes today.
func TestDeployTunnelChannelUnknownPurpose(t *testing.T) {
	clearAuthEnv(t)
	fb := &fakeBackend{}
	prov := &fakeTunnelProvider{conns: make(chan net.Conn, 1), url: "https://agent.example"}
	_, srv := newTunnelTestServer(t, fb, prov)

	resp, err := http.Get(srv.URL + "/.cornus/v1/deploy/web/tunnel/channel/bogus")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET unknown channel purpose: code=%d, want 400", resp.StatusCode)
	}
}
