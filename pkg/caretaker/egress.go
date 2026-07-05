package caretaker

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/hashicorp/yamux"

	"cornus/pkg/api"
	"cornus/pkg/egresspolicy"
	"cornus/pkg/logging"
	"cornus/pkg/wire"
)

// EgressRole routes the app container's OUTBOUND connections through a client-side
// vantage point. In "proxy" mode the caretaker runs a forward proxy on loopback
// (both HTTP CONNECT and SOCKS5) that the app's proxy env vars point at; each
// accepted connection's destination is classified by the routing policy and, when
// it routes to a relay terminus (client/gateway), tunneled over the pod-scoped
// caretaker connection as a TagEgress stream the server bridges to the client (or a
// gateway). A "cluster" verdict is dialed directly from the pod; "deny" is dropped.
//
// SOCKS5 domainname requests (ATYP=3) are forwarded by NAME, i.e. socks5h
// semantics: the destination is resolved at the terminus, not in the pod — the
// correct behavior for an air-gapped pod that cannot resolve external names.
type EgressRole struct {
	// Server is the cornus server holding the deploy-attach session; it selects the
	// pod-scoped caretaker connection this role rides.
	Server string `json:"server"`
	// Session is the deploy-attach session id presented on each relay stream (the
	// unguessable capability tying this pod to its deployment).
	Session string `json:"session"`
	// Mode is "proxy" (default) or "transparent".
	Mode string `json:"mode,omitempty"`
	// ListenPort is the loopback port the forward proxy listens on.
	ListenPort int `json:"listenPort,omitempty"`
	// Rules / Script / Default are the routing policy (see egresspolicy). The
	// caretaker decides the route per connection; the server re-checks it.
	Rules   []api.EgressRule `json:"rules,omitempty"`
	Script  string           `json:"script,omitempty"`
	Default string           `json:"default,omitempty"`
	// SetupRedirect makes a transparent caretaker program the nftables redirect
	// itself (in the shared network namespace it runs in, with NET_ADMIN) before
	// listening. The host companion sets it; on Kubernetes a dedicated NET_ADMIN
	// init container programs the redirect instead, so it stays false there.
	SetupRedirect bool `json:"setupRedirect,omitempty"`
}

// policy compiles the role's routing policy.
func (r EgressRole) policy() (egresspolicy.Policy, error) {
	return egresspolicy.Compile(api.EgressSpec{Rules: r.Rules, Script: r.Script, Default: r.Default})
}

// egressReady reports whether the egress forward proxy is accepting connections
// on its loopback listen port — the readiness the sidecar's startup probe gates
// the app container on, so the app never starts (and never emits egress that would
// bypass a not-yet-bound proxy) until egress is usable. Proxy mode binds
// 127.0.0.1:port and transparent mode binds :port; both are reachable at
// 127.0.0.1:port, and either only binds once its server relay session is up, so a
// successful dial also implies the caretaker's server connection came up.
func egressReady(role EgressRole) error {
	if role.ListenPort == 0 {
		return nil
	}
	addr := fmt.Sprintf("127.0.0.1:%d", role.ListenPort)
	c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return fmt.Errorf("egress proxy not listening on %s: %w", addr, err)
	}
	c.Close()
	return nil
}

// runEgress runs the egress role over the pod-scoped session. mark, when non-zero,
// is stamped on the caretaker's own direct (cluster-route) dials so they escape the
// egress redirect. Only "proxy" mode is served here; "transparent" is handled by the
// redirect path.
func runEgress(ctx context.Context, sess *yamux.Session, role EgressRole, mark int) error {
	if role.Mode == "transparent" {
		return runEgressTransparent(ctx, sess, role, mark)
	}
	pol, err := role.policy()
	if err != nil {
		return fmt.Errorf("egress: policy: %w", err)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", role.ListenPort))
	if err != nil {
		return fmt.Errorf("egress: listen 127.0.0.1:%d: %w", role.ListenPort, err)
	}
	logging.FromContext(ctx).InfoContext(ctx, "caretaker egress proxy listening", "port", role.ListenPort, "mode", "proxy")
	return serveEgressProxy(ctx, ln, sess, role.Session, pol, mark)
}

// serveEgressProxy accepts forward-proxy connections on ln and relays each by
// route until ctx is done. Split out of runEgress so tests can drive it on an
// ephemeral listener.
func serveEgressProxy(ctx context.Context, ln net.Listener, sess *yamux.Session, session string, pol egresspolicy.Policy, mark int) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go handleEgressProxy(ctx, sess, session, c, pol, mark)
	}
}

