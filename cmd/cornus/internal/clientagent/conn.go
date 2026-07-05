package clientagent

import (
	"strings"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/client"
	"cornus/pkg/clientconduit"
	"cornus/pkg/portfwd"
)

// ConduitCfg is the resolved conduit configuration a client sends to the agent
// (aliased so the wire type and the engine type stay identical).
type ConduitCfg = clientconduit.Config

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

// conduitKey is the identity of one Conduit within a connState. A shared conduit
// keys on its config alone, so every session with the same tunnel config joins one
// proxy. A session-local conduit additionally keys on a per-session discriminator
// (the project name or docker socket) so it is never shared — its own listener and
// alias table, coexisting with the shared proxy and other session-local ones.
type conduitKey struct {
	mode      string
	listen    string
	suffix    string
	rules     string
	bareNames string // "", "on", or "off" — folds the tri-state toggle into the key
	local     string // "" for the shared proxy; the session id for a session-local one
}

// conduitKeyOf derives the key for cfg. session identifies the requesting session
// (project name or docker socket); it is folded into the key only for a
// session-local conduit, so shared conduits ignore it and are shared as before.
func conduitKeyOf(cfg ConduitCfg, session string) conduitKey {
	var rules []string
	for _, r := range cfg.Socks5Resolve {
		rules = append(rules, r.Pattern+"="+r.Replace)
	}
	bareNames := ""
	if cfg.Socks5BareServiceNames != nil {
		if *cfg.Socks5BareServiceNames {
			bareNames = "on"
		} else {
			bareNames = "off"
		}
	}
	local := ""
	if cfg.Socks5SessionLocal {
		local = session
	}
	return conduitKey{mode: cfg.Mode, listen: cfg.Socks5Listen, suffix: cfg.Socks5Suffix, rules: strings.Join(rules, "\n"), bareNames: bareNames, local: local}
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
	eg   clientconduit.Conduit
	refs int
}

// ResolveFunc resolves a ConnSpec to a live connection. The default reuses
// clientconn.Resolver (kube-auth minting, TLS, static token, in-cluster
// svcforward); tests inject a fake.
type ResolveFunc func(ConnSpec) (*clientconn.Conn, error)

func defaultResolve(s ConnSpec) (*clientconn.Conn, error) {
	r := &clientconn.Resolver{ConfigFile: s.ConfigFile, Context: s.Context}
	return r.ResolveWith(s.Server, s.Token)
}
