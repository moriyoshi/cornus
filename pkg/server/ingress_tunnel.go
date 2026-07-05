package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh/agent"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/ingressroute"
	"cornus/pkg/logging"
	"cornus/pkg/tunnel"
	"cornus/pkg/wire"
)

// An ingress tunnel is scoped to a project or a single deployment, never to a
// port, so its session key is prefixed to keep it out of the deployment-tunnel
// namespace. Deployment names cannot contain "/", so the two can never collide.
const (
	ingressTunnelProjectPrefix    = "ingress/project/"
	ingressTunnelDeploymentPrefix = "ingress/deployment/"
)

// ingressTunnelScope identifies what an ingress tunnel fronts.
type ingressTunnelScope struct {
	project    string
	deployment string
}

func (s ingressTunnelScope) key() string {
	if s.project != "" {
		return ingressTunnelProjectPrefix + s.project
	}
	return ingressTunnelDeploymentPrefix + s.deployment
}

func (s ingressTunnelScope) String() string {
	if s.project != "" {
		return "project/" + s.project
	}
	return "deployment/" + s.deployment
}

// scopeFromRequest reads the project/deployment selector from a query string.
func scopeFromRequest(r *http.Request) (ingressTunnelScope, error) {
	q := r.URL.Query()
	return newIngressTunnelScope(q.Get("project"), q.Get("deployment"))
}

func newIngressTunnelScope(project, deployment string) (ingressTunnelScope, error) {
	project, deployment = strings.TrimSpace(project), strings.TrimSpace(deployment)
	switch {
	case project != "" && deployment != "":
		return ingressTunnelScope{}, fmt.Errorf("give either project or deployment, not both")
	case project != "":
		return ingressTunnelScope{project: project}, nil
	case deployment != "":
		return ingressTunnelScope{deployment: deployment}, nil
	default:
		return ingressTunnelScope{}, fmt.Errorf("no scope: pass project or deployment")
	}
}

// ingressFront is what an ingress tunnel hands its accepted connections to: a
// real cluster ingress controller, or the server's own mux.
type ingressFront struct {
	// dial reaches a real ingress controller, nil when there is none.
	dial func(ctx context.Context, https bool) (net.Conn, error)
	// handler serves the server-side routing table, nil when dial is used.
	handler http.Handler
	// hosts are the ingress hostnames in scope.
	hosts []string
	// target names the front for status: "controller" or "mux".
	target string
}

// resolveIngressFront decides what a tunnel for scope should front.
//
// A real cluster controller wins whenever one is reachable: it already does the
// Host/path routing and TLS termination the mux would have to emulate, and a raw
// TCP tunnel into it carries the client's SNI and Host through untouched. The
// server-side mux is the fallback — every host backend, and a cluster with no
// discoverable controller.
func (s *Server) resolveIngressFront(ctx context.Context, backend deploy.Backend, scope ingressTunnelScope, raw bool) (ingressFront, error) {
	hosts, err := s.ingressHostsFor(ctx, backend, scope)
	if err != nil {
		return ingressFront{}, err
	}
	if len(hosts) == 0 {
		return ingressFront{}, fmt.Errorf("no ingress found for %s: deploy with an ingress first (x-cornus-ingress / ingress in the deploy spec)", scope)
	}

	if gw, ok := backend.(deploy.IngressGateway); ok {
		// Probe once: a controller that cannot be dialled now is one the tunnel
		// would fail on for every visitor, so fall back rather than publish a URL
		// that answers nothing. Probe the SAME port the bridge will use — a
		// controller serving HTTP but not HTTPS (or the reverse) would otherwise
		// pass the probe and fail on every real connection.
		if conn, err := gw.DialIngress(ctx, raw); err == nil {
			_ = conn.Close()
			return ingressFront{
				dial:   gw.DialIngress,
				hosts:  hosts,
				target: "controller",
			}, nil
		} else {
			logging.FromContext(ctx).InfoContext(ctx,
				"no reachable ingress controller; the tunnel will front the server's own ingress mux", "error", err)
		}
	}

	return ingressFront{
		handler: s.lazyIngressHandler(),
		hosts:   hosts,
		target:  "mux",
	}, nil
}