// handleEgressProxy sniffs whether the accepted connection speaks SOCKS5 or HTTP
// CONNECT (a single buffered reader is shared with the splice so no bytes are lost)
// and dispatches accordingly.
func handleEgressProxy(ctx context.Context, sess *yamux.Session, session string, c net.Conn, pol egresspolicy.Policy, mark int) {
	defer c.Close()
	ctx = logging.WithAttrs(ctx, slog.Group("egress", slog.Any("remote", c.RemoteAddr())))
	log := logging.FromContext(ctx)
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		// Connection closed before any request — nothing to do (not a fault).
		return
	}
	app := &prefixConn{r: br, Conn: c}
	if first[0] == 0x05 {
		log.DebugContext(ctx, "accepted SOCKS5 connection")
		logEgressConn(ctx, handleSocks5(ctx, sess, session, app, br, pol, mark))
		return
	}
	log.DebugContext(ctx, "accepted HTTP connection")
	logEgressConn(ctx, handleHTTP(ctx, sess, session, app, br, pol, mark))
}

// errEgressDenied marks an egress connection dropped by policy (a deny verdict or an
// unroutable route) as opposed to an operational failure. The boundary logger uses
// it to log a denial at Debug and a real failure at Warn, so a deny-heavy policy
// does not flood the logs.
var errEgressDenied = errors.New("denied by policy")

// routeUpstream applies the policy to dest and opens the upstream connection: a
// relay stream for the client/gateway routes (bytes leave from the terminus) or a
// direct mark-exempt dial for the cluster route. It returns an error wrapping
// errEgressDenied for a deny verdict / unroutable route, or a context-enriched
// operational error (parse / policy / relay / dial). It does NOT log — the caller's
// per-connection boundary logs the returned error once. proto labels the inbound
// protocol for that context. A successful routing decision is traced at Debug.
func routeUpstream(ctx context.Context, sess *yamux.Session, session, dest string, pol egresspolicy.Policy, mark int, proto string) (net.Conn, error) {
	mt := metrics()
	log := logging.FromContext(ctx)
	host, portStr, err := net.SplitHostPort(dest)
	if err != nil {
		mt.egressConns.Add(ctx, 1, egressAttr("error", proto))
		return nil, fmt.Errorf("egress %s: parse destination %q: %w", proto, dest, err)
	}
	d := egresspolicy.Dest{Host: host, Proto: "tcp"}
	if ip := net.ParseIP(host); ip != nil {
		d.IP = ip
	}
	d.Port, _ = strconv.Atoi(portStr)
	route, err := pol.Route(d)
	if err != nil {
		mt.egressConns.Add(ctx, 1, egressAttr("error", proto))
		return nil, fmt.Errorf("egress %s -> %s: policy evaluation failed: %w", proto, dest, err)
	}
	switch route {
	case egresspolicy.RouteClient, egresspolicy.RouteGateway:
		up, err := wire.OpenEgress(sess, session, route, dest)
		if err != nil {
			mt.egressConns.Add(ctx, 1, egressAttr("error", proto))
			return nil, fmt.Errorf("egress %s -> %s (route %s): open relay stream: %w", proto, dest, route, err)
		}
		mt.egressConns.Add(ctx, 1, egressAttr(route, proto))
		log.DebugContext(ctx, "relaying to terminus", "via", proto, "route", route)
		return up, nil
	case egresspolicy.RouteCluster:
		up, err := markDialer(mark).Dial("tcp", dest)
		if err != nil {
			mt.egressConns.Add(ctx, 1, egressAttr("error", proto))
			return nil, fmt.Errorf("egress %s -> %s: direct cluster dial: %w", proto, dest, err)
		}
		mt.egressConns.Add(ctx, 1, egressAttr(egresspolicy.RouteCluster, proto))
		log.DebugContext(ctx, "dialing directly on the cluster network", "via", proto)
		return up, nil
	default:
		mt.egressConns.Add(ctx, 1, egressAttr(egresspolicy.RouteDeny, proto))
		return nil, fmt.Errorf("egress %s -> %s (route %s): %w", proto, dest, route, errEgressDenied)
	}
}

