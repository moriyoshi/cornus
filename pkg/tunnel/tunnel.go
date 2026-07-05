// Package tunnel is cornus's backend-agnostic seam for exposing a deployed
// application to the public internet through a hosted tunnel relay. The server
// hosts the tunnel in-process and bridges each inbound connection to the
// workload's port via deploy.Backend.ForwardPort, so the feature reaches ports
// the workload never published — exactly like `cornus port-forward`, but with a
// public URL instead of a local listener.
//
// A Provider is one relay backend. The default is ngrok (pkg/tunnel/ngrok),
// whose terms permit embedding the agent in your own product; additional
// backends (e.g. a self-hosted zrok relay) can register themselves without any
// change to the server or CLI wiring.
package tunnel

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"

	"golang.org/x/crypto/ssh/agent"
)

// DefaultBackend is the provider name used when none is configured.
const DefaultBackend = "ngrok"

// Credential carries the secret a Provider needs to host a tunnel. For ngrok it
// is an account authtoken; it is a bearer secret and must never be logged or
// persisted.
//
// Agent is an optional forwarded ssh-agent, used only by the ssh backend to
// authenticate with agent-held keys instead of (or alongside) AuthToken. It is
// non-nil only when the caller opened an "ssh-agent" tunnel channel; backends
// that don't consult it simply ignore it.
type Credential struct {
	AuthToken string
	Agent     agent.Agent
}

// Options tune a single tunnel.
type Options struct {
	// Proto is the exposed protocol hint ("http" or "tcp"); empty lets the
	// backend pick its default (ngrok defaults to an HTTPS endpoint).
	Proto string
	// Metadata is opaque backend-visible metadata (e.g. the deployment name),
	// surfaced in the provider's dashboard for operator visibility.
	Metadata string
	// Hostname is the deployment name, for backends that name the tunnel after it
	// (e.g. Tailscale derives the node hostname from it).
	Hostname string
}

// Session is one live hosted tunnel in the listener model. Accept yields inbound
// visitor connections the caller bridges to the workload; it behaves like
// net.Listener.Accept and returns a non-nil error once the session is closed.
type Session interface {
	// URL is the public URL clients use to reach the tunnel.
	URL() string
	// Accept returns the next inbound connection to the tunnel.
	Accept() (net.Conn, error)
	// Close tears the tunnel down; Accept then unblocks with an error.
	Close() error
}

// UpstreamSession is one live hosted tunnel in the upstream model: the backend
// forwards edge traffic to a local address the manager provides, so there is no
// Accept — the manager's own shim listener does the accepting.
type UpstreamSession interface {
	// URL is the public URL clients use to reach the tunnel.
	URL() string
	// Close tears the tunnel down.
	Close() error
}

// Provider opens tunnels in the listener model: it hands inbound connections back
// via Session.Accept for the caller to bridge to the workload. This is the
// efficient path (no extra local hop) and the default for backends that expose an
// in-process listener (ngrok, ssh).
type Provider interface {
	// Start hosts a new tunnel using cred and returns its live Session. ctx
	// governs the lifetime of the session's underlying connection, so callers
	// pass a context that outlives the request that created the tunnel.
	Start(ctx context.Context, cred Credential, opts Options) (Session, error)
}

// UpstreamProvider opens tunnels in the upstream model: the backend forwards edge
// traffic to upstreamURL, a local address the manager stands up (a shim listener
// bridging to the workload). This suits backends that only proxy to a local
// upstream — e.g. Cloudflare's cloudflared subprocess.
type UpstreamProvider interface {
	StartUpstream(ctx context.Context, cred Credential, opts Options, upstreamURL string) (UpstreamSession, error)
}

// CredentialOptional is implemented by backends that can host without an injected
// credential — e.g. Cloudflare quick tunnels, which are anonymous. Backends that
// require a credential simply do not implement it (the default is: required).
type CredentialOptional interface {
	CredentialOptional() bool
}

// Factory constructs a backend. The returned value's concrete type must implement
// Provider or UpstreamProvider; the server dispatches on which. Backends register
// a Factory under a name via Register.
type Factory func() (any, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register makes a backend available under name. Backends call it from an init
// function; a blank import of the backend package wires it in. Registering the
// same name twice panics, since it signals two backends fighting for one name.
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("tunnel: duplicate backend registration for " + name)
	}
	registry[name] = f
}

// Open constructs the named backend. The returned value implements Provider or
// UpstreamProvider (the caller type-switches). It errors when the name is unknown
// (typically because its package was not blank-imported).
func Open(name string) (any, error) {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown tunnel backend %q (available: %v)", name, Backends())
	}
	return f()
}

// ListenerSession adapts a net.Listener plus a public URL into a Session, for
// backends whose host handle is already a net.Listener (ssh). Closing it closes
// the listener and, if set, runs extraClose (e.g. tear down the SSH client).
type ListenerSession struct {
	Listener   net.Listener
	PublicURL  string
	ExtraClose func() error
}

func (s *ListenerSession) URL() string               { return s.PublicURL }
func (s *ListenerSession) Accept() (net.Conn, error) { return s.Listener.Accept() }
func (s *ListenerSession) Close() error {
	err := s.Listener.Close()
	if s.ExtraClose != nil {
		if e := s.ExtraClose(); err == nil {
			err = e
		}
	}
	return err
}

// Backends lists the registered backend names, sorted, for diagnostics.
func Backends() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
