package hub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"

	"github.com/hashicorp/yamux"

	"cornus/pkg/wire"
)

// Registration is the message a spoke sends on the hub control stream: its own
// identity (used for policy; verified against the mTLS peer cert when present) and
// the services it hosts, each with an address the hub can dial to reach it.
type Registration struct {
	Identity string    `json:"identity,omitempty"`
	Services []Service `json:"services"`
	// Watch is a capability flag: the spoke asks the hub to push CatalogUpdate
	// frames back on this same control stream whenever the registered-service set
	// changes (dynamic import discovery). Version skew is safe in both directions:
	// an old server ignores the unknown field (and pushes nothing, so the spoke's
	// watcher just idles), and a spoke that does not set it never receives a frame
	// — the hub only writes to control streams that declared the capability.
	Watch bool `json:"watch,omitempty"`
}

// CatalogUpdate is the message the hub pushes on a WATCHING spoke's control
// stream when the catalog changes: the full current set of registered service
// names (a snapshot, not a delta — the spoke diffs it against its own state).
// The shape matches the GET /.cornus/v1/hub/catalog response body.
type CatalogUpdate struct {
	Services []string `json:"services"`
}

// Service is one hosted service: a name other spokes reach it by, and the address
// the hub dials for it (Phase 1: a hub-reachable address such as a cluster
// Service or pod IP). Protocol is "tcp" (default, empty) or "udp"; it is only
// meaningful for a dial-direct service (the hub then UDP-dials Addr).
type Service struct {
	Name     string `json:"name"`
	Addr     string `json:"addr"`
	Protocol string `json:"protocol,omitempty"`
}

// Dial opens a spoke's hub connection (yamux over WebSocket) to url.
func Dial(ctx context.Context, url string) (*yamux.Session, error) { return wire.Dial(ctx, url) }

// DialTLS opens a spoke's hub connection over mTLS: tc carries the spoke's client
// certificate (its authoritative identity, the cert CommonName) and the trusted CA
// roots. The hub uses the verified cert identity for policy, overriding any
// identity declared on the control stream.
func DialTLS(ctx context.Context, url string, tc *tls.Config) (*yamux.Session, error) {
	return wire.DialTLS(ctx, url, tc)
}

// Register opens the control stream on sess and sends reg. The returned Closer
// must be kept open for the connection's life; closing it (or the session)
// deregisters the services on the hub when the connection drops.
func Register(sess *yamux.Session, reg Registration) (io.Closer, error) {
	ctl, err := wire.OpenTagged(sess, wire.TagControl)
	if err != nil {
		return nil, err
	}
	if err := json.NewEncoder(ctl).Encode(reg); err != nil {
		ctl.Close()
		return nil, err
	}
	return ctl, nil
}

// OpenTo opens a data stream to a named service through the hub. Writes are
// relayed to the service; reads return its response.
func OpenTo(sess *yamux.Session, name string) (net.Conn, error) {
	return openNamed(sess, wire.TagData, name)
}

// OpenDeliver opens an ingress-delivery stream on a destination spoke's session
// (called by the hub) to hand it an inbound connection for the named service; the
// spoke dials its local target for that name and splices.
func OpenDeliver(sess *yamux.Session, name string) (net.Conn, error) {
	return openNamed(sess, wire.TagDeliver, name)
}

func openNamed(sess *yamux.Session, tag byte, name string) (net.Conn, error) {
	s, err := wire.OpenTagged(sess, tag)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(s, name+"\n"); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