// ingressHostsFor collects the ingress hostnames in scope: from the live cluster
// Ingress objects where the backend reports them, else from the server's own
// routing table.
func (s *Server) ingressHostsFor(ctx context.Context, backend deploy.Backend, scope ingressTunnelScope) ([]string, error) {
	seen := map[string]bool{}
	var hosts []string
	add := func(hs []string) {
		for _, h := range hs {
			if h = ingressroute.CanonicalHost(h); h != "" && !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}

	workloads, err := s.workloadsInScope(ctx, backend, scope)
	if err != nil {
		return nil, err
	}
	for _, workload := range workloads {
		if gw, ok := backend.(deploy.IngressGateway); ok {
			if hs, err := gw.IngressHosts(ctx, workload); err == nil && len(hs) > 0 {
				add(hs)
				continue
			}
		}
		add(s.ingress.hostsFor(workload))
	}
	sort.Strings(hosts)
	return hosts, nil
}

// workloadsInScope lists the deployment names an ingress tunnel covers. A
// deployment scope is itself; a project scope is every deployment the backend
// reports as belonging to that project.
func (s *Server) workloadsInScope(ctx context.Context, backend deploy.Backend, scope ingressTunnelScope) ([]string, error) {
	if scope.deployment != "" {
		return []string{scope.deployment}, nil
	}
	statuses, err := backend.List(ctx)
	if err != nil {
		// Distinct from "the project declares no ingress": telling someone to
		// declare one when the real problem was an unreachable daemon sends them
		// off fixing the wrong thing.
		return nil, fmt.Errorf("listing deployments for %s: %w", scope, err)
	}
	var names []string
	for _, st := range statuses {
		if st.Origin != nil && st.Origin.Project == scope.project {
			names = append(names, st.Name)
		}
	}
	return names, nil
}

// startIngressTunnel hosts (or replaces) the ingress tunnel for scope.
func (s *Server) startIngressTunnel(ctx context.Context, backend deploy.Backend, scope ingressTunnelScope, req api.IngressTunnelRequest, token string, ag agent.Agent) (api.IngressTunnelStatus, error) {
	front, err := s.resolveIngressFront(ctx, backend, scope, isRawProto(req.Proto))
	if err != nil {
		return api.IngressTunnelStatus{}, err
	}

	hostMode := req.HostMode
	if hostMode == "" {
		hostMode = api.HostModeAuto
	}
	// The host the tunnel fronts: the caller's choice, or the only one in scope.
	host := ingressroute.CanonicalHost(req.Host)
	if host == "" {
		host = front.hosts[0]
	} else if !slicesContains(front.hosts, host) {
		return api.IngressTunnelStatus{}, fmt.Errorf("host %q is not among the ingress hosts in %s (%s)", host, scope, strings.Join(front.hosts, ", "))
	}
	if hostMode == api.HostModeRewrite && len(front.hosts) > 1 && req.Host == "" {
		return api.IngressTunnelStatus{}, fmt.Errorf("%s serves %d ingress hosts; pass a host to say which one to rewrite to (%s)", scope, len(front.hosts), strings.Join(front.hosts, ", "))
	}

	ts := &tunnelSession{}
	cred := tunnel.Credential{AuthToken: token, Agent: ag}
	opts := tunnel.Options{
		Proto:    req.Proto,
		Metadata: "cornus ingress " + scope.String(),
		Hostname: strings.ReplaceAll(scope.String(), "/", "-"),
		Domain:   host, // best-effort: a provider that can grant it makes passthrough exact
	}

	bridge, resolvedMode, err := s.ingressBridge(front, host, hostMode, req.Proto)
	if err != nil {
		return api.IngressTunnelStatus{}, err
	}
	if err := s.tunnels.startSession(scope.key(), ts, cred, opts, bridge); err != nil {
		return api.IngressTunnelStatus{}, err
	}

	// With the public URL now known, settle auto mode and publish the alias that
	// makes the tunnel hostname route. The alias is session state, so it is
	// withdrawn on teardown — otherwise it outlives the tunnel and keeps routing a
	// hostname nobody is serving any more.
	resolvedMode = s.settleHostMode(ts, ts.url, host, resolvedMode, front.target)

	return api.IngressTunnelStatus{
		Active:   true,
		URL:      ts.url,
		Scope:    scope.String(),
		Hosts:    front.hosts,
		HostMode: resolvedMode,
		Target:   front.target,
	}, nil
}

// ingressBridge builds the per-connection bridge for the resolved front.
//
// A controller front is a raw splice: the controller does the routing, so the
// bytes must reach it unmodified. The mux front terminates HTTP so it can read
// the Host header it routes on — except in rewrite mode, where it also replaces
// that header first.
func (s *Server) ingressBridge(front ingressFront, host, hostMode, proto string) (bridgeFunc, string, error) {
	// A raw tunnel hands us the client's bytes untouched, TLS included. That is
	// the only way to get end-to-end TLS — but only a real controller can
	// terminate it, so which front we have decides whether the proto is usable at
	// all.
	raw := isRawProto(proto)

	if front.dial != nil {
		if hostMode == api.HostModeRewrite {
			return nil, "", fmt.Errorf("host rewriting is not available in front of a real ingress controller: the tunnel is a raw byte stream there (use --host-mode passthrough, or a tunnel backend that can grant the ingress hostname)")
		}
		// For a raw tunnel the client's TLS must reach the controller intact, so
		// target its HTTPS port and let SNI through. For an http tunnel the
		// provider already terminated TLS at its edge, so the leg into the cluster
		// is the controller's plain HTTP port.
		return func(ctx context.Context, conn net.Conn) {
			upstream, err := front.dial(ctx, raw)
			if err != nil {
				logging.FromContext(ctx).WarnContext(ctx, "ingress tunnel: could not reach the ingress controller", "error", err)
				_ = conn.Close()
				return
			}
			if err := deploy.Bridge(conn, upstream); err != nil {
				logging.FromContext(ctx).DebugContext(ctx, "ingress tunnel connection ended", "error", err)
			}
		}, api.HostModePassthrough, nil
	}

	// The mux terminates HTTP itself and has no TLS identity to serve, so it
	// cannot answer a raw tunnel carrying TLS. Refuse rather than accept the flag
	// and hand back a URL that fails the moment anyone uses https.
	if raw {
		return nil, "", fmt.Errorf("--proto tcp needs a real ingress controller to terminate TLS; this server routes the ingress itself, which speaks plain HTTP (use --proto http, or deploy to a cluster with an ingress controller)")
	}

	handler := front.handler
	if hostMode == api.HostModeRewrite {
		inner := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Host = host
			r.Header.Set("X-Forwarded-Host", r.Host)
			inner.ServeHTTP(w, r)
		})
	}
	return httpBridge(handler), hostMode, nil
}

