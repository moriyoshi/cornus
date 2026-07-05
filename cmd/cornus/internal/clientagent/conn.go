package clientagent

import (
	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/client"
	"cornus/pkg/clientconduit"
	"cornus/pkg/conduithost"
	"cornus/pkg/portfwd"
	"cornus/pkg/socks5"
)

// ConduitCfg is the resolved conduit configuration a client sends to the agent
// (aliased so the wire type and the engine type stay identical).
type ConduitCfg = clientconduit.Config

type WireConduitCfg clientconduit.Config

func ToWireConduit(c clientconduit.Config) WireConduitCfg { return WireConduitCfg(c) }
func (c WireConduitCfg) Runtime() clientconduit.Config    { return clientconduit.Config(c) }

// ConnSpec identifies a cornus server connection for the agent to resolve. The
// client pre-resolves the env-derived tri-states (ViaServer, Token) and sends
// concrete values, because the agent's process env is frozen at spawn and must
// not consult os.Getenv on behalf of a later client.
type ConnSpec struct {
	ConfigFile string `json:"configFile,omitempty"`
	Context    string `json:"context,omitempty"`
	Server     string `json:"server,omitempty"`
	ViaServer  bool   `json:"viaServer,omitempty"`
	Token      string `json:"token,omitempty"`
}

// connKey fully determines the resolved connection, so two clients targeting the
// same server share one connState (one svcforward tunnel, one kube token, one
// conduit).
type connKey struct {
	ConfigFile string
	Context    string
	Server     string
	ViaServer  bool
	Token      string
}

func (s ConnSpec) key() connKey {
	return connKey{ConfigFile: s.ConfigFile, Context: s.Context, Server: s.Server, ViaServer: s.ViaServer, Token: s.Token}
}

// conduitKey is the canonical identity of one conduit within a connection. It is
// also what makes a conduit SHARED: two frontends whose configurations key the
// same conduit join one live conduit and refcount it.
type conduitKey = clientconduit.Identity

// conduitKeyOf keys the conduit a configuration asks for. It canonicalizes cfg
// first so the key that ACQUIRES a conduit and the key that later RELEASES it
// always agree: ensureConduitLocked defaults an empty Mode to port-forward, so a
// caller that keyed the raw config would look up a key that is not in the map and
// silently leak both the conduit and its refcount.
func conduitKeyOf(cfg ConduitCfg, session string) conduitKey {
	cfg = canonicalConduitCfg(cfg)
	// A socks5 conduit with an agreed ADDRESS is keyed on that address alone,
	// because the address is what it is: two configurations naming one address are
	// one conduit, whatever else they disagree about, and the incumbent's settings
	// win. Keying on the full identity instead made them two conduits that then
	// fought over one bind — the collision canonicalConduitCfg was written to paper
	// over, and which it could only ever paper over for configurations that were
	// spelled differently but meant the same thing.
	//
	// Keying on the address is also what lets a second request in THIS process reuse
	// the conduit object rather than open a second participant that joins its own
	// host — which would work for names but not for published ones, since those
	// terminate in the hosting side.
	if cfg.Mode == clientconduit.ModeSocks5 && !ephemeralListen(cfg.Socks5Listen) {
		return conduitKey{Mode: string(cfg.Mode), Listen: cfg.Socks5Listen}
	}
	// Everything else keeps the full identity, including the session where the
	// configuration asked for a private proxy.
	//
	// An EPHEMERAL socks5 conduit is private in the sense that matters — no other
	// PROCESS can find it, because there is no agreed address to rendezvous on, so it
	// is never advertised. That is not the same as being unshareable WITHIN this
	// process: a docker frontend and a web UI with identical settings still join one
	// conduit and one bound port, which is the whole point of them sharing a browser
	// proxy setting. Forcing the session in here would give each its own random port
	// and leave a browser able to reach only one of them.
	return cfg.Identity(session)
}

// ephemeralListen reports whether addr asks the kernel to choose the port.
func ephemeralListen(addr string) bool {
	a, err := conduithost.ParseAddr(addr)
	return err == nil && a.Ephemeral()
}

// canonicalConduitCfg fills in the defaults the conduit engine applies, so one
// configuration has exactly one identity.
//
// The socks5 fields matter as much as the mode, and for a sharper reason. A
// clientconduit.Identity hashes the RAW listen/suffix/bare-names, but the engine
// substitutes its own defaults for the empty spellings — so `--conduit socks5`
// (listen "") and `--conduit socks5://.shared:1080` described the very same proxy
// under two different keys. That is not two configurations disagreeing; it is one
// configuration spelled twice, and the second frontend to arrive forked a conduit
// that could only ever fail to bind. Substituting here, before the key is taken,
// turns that whole family of collisions back into ordinary sharing.
func canonicalConduitCfg(cfg ConduitCfg) ConduitCfg {
	if cfg.Mode == "" {
		cfg.Mode = clientconduit.ModePortForward
	}
	if cfg.Mode != clientconduit.ModeSocks5 {
		return cfg
	}
	if cfg.Socks5Listen == "" {
		cfg.Socks5Listen = socks5.DefaultListen // socks5.Start's substitution
	}
	if len(cfg.Socks5Resolve) > 0 {
		// Explicit rules REPLACE the suffix default (clientconduit.Router), so the
		// suffix reaches nothing the proxy does. Letting an inert field split the
		// identity would fork two conduits that behave identically.
		cfg.Socks5Suffix = ""
	} else if cfg.Socks5Suffix == "" {
		cfg.Socks5Suffix = socks5.DefaultSuffix // socks5.NewSuffixRouter's substitution
	}
	if cfg.Socks5BareServiceNames == nil {
		on := true // socks5.NewRouter enables it unless SetBareServiceNames says otherwise
		cfg.Socks5BareServiceNames = &on
	}
	return cfg
}

// connState is one shared per-server connection: the resolved clientconn.Conn,
// its tunnel dialer, and the conduites built over it, all refcounted by the
// projects and docker frontends that use them.
type connState struct {
	key    connKey
	conn   *clientconn.Conn
	client *client.Client // cn.Client(): the server client, satisfies both clientagent.Attacher
	// (compose sessions) and dockerproxy's deployAttacher (docker frontend)
	dialer  portfwd.Dialer // cn.Dialer(viaServer): conduit tunnel dialer (may be kube-direct)
	conduit map[conduitKey]*conduitState
	refs    int
}

// conduitState is one Conduit (port-forward listeners or a shared SOCKS5 proxy)
// within a connState, refcounted by the frontends sharing it.
type conduitState struct {
	eg clientconduit.Conduit
	// cfg is the CANONICAL configuration this conduit was built from (the same
	// value conduitKeyOf hashed into the map key). The live Conduit exposes only
	// what it does — Banner, CAInfo — never what it was asked for, so without this
	// the inventory could not report the ingress settings a session is running
	// under. Canonical rather than raw, so what is reported is what took effect.
	cfg  clientconduit.Config
	refs int
}

// ResolveFunc resolves a ConnSpec to a live connection. The default reuses
// clientconn.Resolver (kube-auth minting, TLS, static token, in-cluster
// svcforward); tests inject a fake.
type ResolveFunc func(ConnSpec) (*clientconn.Conn, error)

func defaultResolve(s ConnSpec) (*clientconn.Conn, error) {
	r := &clientconn.Resolver{ConfigFile: s.ConfigFile, Context: s.Context, SSHKeyCacheReadOnly: true}
	return r.ResolveWith(s.Server, s.Token)
}
