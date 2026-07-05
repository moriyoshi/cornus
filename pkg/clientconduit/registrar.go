package clientconduit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"cornus/pkg/conduithost"
	"cornus/pkg/portfwd"
	"cornus/pkg/socks5"
)

// Registration kinds carried on a conduit's control socket. They are strings on the
// wire rather than an enum so an older host meets an unknown kind with a clear
// refusal instead of a misread.
const (
	// KindAlias claims a service label for a deployment.
	KindAlias = "alias"
	// KindLocal publishes a name served by the REGISTERING process, reached through
	// a unix socket it holds open.
	KindLocal = "local"
)

// AliasPayload claims a short name for a deployment.
type AliasPayload struct {
	Label      string `json:"label"`
	Deployment string `json:"deployment"`
}

// LocalPayload publishes host:port, served by the registering process.
//
// Upstream is a unix socket path in that process. It is the one registration whose
// data plane cannot stay with the host: an in-process listener has no address by
// design (pkg/memlisten), so the only way to reach it from elsewhere is a socket
// the publisher holds open. Everything else routes through the host's own tunnel.
type LocalPayload struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Upstream string `json:"upstream"`
}

// DialerFor resolves the tunnel a registering participant's workloads are reached
// through. It is a function rather than a fixed dialer because a conduit joined by
// ADDRESS is shared by projects that need not be talking to the same cornus server,
// and each claim must be dialed through its own.
//
// A nil result (or a nil DialerFor) falls back to the proxy's own dialer, which is
// right for the single-connection case and wrong the moment two are consolidated.
type DialerFor func(peer conduithost.Peer) portfwd.Dialer

// Registrar applies a conduit's registrations to a socks5 router. It is the bridge
// between pkg/conduithost, which knows about rendezvous and lifetimes but
// deliberately nothing about routing, and this package, which knows what a
// registration means.
//
// It also implements conduithost.Recoverer, so a takeover puts the router into a
// recovery window and unrestored names WAIT rather than being answered wrongly.
type Registrar struct {
	Router *socks5.Router
	// Dialer resolves each registering peer's tunnel; nil uses the proxy's own.
	Dialer DialerFor
	// LocalDial opens a connection to a published name's upstream. nil refuses
	// KindLocal registrations, which is correct for a host that cannot reach another
	// process's sockets rather than pretending it published something.
	LocalDial func(upstream string) socks5.LocalDialer
	// Logf reports a name MOVING between workloads.
	//
	// Several projects may share a conduit and claim the same short name; the latest
	// claim wins, and withdrawing it hands the name back to whoever held it before.
	// Both are silent otherwise — a client keeps using the name and reaches a
	// different workload, with no error anywhere to notice — and this is the only
	// place that knows it happened.
	Logf func(format string, args ...any)
}

// report announces a name movement, unless the router is recovering.
//
// During recovery a claim arriving is a name being RESTORED after a takeover, not
// a name moving, and narrating each one would bury the case that matters under a
// burst after every handover.
func (r *Registrar) report(format string, args ...any) {
	if r.Logf == nil || r.Router.Recovering() {
		return
	}
	r.Logf(format, args...)
}

var (
	_ conduithost.Registrar = (*Registrar)(nil)
	_ conduithost.Recoverer = (*Registrar)(nil)
)

// Register applies one registration and returns its withdrawal.
func (r *Registrar) Register(_ context.Context, reg conduithost.Registration) (conduithost.Withdraw, error) {
	switch reg.Kind {
	case KindAlias:
		var p AliasPayload
		if err := json.Unmarshal(reg.Payload, &p); err != nil {
			return nil, fmt.Errorf("conduit: malformed %s registration: %w", reg.Kind, err)
		}
		if p.Label == "" || p.Deployment == "" {
			return nil, fmt.Errorf("conduit: %s registration needs both a label and a deployment", reg.Kind)
		}
		var d portfwd.Dialer
		if r.Dialer != nil {
			d = r.Dialer(reg.Peer)
		}
		// Seq carried verbatim: it is this claim's precedence, assigned once and
		// preserved across every later host, so a replay after a takeover restores the
		// order rather than renumbering by arrival.
		if got := r.Router.Claim(socks5.AliasSpec{Label: p.Label, Deployment: p.Deployment, Seq: reg.Seq, Dialer: d}); got.Changed {
			r.report("the name %q now reaches %s", p.Label, got.Winner)
		}
		return func() {
			got := r.Router.UnregisterAlias(p.Label, p.Deployment)
			switch {
			case !got.Changed:
			case got.Winner == "":
				r.report("the name %q is no longer served (%s left)", p.Label, p.Deployment)
			default:
				r.report("the name %q now reaches %s (%s left)", p.Label, got.Winner, p.Deployment)
			}
		}, nil

	case KindLocal:
		var p LocalPayload
		if err := json.Unmarshal(reg.Payload, &p); err != nil {
			return nil, fmt.Errorf("conduit: malformed %s registration: %w", reg.Kind, err)
		}
		if p.Host == "" || p.Port < 1 || p.Port > 65535 {
			return nil, fmt.Errorf("conduit: %s registration needs a host and a port in 1-65535", reg.Kind)
		}
		if r.LocalDial == nil {
			return nil, fmt.Errorf("conduit: this host cannot serve published names registered by another process")
		}
		d := r.LocalDial(p.Upstream)
		if d == nil {
			return nil, fmt.Errorf("conduit: no upstream for published name %s:%d", p.Host, p.Port)
		}
		local := r.Router.RegisterLocalSeq(p.Host, p.Port, d, reg.Seq)
		if !local.Handle.Valid() {
			return nil, fmt.Errorf("conduit: publishing %s:%d was rejected", p.Host, p.Port)
		}
		if local.Changed {
			r.report("the published name %s:%d is now served by a different publisher", p.Host, p.Port)
		}
		// Withdraw by HANDLE, not by host:port. Several publishers may claim one
		// subject, and a key-based withdrawal removes whichever claim is serving —
		// usually somebody else's, and still in use.
		return func() {
			if got := r.Router.UnregisterLocal(local.Handle); got.Changed {
				r.report("the published name %s:%d has changed hands or is no longer served", p.Host, p.Port)
			}
		}, nil

	default:
		return nil, fmt.Errorf("conduit: unknown registration kind %q", reg.Kind)
	}
}

// BeginRecovery puts the router into a recovery window, so a name whose owner has
// not re-registered yet holds the request instead of being answered as unknown.
func (r *Registrar) BeginRecovery(until time.Time) { r.Router.SetRecoveryUntil(until) }