// settleHostMode resolves auto mode now that the public URL is known, and wires
// the alias that lets a tunnel hostname route to the ingress host.
func (s *Server) settleHostMode(ts *tunnelSession, publicURL, host, mode, target string) string {
	tunnelHost := hostOfURL(publicURL)
	if mode == api.HostModeAuto {
		// The provider granted the ingress hostname itself: nothing to adjust.
		if tunnelHost != "" && ingressroute.CanonicalHost(tunnelHost) == host {
			mode = api.HostModePassthrough
		} else {
			mode = api.HostModeAlias
		}
	}
	// Aliasing is a routing-table concept, so it only applies to the mux front; a
	// real controller routes on its own rules and we must not touch its request.
	if target == "mux" && mode == api.HostModeAlias && tunnelHost != "" {
		routes := s.ingress.routes()
		routes.HostAlias(tunnelHost, host)
		ts.addCleanup(func() { routes.HostAlias(tunnelHost, "") })
	}
	return mode
}

// hostOfURL extracts the hostname from a tunnel's public URL. Providers report
// "https://abc.ngrok.app" for http tunnels and "tcp://host:port" for raw ones.
func hostOfURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

// httpBridge turns an http.Handler into a tunnel bridge by serving each accepted
// connection as a one-connection HTTP server. The provider's edge already speaks
// HTTP to us, so this is the natural shape; keep-alives and WebSocket upgrades
// work because it is a real http.Server.
//
// The listener is closed when the connection is — that is what lets Serve return
// and this goroutine exit. Without it Serve blocks in Accept forever and every
// inbound tunnel connection leaks a goroutine plus an http.Server for the life of
// the process.
func httpBridge(h http.Handler) bridgeFunc {
	return func(ctx context.Context, conn net.Conn) {
		srv := &http.Server{
			Handler:     h,
			BaseContext: func(net.Listener) context.Context { return ctx },
			// A tunnel endpoint is reachable by anyone with the URL, so bound how
			// long a peer may hold a connection without making progress.
			ReadHeaderTimeout: 30 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		lis := newSingleConnListener()
		// http.Server closes the connection when it is done with it (the handler
		// finished a Connection: close request, the idle timeout elapsed, or the
		// peer went away); closing the listener in turn unblocks Serve.
		wrapped := &closeNotifyConn{Conn: conn, notify: func() { _ = lis.Close() }}
		// Tearing the tunnel down cancels the session context. Close the connection
		// on that signal too: http.Server does not watch a context, so without this
		// an idle keep-alive connection would hold this goroutine until IdleTimeout
		// long after the tunnel it belongs to is gone.
		stopWatch := context.AfterFunc(ctx, func() { _ = wrapped.Close() })
		defer stopWatch()

		lis.serve(wrapped)
		_ = srv.Serve(lis)
		_ = srv.Close()
	}
}

// closeNotifyConn runs notify exactly once, the first time the connection is
// closed, so a listener wrapping a single connection can learn its lifetime is
// over.
type closeNotifyConn struct {
	net.Conn
	once   sync.Once
	notify func()
}

func (c *closeNotifyConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.notify)
	return err
}

