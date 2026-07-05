package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"cornus/pkg/hub"
	"cornus/pkg/wire"
)

// hubConn is the per-connection state for a spoke on the hub: its registry id and
// its declared identity (set from the control-stream registration, or from the
// verified mTLS peer cert when the connection presents one). Data-stream handlers
// wait for the identity before a policy check, since control and data streams are
// accepted concurrently.
type hubConn struct {
	id       string
	ready    chan struct{}
	once     sync.Once
	identity string
}

func newHubConn(id string) *hubConn { return &hubConn{id: id, ready: make(chan struct{})} }

// declare records the connection's identity once (the first declaration wins) and
// unblocks waiters.
func (c *hubConn) declare(identity string) {
	c.once.Do(func() { c.identity = identity; close(c.ready) })
}

// waitIdentity returns the declared identity, blocking up to timeout for it.
func (c *hubConn) waitIdentity(timeout time.Duration) (string, bool) {
	select {
	case <-c.ready:
		return c.identity, true
	case <-time.After(timeout):
		return "", false
	}
}

// identityNow returns the current identity without blocking ("" if not yet set).
func (c *hubConn) identityNow() string {
	select {
	case <-c.ready:
		return c.identity
	default:
		return ""
	}
}

// The hub spoke connection is served by the unified caretaker endpoint
// (caretaker_attach.go handleCaretakerUnified); the control/register and data/relay
// handlers below are shared by it. hubControl and hubRelay carry the hub protocol.

// hubControl reads service registrations from a spoke's control stream, records the
// connection's declared identity (for policy), and adds services to the registry
// under the connection id. A service with an Addr registers dial-direct (the hub
// dials it); one without registers for delivery over this spoke's mux (the hub opens
// an ingress stream and the spoke dials its local target). Entries are removed when
// the connection drops (the handler's deferred RemoveConn).
//
// A registration carrying the Watch capability additionally subscribes this
// control stream to catalog pushes: the hub writes a hub.CatalogUpdate frame back
// (server→spoke, the previously unused direction of the stream) with the current
// snapshot and again whenever the registered set changes. Only declared watchers
// are ever written to, so an old caretaker never receives an unexpected frame.
func (s *Server) hubControl(hc *hubConn, mux *yamux.Session, stream net.Conn) {
	defer stream.Close()
	dec := json.NewDecoder(stream)
	watching := false
	for {
		var reg hub.Registration
		if err := dec.Decode(&reg); err != nil {
			return
		}
		if reg.Identity != "" {
			hc.declare(reg.Identity) // no-op if an authenticated identity (mTLS CN or JWT sub) already won
		}
		identity := hc.identityNow()
		registered := false
		for _, svc := range reg.Services {
			if svc.Name == "" {
				continue
			}
			// Registration authorization: only an identity permitted to host the
			// name may register it (unconfigured register policy allows all).
			if s.policy != nil && !s.policy.AllowRegister(identity, svc.Name) {
				continue
			}
			if svc.Addr != "" {
				s.hub.Register(hc.id, svc.Name, svc.Addr, svc.Protocol)
			} else {
				s.hub.RegisterDeliver(hc.id, svc.Name, mux)
			}
			registered = true
		}
		if registered {
			s.catalogWatch().changed()
		}
		if reg.Watch && !watching {
			watching = true
			ch, cancel := s.catalogWatch().subscribe()
			defer cancel()
			// Writer: push each catalog snapshot as a frame. It exits when the
			// subscription is cancelled (channel closed) or the write fails; the
			// deferred stream.Close above unblocks a write stuck on a dead peer.
			go func() {
				enc := json.NewEncoder(stream)
				for names := range ch {
					if names == nil {
						names = []string{}
					}
					if err := enc.Encode(hub.CatalogUpdate{Services: names}); err != nil {
						return
					}
				}
			}()
		}
	}
}

// hubRelay bridges one data stream to a registered service. It reads the target
// name, enforces policy (caller identity → callee service) when one is configured,
// looks the service up, and either dials the registered address directly
// (dial-direct) or opens an ingress-delivery stream to the hosting spoke (delivery),
// then splices. An unknown name or a denied pair closes the stream (the spoke sees
// EOF).
func (s *Server) hubRelay(hc *hubConn, stream net.Conn) {
	defer stream.Close()
	name, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	if s.policy.ReachEnforced() {
		caller, ok := hc.waitIdentity(5 * time.Second)
		if !ok || !s.policy.Allow(caller, name) {
			return // deny: no/late identity, or not permitted
		}
	}
	tgt, ok := s.hub.Lookup(name)
	if !ok {
		return
	}
	// Remote delivery (multi-replica, distributed Store): the service is a delivery
	// hosted by a spoke connected to ANOTHER replica, which alone holds that spoke's
	// mux. Forward this relay to the owner replica's /.cornus/v1/hub/forward, which opens the
	// ingress stream to its spoke and splices; we splice the client stream to it. The
	// reach policy was already enforced above, so the owner trusts this authenticated
	// peer. The framed byte stream (TCP or UDP datagrams) passes straight through.
	if tgt.ForwardAddr != "" {
		fwd, err := s.dialHubForward(tgt.ForwardAddr, tgt.ForwardName)
		if err != nil {
			return
		}
		defer fwd.Close()
		wire.Pipe(stream, fwd)
		return
	}
	// UDP dial-direct: the hub opens a connected UDP socket to the target and
	// datagram-bridges it (the framed datagrams the source spoke sent become real
	// UDP writes, and replies are re-framed back). Delivery is byte-agnostic — the
	// already-framed datagrams pass through Pipe to the hosting spoke unchanged.
	if tgt.Addr != "" && tgt.Protocol == "udp" {
		up, err := net.Dial("udp", tgt.Addr)
		if err != nil {
			return
		}
		defer up.Close()
		wire.BridgeDatagram(stream, up)
		return
	}
	var up net.Conn
	switch {
	case tgt.Addr != "":
		up, err = net.Dial("tcp", tgt.Addr)
	case tgt.Mux != nil:
		up, err = hub.OpenDeliver(tgt.Mux, name)
	default:
		return
	}
	if err != nil {
		return
	}
	defer up.Close()
	wire.Pipe(stream, up)
}

