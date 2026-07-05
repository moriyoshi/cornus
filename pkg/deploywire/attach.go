package deploywire

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/hashicorp/yamux"

	"cornus/pkg/egresspolicy"
	"cornus/pkg/wire"
)

// ServerSession is the server-side view of a deploy-attach: the caller's spec,
// the yamux session (used to open 9P mount backings), and the control stream
// over which status/log events flow out and a "down" command may arrive.
type ServerSession struct {
	Spec DeployAttachSpec

	sess    *yamux.Session
	control net.Conn

	encMu sync.Mutex
	enc   *json.Encoder
	dec   *json.Decoder

	egressOnce   sync.Once
	egressPolicy egresspolicy.Policy
	egressErr    error
}

// Attach upgrades the request to the deploy WebSocket and reads the caller's
// DeployAttachSpec from the control stream.
func Attach(w http.ResponseWriter, r *http.Request) (*ServerSession, error) {
	sess, err := wire.Accept(w, r)
	if err != nil {
		return nil, err
	}
	tag, control, err := wire.AcceptTagged(sess)
	if err != nil {
		sess.Close()
		return nil, err
	}
	if tag != wire.TagControl {
		sess.Close()
		return nil, fmt.Errorf("deploywire: expected control stream, got tag %q", tag)
	}
	// The same decoder must read the spec and any later commands: json.Decoder
	// buffers, so a second decoder on the stream could drop bytes.
	dec := json.NewDecoder(control)
	var spec DeployAttachSpec
	if err := dec.Decode(&spec); err != nil {
		sess.Close()
		return nil, fmt.Errorf("deploywire: read spec: %w", err)
	}
	return &ServerSession{
		Spec:    spec,
		sess:    sess,
		control: control,
		enc:     json.NewEncoder(control),
		dec:     dec,
	}, nil
}

// Mux returns the underlying yamux session, used to open 9P mount backings.
func (s *ServerSession) Mux() *yamux.Session { return s.sess }

// AllowsMount reports whether name is one of the session's declared local-mount
// backings — the mount-relay endpoint checks this so a pod can only pull the
// exports its own deployment declared.
func (s *ServerSession) AllowsMount(name string) bool {
	for _, lm := range s.Spec.LocalMounts {
		if lm.Name == name {
			return true
		}
	}
	return false
}

// AllowsCredential reports whether name is one of the session's declared
// credential sources — the credential-relay endpoint checks this so a pod can
// only fetch the credentials its own deployment declared (the same capability
// model as AllowsMount).
func (s *ServerSession) AllowsCredential(name string) bool {
	for _, cs := range s.Spec.CredentialSources {
		if cs.Name == name {
			return true
		}
	}
	return false
}

// MountCacheable reports whether the named mount backing may be served through
// the server-side block cache (immutable and read-only). The mount-relay paths
// consult this to decide between a caching 9P proxy and a blind byte pipe.
func (s *ServerSession) MountCacheable(name string) bool {
	for _, lm := range s.Spec.LocalMounts {
		if lm.Name == name {
			return lm.Cacheable()
		}
	}
	return false
}

// MountWritableCacheable reports whether the named mount uses the writable,
// cache-coherent block protocol (see LocalMount.WritableCacheable). The mount-relay
// paths consult it to choose the writable block proxy over the blind pipe.
func (s *ServerSession) MountWritableCacheable(name string) bool {
	for _, lm := range s.Spec.LocalMounts {
		if lm.Name == name {
			return lm.WritableCacheable()
		}
	}
	return false
}

// EgressRoute resolves the routing verdict for an egress destination against the
// deployment's own policy — the SERVER's authoritative re-check so a compromised
// pod cannot upgrade its own routing. It compiles the policy once and caches it. A
// deployment with no egress policy defaults to "cluster" (no relay). A compile
// error is returned so the relay fails closed.
func (s *ServerSession) EgressRoute(host string, port int) (string, error) {
	s.egressOnce.Do(func() {
		if e := s.Spec.Spec.Egress; e != nil {
			s.egressPolicy, s.egressErr = egresspolicy.Compile(*e)
		}
	})
	if s.egressErr != nil {
		return "", s.egressErr
	}
	if s.egressPolicy == nil {
		return egresspolicy.RouteCluster, nil
	}
	d := egresspolicy.Dest{Host: host, Port: port, Proto: "tcp"}
	if ip := net.ParseIP(host); ip != nil {
		d.IP = ip
	}
	return s.egressPolicy.Route(d)
}

// Event sends a control frame to the caller.
func (s *ServerSession) Event(e Event) error {
	s.encMu.Lock()
	defer s.encMu.Unlock()
	return s.enc.Encode(e)
}

// Wait blocks until the caller disconnects (control stream EOF/error) or sends a
// graceful "down" command. Either outcome means "tear the deployment down now".
func (s *ServerSession) Wait() {
	for {
		var c command
		if err := s.dec.Decode(&c); err != nil {
			return // caller gone
		}
		if c.Action == "down" {
			return
		}
	}
}

// Close tears down the WebSocket session.
func (s *ServerSession) Close() { _ = s.sess.Close() }
