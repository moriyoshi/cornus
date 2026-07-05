package wire

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"syscall"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Stream tags shared by the transport. Control is the first stream both build
// and deploy sessions open (the spec goes out, progress/status comes back);
// tagLazy9P is the per-context 9P backing for lazy bind mounts (mounted by
// kernel-9p on the server). Build/deploy-specific tags (e.g. the eager 9P and
// SSH streams) live with their own wire.
const (
	tagControl       = 'C'
	tagLazy9P        = 'L'
	tagMount         = 'M'
	tagData          = 'D'
	tagDeliver       = 'I'
	tagCredential    = 'K'
	tagCredBacking   = 'k'
	tagEgress        = 'E'
	tagEgressBacking = 'e'
	tagPortForward   = 'F'
	tagAgentRelay    = 'A'
	tagBlockFS       = 'b' // writable, cache-coherent block protocol backing
)

// TagControl identifies the control stream both build and deploy sessions open
// first (the spec goes out, progress/status comes back).
const TagControl = tagControl

// TagMount identifies a caretaker mount stream on a pod's single multiplexed
// connection: the caretaker opens one per client-local mount, writes a name
// line, and the server bridges it to that name's 9P backing on the caller.
const TagMount = tagMount

// TagData identifies a hub data stream: a spoke opens one to reach a named
// service, writes a name line, and the hub relays it to that service.
const TagData = tagData

// TagDeliver identifies a hub ingress-delivery stream: the hub opens one TO a
// destination spoke, writes the service name line, and the spoke dials its local
// target and splices — the path for services not reachable by the hub directly.
const TagDeliver = tagDeliver

// TagCredential identifies a caretaker credential stream on a pod's single
// multiplexed connection: the caretaker opens one per fetch, writes its
// deploy-attach session then the credential name, and the server relays it to
// that name's credential backing on the caller (mirroring TagMount).
const TagCredential = tagCredential

