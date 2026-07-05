package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh/agent"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
	"cornus/pkg/supervisor"
	"cornus/pkg/tunnel"
	"cornus/pkg/wire"
)

// tunnelManager owns the server's live public tunnels, one per deployment name.
// A tunnel hosts a relay endpoint (via a tunnel.Provider) and bridges every
// inbound connection to the workload's port through deploy.Backend.ForwardPort —
// the same byte-bridge `cornus port-forward` uses, so it reaches ports the
// workload never published, on any backend. The backend credential lives only in
// the provider Session; the manager never stores or logs the raw authtoken.
type tunnelManager struct {
	name         string // backend name (for diagnostics)
	backend      any    // tunnel.Provider or tunnel.UpstreamProvider; nil when unopened
	openErr      error  // why backend is nil (reported on first use)
	defaultToken string // CORNUS_TUNNEL_AUTHTOKEN; used when a request omits one

	mu       sync.Mutex
	sessions map[string]*tunnelSession
	channels map[string]map[string]*pendingChannel // name -> purpose -> pending side-channel

	// sup hosts each live tunnel's accept loop as a panic-isolated child. It is
	// the manager's OWN supervisor, deliberately NOT the server's s.sup: a tunnel
	// accept loop only unblocks when its backend/listener is closed (Accept
	// ignores context), which happens during teardown/closeAll — so rooting these
	// loops in the process-wide s.sup, whose Wait() runs BEFORE closeAll during
	// shutdown, would deadlock. Built lazily by serveSupervisor (rather than in
	// newTunnelManager) so managers constructed directly in tests get one too.
	supMu     sync.Mutex
	sup       *supervisor.Supervisor
	supCancel context.CancelFunc
}

// serveSupervisor returns the manager's private supervisor tree, creating it on
// first use. Each tunnel's accept loop runs under it as a panic-recovered child.
func (m *tunnelManager) serveSupervisor() *supervisor.Supervisor {
	m.supMu.Lock()
	defer m.supMu.Unlock()
	if m.sup == nil {
		ctx, cancel := context.WithCancel(context.Background())
		m.sup = supervisor.New(ctx, nil)
		m.supCancel = cancel
	}
	return m.sup
}

// pendingChannel is one side-channel opened via the tunnel/channel/{purpose}
// endpoint, waiting to be claimed by a matching tunnel-start request. done is
// closed by the claimer once it is finished using conn, which is what lets
// handleDeployTunnelChannel's goroutine return and close the underlying
// connection — the channel's lifetime is bounded to "claimed and used", not the
// whole tunnel's lifetime.
type pendingChannel struct {
	conn net.Conn
	done chan struct{}
}

// tunnelChannelTTL bounds how long an opened-but-unclaimed side-channel lingers
// before it is closed and dropped — protects against a client that opens the
// channel but never follows up with (or gives up before) the matching
// tunnel-start request.
const tunnelChannelTTL = 30 * time.Second

// tunnelChannelWaitTimeout bounds how long a tunnel-start request waits for its
// requested side-channel to arrive, covering the ordinary race between the two
// requests (the channel dial and the start POST are independent HTTP calls). A
// var, not a const, so tests can shrink it rather than waiting out the real
// timeout to exercise the no-channel-arrived path.
var tunnelChannelWaitTimeout = 10 * time.Second

// registerChannel publishes conn as the pending purpose-channel for name and
// blocks until it is claimed (by a matching waitForChannel), its TTL elapses
// unclaimed, or ctx is cancelled (the client disconnected) — whichever comes
// first. The caller is expected to have deferred conn.Close() around this
// call, exactly like the exec/attach/port-forward handlers do for their own
// single-stream connections.
func (m *tunnelManager) registerChannel(ctx context.Context, name, purpose string, conn net.Conn) {
	pc := &pendingChannel{conn: conn, done: make(chan struct{})}
	m.mu.Lock()
	if m.channels == nil {
		m.channels = map[string]map[string]*pendingChannel{}
	}
	inner := m.channels[name]
	if inner == nil {
		inner = map[string]*pendingChannel{}
		m.channels[name] = inner
	}
	if old := inner[purpose]; old != nil {
		// A stale, still-unclaimed channel from an earlier attempt for the same
		// name/purpose — replace it rather than leaking it.
		old.conn.Close()
	}
	inner[purpose] = pc
	m.mu.Unlock()

	select {
	case <-pc.done:
	case <-ctx.Done():
		m.dropChannel(name, purpose, pc)
	case <-time.After(tunnelChannelTTL):
		m.dropChannel(name, purpose, pc)
	}
}