// logEgressConn is the per-connection boundary logger: a denial logs at Debug (a
// policy outcome, not a fault), any other error at Warn.
func logEgressConn(ctx context.Context, err error) {
	if err == nil {
		return
	}
	log := logging.FromContext(ctx)
	if errors.Is(err, errEgressDenied) {
		log.DebugContext(ctx, "connection denied", "error", err)
		return
	}
	log.WarnContext(ctx, "connection failed", "error", err)
}

// --- HTTP proxy (CONNECT tunnels + absolute-form forwarding) -----------------

// handleHTTP serves the app as an HTTP forward proxy on this connection. A CONNECT
// establishes a raw tunnel (for HTTPS); an absolute-form request (GET
// http://host/path) is forwarded to the origin per request — each request is
// routed independently, so pointing HTTP_PROXY/HTTPS_PROXY at http://<proxy> works
// for plain HTTP too. Keep-alive on the app side is honored; each forwarded request
// uses a fresh upstream so different hosts route correctly.
func handleHTTP(ctx context.Context, sess *yamux.Session, session string, app net.Conn, br *bufio.Reader, pol egresspolicy.Policy, mark int) error {
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return nil // end of the client's requests (EOF / hangup) — not a fault
		}
		if req.Method == http.MethodConnect {
			return handleConnect(ctx, sess, session, app, req, pol, mark) // CONNECT consumes the conn
		}
		keepAlive, err := forwardHTTP(ctx, sess, session, app, req, pol, mark)
		if err != nil {
			return err
		}
		if !keepAlive {
			return nil
		}
	}
}

// handleConnect tunnels a CONNECT request: it opens the upstream by route, writes
// the 200 (or 403 on deny), and splices. It returns the routing error (if any) for
// the boundary to log.
func handleConnect(ctx context.Context, sess *yamux.Session, session string, app net.Conn, req *http.Request, pol egresspolicy.Policy, mark int) error {
	dest := req.Host
	if _, _, err := net.SplitHostPort(dest); err != nil {
		dest = net.JoinHostPort(dest, "443") // CONNECT without a port defaults to 443
	}
	ctx = logging.WithAttrs(ctx, slog.Group("egress", slog.String("dest", dest)))
	up, err := routeUpstream(ctx, sess, session, dest, pol, mark, "http-connect")
	if err != nil {
		_, _ = io.WriteString(app, "HTTP/1.1 403 Forbidden\r\n\r\n")
		return err
	}
	defer up.Close()
	_, _ = io.WriteString(app, "HTTP/1.1 200 Connection Established\r\n\r\n")
	ab, ba := spliceBidir(app, up)
	recordEgressBytes(metrics(), ab, ba)
	logging.FromContext(ctx).DebugContext(ctx, "CONNECT tunnel closed", "sent", ab, "recv", ba)
	return nil
}