// handleIngressTunnel serves /.cornus/v1/ingress-tunnel:
//
//   - POST   {authToken, project|deployment, hostMode, host, proto} -> host a tunnel
//   - GET    ?project=P | ?deployment=N                             -> its status
//   - DELETE ?project=P | ?deployment=N                             -> stop it
//
// It is gated on the tunnel action (which "deploy" implies), like the
// per-deployment tunnel endpoint: publishing a project to the internet is at
// least as consequential as deploying it.
func (s *Server) handleIngressTunnel(w http.ResponseWriter, r *http.Request) {
	if !s.apiPolicy.AllowTunnel(Identity(r)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to host tunnels"})
		return
	}
	backend, err := s.getBackend()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "deploy backend unavailable: " + err.Error()})
		return
	}

	switch r.Method {
	case http.MethodPost:
		var req api.IngressTunnelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ingress tunnel request: " + err.Error()})
			return
		}
		scope, err := newIngressTunnelScope(req.Project, req.Deployment)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		switch req.HostMode {
		case "", api.HostModeAuto, api.HostModePassthrough, api.HostModeAlias, api.HostModeRewrite:
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown hostMode %q (auto, passthrough, alias, rewrite)", req.HostMode)})
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
			conn, release, ok := s.tunnels.waitForChannel(r.Context(), scope.key(), "ssh-agent")
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "forwardAgent requested but no ssh-agent channel arrived from the client in time"})
				return
			}
			defer release()
			ag = agent.NewClient(conn)
		}
		st, err := s.startIngressTunnel(r.Context(), backend, scope, req, token, ag)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "starting ingress tunnel: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, st)

	case http.MethodGet:
		scope, err := scopeFromRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		base := s.tunnels.status(scope.key())
		writeJSON(w, http.StatusOK, api.IngressTunnelStatus{
			Active: base.Active,
			URL:    base.URL,
			Scope:  scope.String(),
		})

	case http.MethodDelete:
		scope, err := scopeFromRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.tunnels.stop(scope.key())
		writeJSON(w, http.StatusOK, api.IngressTunnelStatus{Active: false, Scope: scope.String()})

	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleIngressTunnelChannel serves the ssh-agent side-channel for an ingress