// OpenCredBacking opens a new credential backing stream to the caller for the
// named source — the 'k' backing ServeBackings answers — with the tag and name
// line already written. The credential request/response protocol then rides the
// returned stream (the caller reads a request and writes the minted credential).
func OpenCredBacking(sess *yamux.Session, name string) (net.Conn, error) {
	stream, err := openTagged(sess, tagCredBacking)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(stream, name+"\n"); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

// TagEgress identifies a caretaker egress stream on a pod's single multiplexed
// connection: the caretaker opens one per outbound connection it routes to a relay
// terminus, writes its deploy-attach session then the destination, and the server
// relays it to the client (or a gateway). See OpenEgress.
const TagEgress = tagEgress

// OpenEgress opens a caretaker egress stream (server-bound 'E') and writes the
// session id, the route ("client" or "gateway"), and the destination, ready for the
// app's bytes to be spliced onto it. The caretaker uses it when a connection's route
// resolves to a relay terminus; the server answers with relayEgressMuxed. The route
// is authoritative only for a SESSIONLESS (gateway/detached) stream — when the
// server holds the session, its own policy re-check governs.
func OpenEgress(sess *yamux.Session, session, route, dest string) (net.Conn, error) {
	stream, err := openTagged(sess, tagEgress)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(stream, session+"\n"+route+"\n"+dest+"\n"); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

// OpenEgressBacking opens an egress backing stream TO the caller for a destination
// — the 'e' backing ServeBackings answers — with the tag and destination line
// already written. The caller dials that destination through its own network and
// splices, so the pod's egress leaves from the client. Mirrors OpenCredBacking.
func OpenEgressBacking(sess *yamux.Session, dest string) (net.Conn, error) {
	stream, err := openTagged(sess, tagEgressBacking)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(stream, dest+"\n"); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

// TagPortForward identifies a port-forward-relay stream on a remote-mode
// companion's multiplexed connection. Unlike every other tag, the SERVER opens
// this stream TOWARD the caretaker (the reverse of the usual direction) when an
// external cornus port-forward/tunnel connection needs to reach a port inside
// the app instance the companion shares its network namespace with — see
// OpenPortForward and pkg/caretaker's PortForwardRole.
const TagPortForward = tagPortForward

// OpenPortForward opens a server-initiated port-forward-relay stream on a
// caretaker's own session (found via the server's per-instance companion
// registry) and writes the destination port and protocol, ready for the
// external caller's connection to be spliced onto it once the caretaker's
// PortForwardRole accept loop dials 127.0.0.1:port in the app's shared netns.
func OpenPortForward(sess *yamux.Session, port int, proto string) (net.Conn, error) {
	stream, err := openTagged(sess, tagPortForward)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(stream, strconv.Itoa(port)+"\n"+proto+"\n"); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

// TagAgentRelay identifies a caretaker ssh-agent-relay stream: the caretaker
// opens one per local connection to the forwarded agent socket inside the app
// instance's shared scratch volume, and the server relays it to whichever
// `cornus exec --forward-agent` session currently holds the real agent for
// this instance — see OpenAgentRelay and pkg/caretaker's AgentRelayRole.
const TagAgentRelay = tagAgentRelay

// OpenAgentRelay opens a caretaker agent-relay stream (server-bound). No
// header is written: the server already knows which instance this stream
// belongs to, since a TagAgentRelay stream only ever arrives on that
// instance's own caretaker connection.
func OpenAgentRelay(sess *yamux.Session) (net.Conn, error) {
	return openTagged(sess, tagAgentRelay)
}

// Dial opens the WebSocket to a cornus attach endpoint and returns a yamux
// client session over it.
func Dial(ctx context.Context, url string) (*yamux.Session, error) { return dial(ctx, url) }

// Accept upgrades an HTTP request to a WebSocket and returns a yamux server
// session over it.
func Accept(w http.ResponseWriter, r *http.Request) (*yamux.Session, error) {
	return accept(w, r)
}

// OpenTagged opens a yamux stream and writes its 1-byte tag.
func OpenTagged(sess *yamux.Session, tag byte) (net.Conn, error) { return openTagged(sess, tag) }

// AcceptTagged accepts a yamux stream and reads its 1-byte tag.
func AcceptTagged(sess *yamux.Session) (byte, net.Conn, error) { return acceptTagged(sess) }

// OpenBacking opens a new 9P backing stream to the caller for the named export —
// the same 'L' backing Serve9PBacking answers — with the tag and name line
// already written, ready to be piped to a local 9P consumer (e.g. a kernel-9p
// mount, or a pod's mount-agent relayed over the network).
func OpenBacking(sess *yamux.Session, name string) (net.Conn, error) {
	stream, err := openTagged(sess, tagLazy9P)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(stream, name+"\n"); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

// OpenBlockBacking opens a new writable, cache-coherent block-protocol backing
// stream TO the caller for the named export — the 'b' backing ServeBackings
// answers with the caller block server — with the tag and name line already
// written. The server then drives the block protocol over the returned stream via
// ServeBlockProxy. Mirrors OpenBacking, for the kube mount-relay paths.
func OpenBlockBacking(sess *yamux.Session, name string) (net.Conn, error) {
	stream, err := openTagged(sess, tagBlockFS)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(stream, name+"\n"); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

// Pipe copies bidirectionally between a and b until either side closes. a and
// b need only be io.ReadWriteCloser — every existing net.Conn caller still
// satisfies that trivially, and it additionally lets a plain
// io.ReadWriteCloser tunnel conn (e.g. ForwardPort's) pipe directly to a
// yamux stream, neither of which is a full net.Conn.
func Pipe(a, b io.ReadWriteCloser) { pipe(a, b) }

// AcceptConn upgrades an HTTP request to a WebSocket and returns it as a single
// net.Conn (no yamux multiplexing) — for a one-stream relay such as a pod
// mount-agent's 9P backing.
func AcceptConn(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return nil, fmt.Errorf("wire: accept: %w", err)
	}
	c.SetReadLimit(readLimit)
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil
}

// DialConn opens a WebSocket to url and returns it as a single net.Conn.
func DialConn(ctx context.Context, url string) (net.Conn, error) {
	return DialConnControl(ctx, url, nil)
}

// DialConnControl is DialConn with a socket Control hook applied to the
// underlying TCP connection of the WebSocket handshake — e.g. to set SO_MARK so
// a netfilter redirect exempts the caretaker's own relay traffic. A nil control
// dials exactly as DialConn.
func DialConnControl(ctx context.Context, url string, control func(network, address string, c syscall.RawConn) error) (net.Conn, error) {
	return dialConn(ctx, url, control, nil, nil, nil)
}

// DialConnControlHeader is DialConnControl with extra HTTP request headers added
// to the WebSocket handshake — used to carry W3C trace context to the relay
// server so its request span links to the caller's. Empty header behaves exactly
// as DialConnControl.
func DialConnControlHeader(ctx context.Context, url string, control func(network, address string, c syscall.RawConn) error, header http.Header) (net.Conn, error) {
	return dialConn(ctx, url, control, header, nil, nil)
}

// DialConnControlHeaderTLS is DialConnControlHeader with a client TLS config
// applied to the wss:// handshake — custom CA roots and/or a client certificate
// for mTLS, as a connection profile supplies. A nil tc dials exactly as
// DialConnControlHeader (system trust store, plain ws:// when the URL is not TLS).
func DialConnControlHeaderTLS(ctx context.Context, url string, control func(network, address string, c syscall.RawConn) error, header http.Header, tc *tls.Config) (net.Conn, error) {
	return dialConn(ctx, url, control, header, tc, nil)
}

// ClientTransport bundles the optional client-side transport customizations a
// connection profile can carry: a TLS config (custom CA / mTLS) and a custom dial
// function (e.g. an SSH-tunnel dialer that returns a net.Conn to the remote server
// over an SSH connection). Either or both may be nil.
type ClientTransport struct {
	TLS         *tls.Config
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// DialConnControlHeaderCT is DialConnControlHeaderTLS that also honors a custom
// dial function (ct.DialContext), used to route the WebSocket handshake's
// underlying connection through an SSH tunnel. A zero ClientTransport behaves
// exactly as the TLS variant with a nil config.
func DialConnControlHeaderCT(ctx context.Context, url string, control func(network, address string, c syscall.RawConn) error, header http.Header, ct ClientTransport) (net.Conn, error) {
	return dialConn(ctx, url, control, header, ct.TLS, ct.DialContext)
}

// DialTLS opens the WebSocket to url over TLS with the given client config (a
// client certificate for mTLS and the trusted CA roots) and returns a yamux CLIENT
// session over it — the secured hub/caretaker dial.
func DialTLS(ctx context.Context, url string, tc *tls.Config) (*yamux.Session, error) {
	opts := &websocket.DialOptions{HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: tc}}}
	c, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, fmt.Errorf("wire: dial %s: %w", url, err)
	}
	c.SetReadLimit(readLimit)
	nc := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
	return yamux.Client(nc, yamuxConfig())
}

// DialControlHeader opens the WebSocket to url (with an optional socket Control
// hook such as SO_MARK and optional handshake headers, exactly as
// DialConnControlHeader) and returns a yamux CLIENT session over it — the
// multiplexed dial a pod caretaker uses to carry every mount stream, plus a
// control stream, on ONE connection. A nil control and empty header dial as Dial.
func DialControlHeader(ctx context.Context, url string, control func(network, address string, c syscall.RawConn) error, header http.Header) (*yamux.Session, error) {
	return DialControlHeaderTLS(ctx, url, control, header, nil)
}

// DialControlHeaderTLS is DialControlHeader with a client TLS config applied to
// the wss:// handshake (custom CA roots and/or an mTLS client certificate, as a
// connection profile supplies). A nil tc dials exactly as DialControlHeader.
func DialControlHeaderTLS(ctx context.Context, url string, control func(network, address string, c syscall.RawConn) error, header http.Header, tc *tls.Config) (*yamux.Session, error) {
	nc, err := dialConn(ctx, url, control, header, tc, nil)
	if err != nil {
		return nil, err
	}
	return yamux.Client(nc, yamuxConfig())
}

// DialControlHeaderCT is DialControlHeaderTLS that also honors a custom dial
// function (ct.DialContext) so the multiplexed session's underlying connection can
// be routed through an SSH tunnel.
func DialControlHeaderCT(ctx context.Context, url string, control func(network, address string, c syscall.RawConn) error, header http.Header, ct ClientTransport) (*yamux.Session, error) {
	nc, err := dialConn(ctx, url, control, header, ct.TLS, ct.DialContext)
	if err != nil {
		return nil, err
	}
	return yamux.Client(nc, yamuxConfig())
}

func dialConn(ctx context.Context, url string, control func(network, address string, c syscall.RawConn) error, header http.Header, tc *tls.Config, dial func(ctx context.Context, network, addr string) (net.Conn, error)) (net.Conn, error) {
	var opts *websocket.DialOptions
	if control != nil || len(header) > 0 || tc != nil || dial != nil {
		opts = &websocket.DialOptions{}
		if control != nil || tc != nil || dial != nil {
			tr := &http.Transport{}
			switch {
			case dial != nil:
				// A custom dialer (e.g. an SSH tunnel) takes precedence; the socket
				// Control hook does not apply to a tunneled conn.
				tr.DialContext = dial
			case control != nil:
				d := &net.Dialer{Control: control}
				tr.DialContext = d.DialContext
			}
			if tc != nil {
				tr.TLSClientConfig = tc
			}
			opts.HTTPClient = &http.Client{Transport: tr}
		}
		if len(header) > 0 {
			opts.HTTPHeader = header
		}
	}
	c, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, fmt.Errorf("wire: dial %s: %w", url, err)
	}
	c.SetReadLimit(readLimit)
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil
}