// dropChannel removes pc from the registry if it is still the current pending
// channel for name/purpose (it may already have been claimed and replaced).
func (m *tunnelManager) dropChannel(name, purpose string, pc *pendingChannel) {
	m.mu.Lock()
	if inner := m.channels[name]; inner != nil && inner[purpose] == pc {
		delete(inner, purpose)
		if len(inner) == 0 {
			delete(m.channels, name)
		}
	}
	m.mu.Unlock()
}

// claimChannel removes and returns the pending purpose-channel for name, if
// one is currently registered.
func (m *tunnelManager) claimChannel(name, purpose string) *pendingChannel {
	m.mu.Lock()
	defer m.mu.Unlock()
	inner := m.channels[name]
	if inner == nil {
		return nil
	}
	pc := inner[purpose]
	if pc == nil {
		return nil
	}
	delete(inner, purpose)
	if len(inner) == 0 {
		delete(m.channels, name)
	}
	return pc
}

// waitForChannel claims the pending purpose-channel for name, polling briefly
// to cover the ordinary race with the concurrent channel-dial request, up to
// tunnelChannelWaitTimeout. On success it returns the channel's net.Conn and a
// release func the caller must call exactly once when done using it — release
// unblocks the registering handler's goroutine so it can close the connection.
func (m *tunnelManager) waitForChannel(ctx context.Context, name, purpose string) (net.Conn, func(), bool) {
	deadline := time.Now().Add(tunnelChannelWaitTimeout)
	for {
		if pc := m.claimChannel(name, purpose); pc != nil {
			return pc.conn, func() { close(pc.done) }, true
		}
		if time.Now().After(deadline) {
			return nil, nil, false
		}
		select {
		case <-ctx.Done():
			return nil, nil, false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// tunnelSession is one live tunnel. cancel cancels the session context, which
// severs in-flight bridges on teardown; tok is the accept loop's handle in the
// manager's supervisor (its completion channel replaces the old hand-rolled done
// channel — teardown waits on it via Remove); closeFn tears down the backend
// tunnel (and any shim listener).
type tunnelSession struct {
	url     string
	port    int
	cancel  context.CancelFunc
	tok     *supervisor.Token
	closeFn func() error

	// dead is set when the accept loop exits. Deliberate teardown removes the
	// session from m.sessions before closing the backend, so a session still
	// published in the map whose loop has exited is one whose relay died under us
	// — the provider dropped the tunnel, its quota lapsed, the network went away.
	// status reports those as inactive instead of handing back a URL that no
	// longer answers.
	dead atomic.Bool

	// cleanupMu guards cleanup/torn: state a tunnel published elsewhere and must
	// withdraw when it stops (an ingress tunnel's host alias, say). Registering is
	// racy by nature — the URL a cleanup depends on is only known after the session
	// is already published — so addCleanup runs the func immediately if teardown
	// already happened, which is what keeps a stop/start race from stranding it.
	cleanupMu sync.Mutex
	cleanup   []func()
	torn      bool
}

// addCleanup registers work to run when the session is torn down, or runs it now
// if teardown already happened.
func (ts *tunnelSession) addCleanup(fn func()) {
	if fn == nil {
		return
	}
	ts.cleanupMu.Lock()
	if ts.torn {
		ts.cleanupMu.Unlock()
		fn()
		return
	}
	ts.cleanup = append(ts.cleanup, fn)
	ts.cleanupMu.Unlock()
}

// runCleanups executes and clears the registered cleanups exactly once.
func (ts *tunnelSession) runCleanups() {
	ts.cleanupMu.Lock()
	fns := ts.cleanup
	ts.cleanup, ts.torn = nil, true
	ts.cleanupMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// bridgeFunc bridges one inbound tunnel connection to whatever the tunnel
// fronts: a workload port for a deployment tunnel, the ingress front door for an
// ingress tunnel. It takes ownership of conn.
type bridgeFunc func(ctx context.Context, conn net.Conn)

// newTunnelManager opens the configured backend. An explicitly configured but
// unknown backend (CORNUS_TUNNEL_BACKEND set to a bad name) is a hard startup
// error — fail closed, consistent with the rest of the server's config. The
// default backend failing to open (e.g. its package was not linked into a test
// binary) is deferred: the manager reports it only if the tunnel endpoint is
// actually used, so it never blocks server startup.
func newTunnelManager(backend, defaultToken string) (*tunnelManager, error) {
	explicit := backend != ""
	name := backend
	if name == "" {
		name = tunnel.DefaultBackend
	}
	m := &tunnelManager{name: name, defaultToken: defaultToken, sessions: map[string]*tunnelSession{}}
	b, err := tunnel.Open(name)
	if err != nil {
		if explicit {
			return nil, fmt.Errorf("tunnel backend: %w", err)
		}
		m.openErr = err
		return m, nil
	}
	m.backend = b
	return m, nil
}

// start hosts (or replaces) the tunnel for a deployment, bridging inbound
// connections to port inside it via backend. token is the already-resolved
// credential. It is a thin instantiation of startSession whose bridge is the
// same byte-bridge `cornus port-forward` uses, so it reaches ports the workload
// never published, on any backend.
func (m *tunnelManager) start(backend deploy.Backend, name, token string, ag agent.Agent, port int, proto string) (api.TunnelStatus, error) {
	ts := &tunnelSession{port: port}
	cred := tunnel.Credential{AuthToken: token, Agent: ag}
	opts := tunnel.Options{Proto: proto, Metadata: "cornus deployment " + name, Hostname: name}
	bridge := func(ctx context.Context, conn net.Conn) {
		if err := backend.ForwardPort(ctx, name, port, "tcp", conn); err != nil {
			logging.FromContext(ctx).DebugContext(ctx, "tunnel connection ended", "deployment", name, "port", port, "error", err)
		}
	}
	if err := m.startSession(name, ts, cred, opts, bridge); err != nil {
		return api.TunnelStatus{}, err
	}
	return api.TunnelStatus{Active: true, URL: ts.url, Port: port}, nil
}

// startSession hosts (or replaces) the tunnel registered under key, bridging
// every inbound connection with bridge. The caller supplies ts carrying whatever
// metadata its status needs; startSession fills in the live session fields.
//
// It dispatches on the backend model: a listener-model Provider hands
// connections back via Session.Accept (the efficient path, no extra local hop);
// an upstream-model UpstreamProvider only forwards to a local URL, so the
// manager stands up a loopback shim listener here and points the backend at it.
// The session uses a background context (not the request's) so the tunnel
// outlives the HTTP request that created it.
func (m *tunnelManager) startSession(key string, ts *tunnelSession, cred tunnel.Credential, opts tunnel.Options, bridge bridgeFunc) error {
	if m.backend == nil {
		if m.openErr != nil {
			return fmt.Errorf("tunnel backend %q unavailable: %w", m.name, m.openErr)
		}
		return fmt.Errorf("tunnel backend %q unavailable", m.name)
	}

	sctx, cancel := context.WithCancel(context.Background())
	ts.cancel = cancel
	var accept func() (net.Conn, error)

	switch p := m.backend.(type) {
	case tunnel.Provider:
		sess, err := p.Start(sctx, cred, opts)
		if err != nil {
			cancel()
			return err
		}
		ts.url = sess.URL()
		ts.closeFn = sess.Close
		accept = sess.Accept

	case tunnel.UpstreamProvider:
		// The backend only forwards to a local upstream, so stand up a shim
		// listener carrying the bridge and point the backend at it.
		shim, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			cancel()
			return err
		}
		usess, err := p.StartUpstream(sctx, cred, opts, "http://"+shim.Addr().String())
		if err != nil {
			_ = shim.Close()
			cancel()
			return err
		}
		ts.url = usess.URL()
		ts.closeFn = func() error {
			err := usess.Close()
			if e := shim.Close(); err == nil {
				err = e
			}
			return err
		}
		accept = shim.Accept

	default:
		cancel()
		return fmt.Errorf("tunnel backend %q has unsupported type %T", m.name, m.backend)
	}

	// Launch the accept loop as a panic-isolated child BEFORE publishing ts, so a
	// concurrent stop/teardown that grabs ts from the map always finds a complete
	// session (its token already set). The policy is RemoveOnExit, not Restart:
	// Accept() blocks on I/O and ignores context, so the loop's normal exit is
	// Accept erroring when teardown closes the backend — a Restart policy would
	// busy-loop re-accepting on the dead backend until Remove cancels it. What
	// supervision buys here is the one property that matters for a resource-bounded
	// loop: a panic in the accept path is recovered instead of crashing the whole
	// server process, after which the loop exits cleanly.
	ts.tok = m.serveSupervisor().AddSystem("tunnel:"+key, supervisor.ServiceFunc(func(context.Context) error {
		m.serve(sctx, ts, accept, bridge)
		return nil
	}), supervisor.RemoveOnExit)

	// Publish the new session and atomically adopt any session it replaces (a
	// prior tunnel, or a concurrent start of the same name) for teardown. Holding
	// m.mu across this check-and-set is what keeps two overlapping starts from both
	// writing the map and orphaning the loser — its live relay endpoint and serve
	// goroutine would otherwise leak, reachable by neither stop nor closeAll.
	m.mu.Lock()
	old := m.sessions[key]
	m.sessions[key] = ts
	m.mu.Unlock()

	if old != nil {
		m.teardown(sctx, key, old)
	}
	logging.FromContext(sctx).InfoContext(sctx, "tunnel started", "tunnel", key, "url", ts.url, "backend", m.name)
	return nil
}

// serve accepts inbound tunnel connections (via accept) and hands each to bridge
// on its own goroutine, until the tunnel is closed (accept errors). It runs as a
// supervised child (see startSession): its completion is tracked by the manager's
// supervisor token, so it no longer signals a hand-rolled done channel; a panic
// in this loop is recovered by the supervisor rather than killing the server
// process. On exit it marks the session dead, which is how a relay that died on
// its own stops being reported as active.
func (m *tunnelManager) serve(ctx context.Context, ts *tunnelSession, accept func() (net.Conn, error), bridge bridgeFunc) {
	defer ts.dead.Store(true)
	for {
		conn, err := accept()
		if err != nil {
			return
		}
		go bridge(ctx, conn)
	}
}

// stop tears down the tunnel registered under key if one exists. It is
// idempotent and waits for the accept loop to exit so teardown is synchronous.
func (m *tunnelManager) stop(key string) {
	m.mu.Lock()
	ts := m.sessions[key]
	delete(m.sessions, key)
	m.mu.Unlock()
	m.teardown(context.Background(), key, ts)
}

// teardown closes one specific session (its backend tunnel and any shim
// listener), severs in-flight bridges, and waits for its accept loop to exit. It
// does NOT touch m.sessions, so it can safely tear down a session that has
// already been replaced in the map (see start's atomic replace). Nil-safe.
func (m *tunnelManager) teardown(ctx context.Context, key string, ts *tunnelSession) {
	if ts == nil {
		return
	}
	ts.runCleanups() // withdraw anything this session published elsewhere
	if ts.closeFn != nil {
		_ = ts.closeFn() // unblocks the accept loop: Accept errors on a closed backend
	}
	ts.cancel() // sever in-flight ForwardPort bridges
	if ts.tok != nil {
		// Wait for the accept loop to drain. Remove is idempotent and safe even
		// when the RemoveOnExit child has already self-exited on the Accept error
		// (or a recovered panic): it returns as soon as the child is gone.
		m.serveSupervisor().Remove(ts.tok)
	}
	logging.FromContext(ctx).InfoContext(ctx, "tunnel stopped", "tunnel", key, "backend", m.name)
}

// credentialOptional reports whether the backend can host without an injected
// credential (e.g. Cloudflare quick tunnels).
func (m *tunnelManager) credentialOptional() bool {
	if co, ok := m.backend.(tunnel.CredentialOptional); ok {
		return co.CredentialOptional()
	}
	return false
}

// status reports the current tunnel state for key. A session whose accept loop
// has exited reads as inactive: its relay is gone, so reporting its URL would
// point the caller at an endpoint that no longer answers.
func (m *tunnelManager) status(key string) api.TunnelStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	ts := m.sessions[key]
	if ts == nil || ts.dead.Load() {
		return api.TunnelStatus{Active: false}
	}
	return api.TunnelStatus{Active: true, URL: ts.url, Port: ts.port}
}

// closeAll tears down every live tunnel (server shutdown).
func (m *tunnelManager) closeAll() {
	m.mu.Lock()
	keys := make([]string, 0, len(m.sessions))
	for k := range m.sessions {
		keys = append(keys, k)
	}
	m.mu.Unlock()
	for _, k := range keys {
		m.stop(k)
	}
	// Every stop above already drained its own accept loop (teardown → Remove), so
	// the supervisor holds no children now; cancel its root context to release the
	// tree. Reset so a manager reused after closeAll (only tests) rebuilds cleanly.
	m.supMu.Lock()
	if m.supCancel != nil {
		m.supCancel()
		m.supCancel = nil
		m.sup = nil
	}
	m.supMu.Unlock()
}

// handleDeployTunnel serves /.cornus/v1/deploy/{name}/tunnel:
//
//   - POST  {authToken, port, proto} → host a tunnel, respond TunnelStatus{url}
//   - GET                            → current TunnelStatus
//   - DELETE                         → stop the tunnel
//
// It is gated on the `deploy` API-policy action by the dispatch in
// handleDeployItem (a tunnel exposes a workload to the public internet, a
// deploy-level operation), and rides the server's existing auth middleware, so
// the credential is injected only over an authenticated channel.
func (s *Server) handleDeployTunnel(w http.ResponseWriter, r *http.Request, backend deploy.Backend, name string) {
	switch r.Method {
	case http.MethodPost:
		var req api.TunnelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tunnel request: " + err.Error()})
			return
		}
		if req.Port < 1 || req.Port > 65535 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "port out of range (1-65535)"})
			return
		}
		if req.ForwardAgent && s.tunnels.name != "ssh" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "forwardAgent is only supported by the ssh tunnel backend"})
			return
		}
		token := req.AuthToken
		if token == "" {
			token = s.tunnels.defaultToken
		}
		if token == "" && !req.ForwardAgent && !s.tunnels.credentialOptional() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no tunnel authtoken: pass --authtoken (or set CORNUS_TUNNEL_AUTHTOKEN on the server)"})
			return
		}
		var ag agent.Agent
		if req.ForwardAgent {
			conn, release, ok := s.tunnels.waitForChannel(r.Context(), name, "ssh-agent")
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "forwardAgent requested but no ssh-agent channel arrived from the client in time"})
				return
			}
			defer release()
			ag = agent.NewClient(conn)
		}
		st, err := s.tunnels.start(backend, name, token, ag, req.Port, req.Proto)
		if err != nil {
			// A start failure is an upstream-relay problem (bad token, quota,
			// unreachable service), not a client request error.
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "starting tunnel: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, st)

	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.tunnels.status(name))

	case http.MethodDelete:
		s.tunnels.stop(name)
		writeJSON(w, http.StatusOK, api.TunnelStatus{Active: false})

	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleDeployTunnelChannel serves GET /.cornus/v1/deploy/{name}/tunnel/channel/{purpose}:
// it upgrades to a raw byte-stream WebSocket and registers the connection as a
// side-channel for the next matching tunnel-start (POST .../tunnel with
// ForwardAgent: true) to claim, keyed by name and purpose. It is a small,
// deliberately generic mechanism — the CLI dials it before issuing the
// tunnel-start request, exactly like the exec/attach/port-forward tunnels dial
// their own endpoint first.
//
// Only the "ssh-agent" purpose is recognized today (forwarding the caller's
// local ssh-agent to the ssh backend's outbound handshake); a future feature
// can add another purpose to this same endpoint without a new protocol.
//
// It rides the same auth/policy gate as the tunnel endpoint itself (see
// handleDeployItem's dispatch), since opening a channel is only useful as a
// prelude to hosting a tunnel.
func (s *Server) handleDeployTunnelChannel(w http.ResponseWriter, r *http.Request, backend deploy.Backend, name, purpose string) {
	if purpose != "ssh-agent" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown tunnel channel purpose: " + purpose})
		return
	}
	conn, err := wire.AcceptConn(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	s.tunnels.registerChannel(r.Context(), name, purpose, conn)
}