// tunnel, mirroring handleDeployTunnelChannel but keyed on the tunnel's scope.
func (s *Server) handleIngressTunnelChannel(w http.ResponseWriter, r *http.Request) {
	if !s.apiPolicy.AllowTunnel(Identity(r)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to host tunnels"})
		return
	}
	purpose := strings.TrimPrefix(r.URL.Path, "/.cornus/v1/ingress-tunnel/channel/")
	if purpose != "ssh-agent" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown tunnel channel purpose: " + purpose})
		return
	}
	scope, err := scopeFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	conn, err := wire.AcceptConn(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	s.tunnels.registerChannel(r.Context(), scope.key(), purpose, conn)
}

// isRawProto reports whether the requested tunnel protocol delivers an
// unmodified byte stream ("tcp") rather than HTTP the provider already parsed.
func isRawProto(proto string) bool {
	return strings.EqualFold(strings.TrimSpace(proto), "tcp")
}

func slicesContains(hs []string, want string) bool {
	for _, h := range hs {
		if h == want {
			return true
		}
	}
	return false
}

// singleConnListener presents one already-established connection to an
// http.Server and then blocks until closed. It is how a tunnel's accepted
// connection gets served by an http.Handler: the provider's edge already spoke
// HTTP to us, so a real http.Server is the right thing to put behind it — and it
// brings keep-alive and WebSocket-upgrade handling along for free.
type singleConnListener struct {
	conn net.Conn
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

// newSingleConnListener returns an unarmed listener. The caller arms it with
// serve once it has the connection to hand over — httpBridge wraps the raw
// connection first, precisely so the wrapper can close this listener when the
// connection ends, which is why the connection cannot be supplied here.
func newSingleConnListener() *singleConnListener {
	return &singleConnListener{ch: make(chan net.Conn, 1), done: make(chan struct{})}
}

// serve hands conn to the next Accept. It must be called before Serve, and only
// once.
func (l *singleConnListener) serve(conn net.Conn) {
	l.conn = conn
	l.ch <- conn
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// Close ends the accept loop. The connection itself is owned by the http.Server
// once accepted, so it is not closed here.
func (l *singleConnListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// advertisedTunnel reports the server's public-tunnel capability for
// /.cornus/v1/info, so a client (and `cornus setup`) can tell up front whether a
// tunnel request will work, and say which server-side knob to turn when it will
// not.
func (s *Server) advertisedTunnel() *api.TunnelInfo {
	if s.tunnels == nil {
		return nil
	}
	return &api.TunnelInfo{
		Backend:            s.tunnels.name,
		Available:          s.tunnels.backend != nil,
		CredentialRequired: s.tunnels.defaultToken == "" && !s.tunnels.credentialOptional(),
		SupportsIngress:    true,
	}
}

// fillIngressTunnelURL surfaces an active ingress tunnel's public URL on a
// deployment's status, so `cornus ps` and the web UI show it without a second
// request. A deployment-scoped tunnel wins over its project's, being the more
// specific one; a URL the backend already reported (a Knative route) wins over
// both, being a property of the workload rather than of a session someone opened.
func (s *Server) fillIngressTunnelURL(status *api.DeployStatus) {
	if status == nil || status.URL != "" || s.tunnels == nil {
		return
	}
	if st := s.tunnels.status(ingressTunnelScope{deployment: status.Name}.key()); st.Active {
		status.URL = st.URL
		return
	}
	if status.Origin != nil && status.Origin.Project != "" {
		if st := s.tunnels.status(ingressTunnelScope{project: status.Origin.Project}.key()); st.Active {
			status.URL = st.URL
		}
	}
}