// forwardHTTP forwards one absolute-form request to its origin and copies the
// response back. It returns whether the app connection may be reused (keep-alive)
// and a context-enriched error (if any) for the boundary to log.
func forwardHTTP(ctx context.Context, sess *yamux.Session, session string, app net.Conn, req *http.Request, pol egresspolicy.Policy, mark int) (keepAlive bool, err error) {
	dest := req.Host
	if dest == "" {
		dest = req.URL.Host
	}
	if _, _, e := net.SplitHostPort(dest); e != nil {
		dest = net.JoinHostPort(dest, "80") // plain HTTP defaults to port 80
	}
	ctx = logging.WithAttrs(ctx, slog.Group("egress", slog.String("dest", dest)))
	up, err := routeUpstream(ctx, sess, session, dest, pol, mark, "http")
	if err != nil {
		_, _ = io.WriteString(app, "HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n")
		return false, err
	}
	defer up.Close()

	// Meter the forward path like CONNECT/SOCKS: request bytes to the upstream are
	// outbound, response bytes back to the app are inbound.
	outbound := &countWriter{w: up}
	inbound := &countWriter{w: app}

	// Forward in origin form (Request.Write emits the path, not the absolute URI)
	// after stripping hop-by-hop proxy headers.
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Connection")
	req.RequestURI = ""
	if err := req.Write(outbound); err != nil {
		return false, fmt.Errorf("egress http -> %s: forward request to upstream: %w", dest, err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(up), req)
	if err != nil {
		_, _ = io.WriteString(app, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n")
		return false, fmt.Errorf("egress http -> %s: read upstream response: %w", dest, err)
	}
	defer resp.Body.Close()
	logging.FromContext(ctx).DebugContext(ctx, "forwarded HTTP request", "method", req.Method, "path", req.URL.Path, "status", resp.StatusCode)
	// One upstream per request: a fresh upstream is used per request so keep-alive on
	// the app side still routes different hosts correctly.
	reuse := !req.Close && !resp.Close
	if err := resp.Write(inbound); err != nil {
		recordEgressBytes(metrics(), outbound.n, inbound.n)
		return false, fmt.Errorf("egress http -> %s: write response to app: %w", dest, err)
	}
	recordEgressBytes(metrics(), outbound.n, inbound.n)
	return reuse, nil
}

// countWriter counts the bytes written through it, so the per-request HTTP forward
// path can be byte-metered like the spliced CONNECT/SOCKS paths.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// --- SOCKS5 (RFC 1928, no auth) ---------------------------------------------

func handleSocks5(ctx context.Context, sess *yamux.Session, session string, app net.Conn, br *bufio.Reader, pol egresspolicy.Policy, mark int) error {
	// Greeting: VER NMETHODS METHODS... A truncated/hung-up handshake is not a fault.
	ver, err := br.ReadByte()
	if err != nil || ver != 0x05 {
		return nil
	}
	nMethods, err := br.ReadByte()
	if err != nil {
		return nil
	}
	if _, err := io.CopyN(io.Discard, br, int64(nMethods)); err != nil {
		return nil
	}
	// Reply: no authentication required.
	if _, err := app.Write([]byte{0x05, 0x00}); err != nil {
		return nil
	}
	// Request: VER CMD RSV ATYP DST.ADDR DST.PORT
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return nil
	}
	if hdr[0] != 0x05 || hdr[1] != 0x01 { // only CONNECT
		socksReply(app, 0x07) // command not supported
		return fmt.Errorf("egress socks5: unsupported command %d", hdr[1])
	}
	host, err := readSocksAddr(br, hdr[3])
	if err != nil {
		socksReply(app, 0x08) // address type not supported
		return fmt.Errorf("egress socks5: %w", err)
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(br, portBuf[:]); err != nil {
		return nil
	}
	dest := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBuf[:]))))
	ctx = logging.WithAttrs(ctx, slog.Group("egress", slog.String("dest", dest)))
	up, err := routeUpstream(ctx, sess, session, dest, pol, mark, "socks5")
	if err != nil {
		socksReply(app, 0x02) // connection not allowed by ruleset
		return err
	}
	defer up.Close()
	socksReply(app, 0x00) // succeeded
	ab, ba := spliceBidir(app, up)
	recordEgressBytes(metrics(), ab, ba)
	logging.FromContext(ctx).DebugContext(ctx, "SOCKS5 tunnel closed", "sent", ab, "recv", ba)
	return nil
}

// readSocksAddr reads DST.ADDR for the given ATYP. A domainname (0x03) is returned
// as the literal name (socks5h: resolved at the terminus, not here).
func readSocksAddr(r *bufio.Reader, atyp byte) (string, error) {
	switch atyp {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case 0x03: // domainname
		n, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		b := make([]byte, int(n))
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("unsupported ATYP %d", atyp)
	}
}

// socksReply writes a SOCKS5 reply with the given status and a zero BND.ADDR/PORT.
func socksReply(c net.Conn, status byte) {
	_, _ = c.Write([]byte{0x05, status, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
}

// prefixConn is a net.Conn whose reads come from r (a buffered reader that may hold
// bytes already peeked/read from the underlying conn), so a protocol sniff does not
// lose buffered application bytes when the connection is later spliced.
type prefixConn struct {
	r io.Reader
	net.Conn
}

func (p *prefixConn) Read(b []byte) (int, error) { return p.r.Read(b) }