// dialHubForward opens the inter-replica forward WebSocket to a peer replica's
// /.cornus/v1/hub/forward (forwardAddr is that replica's base URL, e.g. "ws://podIP:5000")
// and writes the service name line, returning the stream ready to be spliced.
func (s *Server) dialHubForward(forwardAddr, name string) (net.Conn, error) {
	return s.dialForward(context.Background(), forwardAddr, "/.cornus/v1/hub/forward", name)
}

// dialForward opens an inter-replica forward WebSocket to path on a peer replica
// (forwardAddr is that replica's base URL, e.g. "ws://podIP:5000") and writes the
// given header lines, returning the stream ready to be spliced. It is shared by
// the hub delivery forward (/.cornus/v1/hub/forward, one service-name line) and the
// mount-relay forward (/.cornus/v1/mount/forward, session then name). It carries this
// server's own full credential (forwardToken) as a bearer token so the peer's
// auth middleware accepts the inter-replica call; when auth is off the token is
// empty and no header is sent. A wss:// peer is verified against s.forwardTLS
// (CORNUS_HUB_FORWARD_CA appended to the system roots) when configured, else the
// system trust store alone (nil TLS config — the historical dial, byte-identical).
// ctx carries the caller's trace context: the global propagator injects a W3C
// traceparent header so the peer's otelhttp span on the forward endpoint links
// to this side's relay span (a no-op unless observability is enabled). ctx also
// bounds the dial; it never cancels the returned stream.
func (s *Server) dialForward(ctx context.Context, forwardAddr, path string, lines ...string) (net.Conn, error) {
	header := http.Header{}
	if s.forwardToken != "" {
		header.Set("Authorization", "Bearer "+s.forwardToken)
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
	if len(header) == 0 {
		header = nil
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := wire.DialConnControlHeaderTLS(dctx, forwardAddr+path, nil, header, s.forwardTLS)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(conn, strings.Join(lines, "\n")+"\n"); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// loadHubForwardTLS builds the inter-replica forward dial's TLS config from
// CORNUS_HUB_FORWARD_CA: a path to a PEM CA bundle trusted (in addition to the
// system roots) for wss:// /.cornus/v1/hub/forward peers — e.g. replicas behind an
// internal CA. Unset returns nil (system store only, the prior behavior). A
// missing or malformed file is a hard startup error, matching the fail-closed
// handling of the other server env knobs.
func loadHubForwardTLS() (*tls.Config, error) {
	path := os.Getenv("CORNUS_HUB_FORWARD_CA")
	if path == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("invalid CORNUS_HUB_FORWARD_CA: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("invalid CORNUS_HUB_FORWARD_CA: %s contains no PEM certificates", path)
	}
	return &tls.Config{RootCAs: pool}, nil
}

// handleHubForward serves GET /.cornus/v1/hub/forward: the inter-replica delivery hop. A
// peer replica dials here when its Lookup resolved a delivery service owned by THIS
// replica (this process holds the hosting spoke's mux). We read the service name,
// resolve it LOCALLY, and require a local-delivery target (this replica's own mux);
// then open the ingress stream to the spoke and splice. It never re-forwards — if
// the local lookup is not a local delivery (dial-direct, remote, or absent) it just
// closes, so a stale record can never cause a forward loop between replicas.
//
// Trust model: this endpoint is under /.cornus/v1/, so the auth middleware already requires
// a FULL credential (a caretaker-scoped token is rejected — the path is not
// /.cornus/v1/caretaker/attach). The reach policy was ALREADY enforced by the peer replica
// before it forwarded, so we deliberately do NOT re-run it here; we trust the
// authenticated peer's authorization. Only reachable in a multi-replica deployment.
func (s *Server) handleHubForward(w http.ResponseWriter, r *http.Request) {
	conn, err := wire.AcceptConn(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	name, err := wire.ReadLine(conn)
	if err != nil {
		return
	}
	tgt, ok := s.hub.Lookup(name)
	if !ok || tgt.Mux == nil {
		return // not a local delivery here — never re-forward (loop guard)
	}
	up, err := hub.OpenDeliver(tgt.Mux, name)
	if err != nil {
		return
	}
	defer up.Close()
	wire.Pipe(conn, up)
}

// handleHubCatalog serves GET /.cornus/v1/hub/catalog: the overlay's live directory — a
// JSON object {"services": [...]} of the service names currently registered (at
// least one live provider). A discovery/status read for operators and spokes.
// Reserved mount-session routing records are filtered out (filterMountCatalog);
// they are internal state, not services.
func (s *Server) handleHubCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string][]string{"services": filterMountCatalog(s.hub.Catalog())})
}

// verifiedIdentity returns the identity (certificate CommonName) from a VERIFIED
// mTLS client certificate on the request, or "" when the connection presents none.
// A non-empty result is authoritative for hub policy: it is set before any control-
// stream registration, so a spoke cannot claim an identity its cert does not bear.
func verifiedIdentity(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	return r.TLS.PeerCertificates[0].Subject.CommonName
}
