// Package socks5 is a client-side SOCKS5 split-tunnel proxy that reaches cornus
// workloads by name. It is the alternative to per-port automatic forwarding
// (pkg/portfwd): instead of binding one local listener per published port, a
// single SOCKS5 listener routes each CONNECT by a set of resolution rules.
//
// A CONNECT target "host:port" whose subject matches a rule is rewritten to a
// "service:port" and tunneled into the cluster through a portfwd.Dialer (the
// same PortForward transport port-forwarding uses, reaching a deployment by name
// on any backend). A target that matches no rule is dialed directly from the
// proxy host — the "split tunnel": cluster names go in, everything else egresses
// normally.
//
// Scope is deliberately small: no-auth + CONNECT (TCP) only. BIND, UDP ASSOCIATE,
// and username/password auth are not implemented (SOCKS5 CONNECT is TCP, which is
// all the workload port-forward transport carries anyway).
package socks5

import (
	"context"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cornus/pkg/logging"
	"cornus/pkg/portfwd"
	"cornus/pkg/wire"
)

// DefaultListen is the local address the proxy binds when none is configured.
const DefaultListen = "127.0.0.1:1080"

// DefaultHandshakeTimeout bounds how long an accepted connection may take to
// complete the SOCKS5 method negotiation and CONNECT request before it is
// reaped. Without it, a client that finishes the TCP handshake but sends no
// SOCKS5 bytes would park its handling goroutine, file descriptor, and conns
// entry for the entire proxy lifetime (an unbounded resource leak / DoS).
const DefaultHandshakeTimeout = 30 * time.Second

// DefaultSuffix is the service-host suffix NewSuffixRouter matches when none is
// given: "<name>.cornus.internal:<port>" resolves to service "<name>".
const DefaultSuffix = ".cornus.internal"

// DirectDialer dials the destinations no rule matched — the split-tunnel direct
// egress path. *net.Dialer satisfies it; tests inject a fake.
type DirectDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// LocalDialer opens a connection to a listener published in this process under a
// name. It is the third destination kind, alongside "tunnel to a workload" and
// "dial the internet directly": a published name resolves to its listener without
// any address, so nothing is bound and no port exists for the kernel to recycle to
// an unrelated process. pkg/memlisten satisfies it.
type LocalDialer interface {
	DialLocal(ctx context.Context) (net.Conn, error)
}

// Rule is one resolution rule: a Go regexp Pattern tested against the "host:port"
// subject, and a Replace template that yields the rewritten "service:port". The
// template accepts sed-style \1 backreferences (translated to Go's $1 form) so a
// rule can rewrite both the host and the port.
type Rule struct {
	Pattern string
	Replace string
}

// Router resolves a CONNECT target to a workload service (or reports no match, so
// the caller egresses directly). Rules are tried in order; the first match wins.
//
// On top of the static rules, a Router carries a mutable alias table mapping an
// unqualified service label to its real (context-prefixed) deployment name — e.g.
// "web" -> "demo-web". Aliases let a caller reach a compose service by the name it
// wrote in the compose file, in either form: the suffixed "web.cornus.internal"
// (a rule strips the suffix to "web", then the alias remaps it to "demo-web") or
// the bare, single-label "web" (which no suffix rule matches, so it is routed
// inward only when it exactly matches an alias — everything else egresses
// directly).
//
// Aliases are pure session state: registered/withdrawn as services come and go and
// never persisted, so the table is guarded for concurrent use against Resolve. A
// single proxy is shared by more than one session — and, once conduits are joined
// by address, by more than one PROJECT — so a label is tracked as an ordered list
// of claims, one per distinct deployment, each with a live-registration count.
//
// When several deployments claim one label, THE MOST RECENT CLAIM WINS, and
// withdrawing it restores the one before. So two projects that both call a service
// "web" both work: the short name follows whichever joined last, and each is always
// reachable by its deployment-qualified name ("demo-web.cornus.internal"), which no
// alias ever shadows.
//
// The count is what makes a RECREATE invisible: the replacement registers before
// the predecessor withdraws, so the claim never drops to zero and never loses its
// place in the order. A recreate therefore does not steal a label away from a
// project that claimed it later.
//
// A Router also carries a table of published local names: an exact "host:port"
// subject that resolves to a listener in this process (see LocalDialer). Locals are
// consulted BEFORE the rules, so a published name cannot be shadowed by a
// resolution rule — a catch-all rule would otherwise swallow it, and even the
// default suffix rule claims the whole "<name><suffix>" space.
type Router struct {
	rules []compiledRule

	aliasMu          sync.RWMutex
	aliases          map[string][]aliasClaim // service label -> claims, ascending by seq; the last one wins
	seqNext          uint64                  // next auto-assigned claim sequence (0 means "unset")
	locals           map[string][]localClaim // "host:port" subject -> claims, ascending by seq; the last one wins
	localIDNext      uint64                  // next published-name claim identity
	recoverUntil     time.Time               // while set and unexpired, unclaimed conduit names WAIT rather than answer
	claimWaiters     map[string][]chan struct{}
	bareServiceNames bool // when false, only the suffixed alias form routes inward
}

// aliasClaim is one deployment's claim on a service label: the number of live
// registrations behind it (more than one only while a recreate overlaps), and the
// sequence number that fixes its precedence.
//
// The seq is what makes precedence survive the process that assigned it. "Last
// registered wins" is only well defined while one process has watched every
// registration; the moment a conduit's host is replaced and the survivors replay
// what they hold, arrival order is whatever the reconnect race produced, and the
// same crash routes differently each time. An explicit seq makes precedence a
// property OF THE CLAIM rather than of the order it happened to be applied in.
type aliasClaim struct {
	deployment string
	n          int
	seq        uint64
	// dialer reaches this claimant's workloads, nil for the proxy's own. It rides on
	// the claim because the claim is what knows which session registered it.
	dialer portfwd.Dialer
}

// AliasReg is the outcome of claiming a label.
type AliasReg struct {
	// Seq is the claim's precedence key. A caller that may later have to replay this
	// registration into another router — after a conduit host is replaced — must keep
	// it and pass it back as AliasSpec.Seq, or precedence is decided by the reconnect
	// race instead.
	Seq uint64
	// Winner is the deployment the label resolves to after the call, "" if none.
	Winner string
	// Changed reports that Winner differs from what it was before the call, so a
	// caller can tell the user a short name has moved.
	Changed bool
}

type compiledRule struct {
	re   *regexp.Regexp
	repl string
}

// localClaim is one publisher's claim on an exact "host:port" subject.
//
// Published names need the same ordering aliases have, for the same reason — two
// projects sharing a conduit may both publish the same ingress host — plus one thing
// aliases do not: an IDENTITY. A label claim is identified by its deployment name,
// but two publishers of one subject are told apart only by which registration they
// made, so a withdrawal that names only the subject cannot say which claim it means.
// Without that, an earlier publisher tearing down removes whichever claim is
// currently serving, which is usually somebody else's.
type localClaim struct {
	d   LocalDialer
	seq uint64
	id  uint64
}

// LocalHandle identifies one published-name claim, for withdrawing exactly that
// claim and no other. It is comparable and carries no behaviour; treat it as opaque.
type LocalHandle struct {
	subject string
	id      uint64
}

// Valid reports whether h refers to a registration that was actually made.
func (h LocalHandle) Valid() bool { return h.subject != "" && h.id != 0 }

// LocalReg is the outcome of publishing or withdrawing a name.
type LocalReg struct {
	// Handle withdraws exactly this claim. Zero when nothing was registered.
	Handle LocalHandle
	// Seq is the claim's precedence, to be quoted back on a replay into a router
	// that replaced the one which assigned it.
	Seq uint64
	// Serving reports whether this claim is the one the subject resolves to AS OF
	// THIS CALL. It is a snapshot, not a live property: a later claim on the same
	// subject supersedes it, and this value does not change. A caller that needs the
	// current answer asks the router.
	Serving bool
	// Changed reports that the serving claim differs from before the call, so a
	// caller can tell the user a published name has moved.
	Changed bool
}

// Kind is which destination a routed target resolves to.
type Kind int

const (
	// KindDirect: no name or rule claimed the target — dial host:port directly
	// (the "split" in split-tunnel).
	KindDirect Kind = iota
	// KindService: a rule or alias claimed it — tunnel to Service:Port through the
	// port-forward transport.
	KindService
	// KindLocal: a published name claimed it — hand off to Local, a listener in
	// this process.
	KindLocal
	// KindPending: the target looks like a conduit name, nothing claims it YET, and
	// the router is recovering — a host has just taken over and the participants
	// that hold the other claims have not all re-registered.
	//
	// It exists because during that window the two honest answers are both wrong. A
	// bare name would fall through to direct egress, sending a request that was meant
	// for a workload out to public DNS; a suffixed one would tunnel to a service that
	// does not exist and fail. Neither is what the caller asked for, and the
	// information needed to answer correctly is arriving imminently. So the caller
	// waits, exactly as a connection made while nobody owned the listening socket
	// waits in the kernel's backlog rather than being refused.
	KindPending
)

// Result is the outcome of routing one target. Kind selects which of the other
// fields is meaningful: KindService reads Service/Port, KindLocal reads Local, and
// KindDirect reads neither (the caller dials the original host:port).
type Result struct {
	Kind    Kind
	Service string
	Port    int
	Local   LocalDialer
	// Label is the service label a KindPending result is waiting to see claimed.
	Label string
	// Dialer reaches the workload for a KindService result, nil to use the proxy's
	// own. It is per RESULT rather than per proxy because one conduit is shared by
	// several projects once conduits are joined by address, and those projects need
	// not be talking to the same cornus server. A single proxy-wide dialer would
	// tunnel every consolidated project's traffic to whichever server the proxy
	// happened to be started for.
	Dialer portfwd.Dialer
}

// NewRouter compiles an ordered rule list. A bad pattern is a construction error.
func NewRouter(rules []Rule) (*Router, error) {
	r := &Router{
		aliases:          map[string][]aliasClaim{},
		locals:           map[string][]localClaim{},
		bareServiceNames: true,
	}
	for i, rr := range rules {
		re, err := regexp.Compile(rr.Pattern)
		if err != nil {
			return nil, fmt.Errorf("socks5 resolution rule %d: bad pattern %q: %w", i, rr.Pattern, err)
		}
		r.rules = append(r.rules, compiledRule{re: re, repl: translateReplace(rr.Replace)})
	}
	return r, nil
}

// NewSuffixRouter builds the everyday default Router: a single rule that matches
// hosts bearing suffix and strips it, keeping the port —
// "<name><suffix>:<port>" -> "<name>:<port>". Hosts without the suffix match no
// rule and are dialed directly, so ordinary internet egress keeps working. An
// empty suffix uses DefaultSuffix.
func NewSuffixRouter(suffix string) (*Router, error) {
	if suffix == "" {
		suffix = DefaultSuffix
	}
	pattern := "^(.*)" + regexp.QuoteMeta(suffix) + ":([0-9]+)$"
	return NewRouter([]Rule{{Pattern: pattern, Replace: `\1:\2`}})
}

// SetBareServiceNames controls whether a bare, single-label CONNECT host (no
// suffix) that exactly matches a registered alias is routed inward. Enabled by
// default; disable it as an escape hatch when a service name would shadow a real
// single-label host the caller means to reach directly. The suffixed alias form is
// unaffected.
func (r *Router) SetBareServiceNames(enabled bool) {
	r.aliasMu.Lock()
	r.bareServiceNames = enabled
	r.aliasMu.Unlock()
}

// RegisterAlias records that the unqualified label resolves to deployment for the
// life of one service session. Registrations are counted per (label, deployment),
// so a recreate that overlaps its predecessor and a genuine cross-project collision
// are told apart: the count keeps the alias live across the overlap and holds its
// place in the order, while a new deployment appends and so becomes the claim the
// label resolves to. An empty label or deployment is ignored.
// The returned AliasReg carries the claim's sequence number, the deployment the
// label now resolves to, and whether that changed. A caller that discards it gets
// the old behaviour; a caller that keeps the Seq can replay the claim into a
// replacement router with its precedence intact, and one that reports Changed can
// tell the user a short name has moved.
func (r *Router) RegisterAlias(label, deployment string) AliasReg {
	return r.Claim(AliasSpec{Label: label, Deployment: deployment})
}

// AliasSpec describes one claim on a service label.
type AliasSpec struct {
	Label      string
	Deployment string
	// Seq is the claim's precedence, zero to have the router assign one. Quote the
	// original when replaying into a router that replaced the one which assigned it.
	Seq uint64
	// Dialer reaches this claimant's workloads, nil for the proxy's own. Set it when
	// the claimant talks to a different cornus server than the proxy was started for
	// — which is the normal case once several projects share one conduit.
	Dialer portfwd.Dialer
}

// Claim registers spec, returning the claim's precedence and what the label now
// resolves to. It is the full form; RegisterAlias is the convenience wrapper for a
// claim that needs neither an explicit precedence nor a dialer.
func (r *Router) Claim(spec AliasSpec) AliasReg {
	if spec.Label == "" || spec.Deployment == "" {
		return AliasReg{}
	}
	r.aliasMu.Lock()
	defer r.aliasMu.Unlock()
	if spec.Seq != 0 {
		if spec.Seq >= r.seqNext {
			r.seqNext = spec.Seq + 1
		}
	} else {
		if r.seqNext == 0 {
			r.seqNext = 1
		}
		spec.Seq = r.seqNext
		r.seqNext++
	}
	return r.claimLocked(spec)
}

// claimLocked inserts or refreshes one claim. Caller holds aliasMu.
func (r *Router) claimLocked(spec AliasSpec) AliasReg {
	label, deployment, seq := spec.Label, spec.Deployment, spec.Seq
	before := r.winnerLocked(label)
	claims := r.aliases[label]
	for i := range claims {
		if claims[i].deployment == deployment {
			// Already claimed: bump the count and keep the ORIGINAL seq. This is what
			// makes a recreate invisible — the replacement registers before the
			// predecessor withdraws, and taking a fresh seq here would promote it past a
			// project that claimed the label later, silently stealing the name.
			claims[i].n++
			// A recreate may legitimately arrive on a new session, so refresh the dialer
			// while keeping the sequence: the claim is the same, the route to it is not.
			if spec.Dialer != nil {
				claims[i].dialer = spec.Dialer
			}
			return AliasReg{Seq: claims[i].seq, Winner: before, Changed: false}
		}
	}
	i := sort.Search(len(claims), func(i int) bool { return claims[i].seq > seq })
	claims = append(claims, aliasClaim{})
	copy(claims[i+1:], claims[i:])
	claims[i] = aliasClaim{deployment: deployment, n: 1, seq: seq, dialer: spec.Dialer}
	r.aliases[label] = claims
	// Release anything waiting for this label to be claimed. Collected under the
	// lock and closed after it is dropped, so a woken waiter does not immediately
	// contend for the lock it is about to take.
	woken := r.wakeClaimWaitersLocked(label)
	defer func() {
		for _, ch := range woken {
			close(ch)
		}
	}()
	after := r.winnerLocked(label)
	return AliasReg{Seq: seq, Winner: after, Changed: after != before}
}

// UnregisterAlias drops one registration of label -> deployment (its withdrawal
// when a service session ends). Removing the winning claim restores the one before
// it, so a project leaving hands the short name back to whoever held it earlier
// rather than taking it out of service. The label is forgotten once its last claim
// is gone. Unbalanced or unknown removals are no-ops.
// It returns the deployment the label resolves to after the call ("" once nothing
// claims it) and whether that changed — so the caller can say so. A member leaving
// SILENTLY REPOINTS every short name it had won, and a client that keeps using the
// name reaches a different workload with no error to notice; the deployment-qualified
// form is the spelling that never moves.
func (r *Router) UnregisterAlias(label, deployment string) AliasReg {
	if label == "" || deployment == "" {
		return AliasReg{}
	}
	r.aliasMu.Lock()
	defer r.aliasMu.Unlock()
	before := r.winnerLocked(label)
	claims := r.aliases[label]
	for i := range claims {
		if claims[i].deployment != deployment {
			continue
		}
		seq := claims[i].seq
		if claims[i].n > 1 {
			claims[i].n--
			return AliasReg{Seq: seq, Winner: before, Changed: false}
		}
		r.aliases[label] = append(claims[:i:i], claims[i+1:]...)
		if len(r.aliases[label]) == 0 {
			delete(r.aliases, label)
		}
		after := r.winnerLocked(label)
		return AliasReg{Seq: seq, Winner: after, Changed: after != before}
	}
	return AliasReg{Winner: before}
}

// winnerLocked is lookupAlias without the lock, for callers that already hold it.
func (r *Router) winnerLocked(label string) string {
	claims := r.aliases[label]
	if len(claims) == 0 {
		return ""
	}
	return claims[len(claims)-1].deployment
}

// ClearAliases forgets every registered alias at once — the deterministic teardown
// a conduit runs when its session ends, so no alias outlives the session even if a
// per-service withdrawal was missed.
func (r *Router) ClearAliases() {
	r.aliasMu.Lock()
	r.aliases = map[string][]aliasClaim{}
	r.aliasMu.Unlock()
}

// lookupAlias returns the deployment a label resolves to: the most recent live
// claim, or "" when nothing claims it. Contested labels resolve rather than fail —
// see Router's documentation for why the last claim wins.
func (r *Router) lookupAlias(label string) string {
	dep, _ := r.lookupAliasClaim(label)
	return dep
}

// claimOrRecovering answers both questions under one lock: what claims this label,
// and — if nothing does — whether the router is mid-recovery and so cannot yet
// distinguish an unknown name from one whose owner is about to re-register it.
func (r *Router) claimOrRecovering(label string) (string, portfwd.Dialer, bool) {
	r.aliasMu.RLock()
	defer r.aliasMu.RUnlock()
	claims := r.aliases[label]
	if len(claims) == 0 {
		return "", nil, r.recoveringLocked()
	}
	c := claims[len(claims)-1]
	return c.deployment, c.dialer, false
}

// isBareLabel reports whether host is a single-label name, the form a compose
// service is reached by. A dotted host that matched no rule is ordinary internet
// traffic.
func isBareLabel(host string) bool { return !strings.Contains(host, ".") }

// lookupAliasClaim returns the winning claim's deployment and its dialer.
func (r *Router) lookupAliasClaim(label string) (string, portfwd.Dialer) {
	r.aliasMu.RLock()
	defer r.aliasMu.RUnlock()
	claims := r.aliases[label]
	if len(claims) == 0 {
		return "", nil
	}
	c := claims[len(claims)-1]
	return c.deployment, c.dialer
}

// bareEnabled reports whether bare single-label alias matching is on.
func (r *Router) bareEnabled() bool {
	r.aliasMu.RLock()
	defer r.aliasMu.RUnlock()
	return r.bareServiceNames
}

// localSubject is the exact key a published name is stored under. Keying on
// "host:port" rather than host alone keeps every other port on that host falling
// through: "cornus.internal:443" stays a direct-egress target instead of tunneling
// TLS into a plaintext handler.
func localSubject(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}

// RegisterLocal publishes d under host:port, so a CONNECT to exactly that target
// is handed to d instead of being routed by the rules.
//
// Several publishers may claim one subject; the highest sequence serves it, and
// withdrawing that one restores the claim beneath. The returned handle withdraws
// exactly this claim — passing the subject alone could not, and an earlier
// publisher's teardown would remove whichever claim happened to be serving.
//
// An empty host, an out-of-range port, or a nil dialer is ignored and returns a
// zero LocalReg, whose Handle is not Valid.
func (r *Router) RegisterLocal(host string, port int, d LocalDialer) LocalReg {
	if host == "" || port < 1 || port > 65535 || d == nil {
		return LocalReg{}
	}
	r.aliasMu.Lock()
	defer r.aliasMu.Unlock()
	if r.seqNext == 0 {
		r.seqNext = 1
	}
	seq := r.seqNext
	r.seqNext++
	return r.publishLocked(host, port, d, seq)
}

// RegisterLocalSeq publishes d at an EXPLICIT precedence, for replaying a claim
// whose sequence was assigned by a router that no longer exists. See
// AliasSpec.Seq for why replaying without the original sequence reorders precedence
// by the reconnect race.
func (r *Router) RegisterLocalSeq(host string, port int, d LocalDialer, seq uint64) LocalReg {
	if host == "" || port < 1 || port > 65535 || d == nil || seq == 0 {
		return LocalReg{}
	}
	r.aliasMu.Lock()
	defer r.aliasMu.Unlock()
	if seq >= r.seqNext {
		r.seqNext = seq + 1
	}
	return r.publishLocked(host, port, d, seq)
}

// publishLocked inserts one claim in sequence order. Caller holds aliasMu.
func (r *Router) publishLocked(host string, port int, d LocalDialer, seq uint64) LocalReg {
	subject := localSubject(host, port)
	before := r.servingLocalLocked(subject)
	r.localIDNext++
	id := r.localIDNext

	claims := r.locals[subject]
	i := sort.Search(len(claims), func(i int) bool { return claims[i].seq > seq })
	claims = append(claims, localClaim{})
	copy(claims[i+1:], claims[i:])
	claims[i] = localClaim{d: d, seq: seq, id: id}
	r.locals[subject] = claims

	after := r.servingLocalLocked(subject)
	return LocalReg{
		Handle:  LocalHandle{subject: subject, id: id},
		Seq:     seq,
		Serving: after == id,
		Changed: after != before,
	}
}

// UnregisterLocal withdraws exactly the claim h identifies, restoring whichever
// claim was beneath it. An invalid or already-withdrawn handle is a no-op.
func (r *Router) UnregisterLocal(h LocalHandle) LocalReg {
	if !h.Valid() {
		return LocalReg{}
	}
	r.aliasMu.Lock()
	defer r.aliasMu.Unlock()
	before := r.servingLocalLocked(h.subject)
	claims := r.locals[h.subject]
	for i := range claims {
		if claims[i].id != h.id {
			continue
		}
		seq := claims[i].seq
		r.locals[h.subject] = append(claims[:i:i], claims[i+1:]...)
		if len(r.locals[h.subject]) == 0 {
			delete(r.locals, h.subject)
		}
		after := r.servingLocalLocked(h.subject)
		return LocalReg{Seq: seq, Changed: after != before}
	}
	return LocalReg{}
}

// servingLocalLocked is the id of the claim currently serving subject, 0 if none.
func (r *Router) servingLocalLocked(subject string) uint64 {
	claims := r.locals[subject]
	if len(claims) == 0 {
		return 0
	}
	return claims[len(claims)-1].id
}

// ClearLocals forgets every published name at once — the deterministic teardown a
// conduit runs when its session ends, mirroring ClearAliases.
func (r *Router) ClearLocals() {
	r.aliasMu.Lock()
	r.locals = map[string][]localClaim{}
	r.aliasMu.Unlock()
}

// lookupLocal returns the dialer currently serving host:port — the highest-sequence
// claim — or nil when nothing claims it.
func (r *Router) lookupLocal(host string, port int) LocalDialer {
	r.aliasMu.RLock()
	defer r.aliasMu.RUnlock()
	claims := r.locals[localSubject(host, port)]
	if len(claims) == 0 {
		return nil
	}
	return claims[len(claims)-1].d
}

// Recovering reports whether the router is inside a recovery window — a host has
// just taken over and the participants holding the other claims have not all
// re-registered.
//
// Exported for a caller that REPORTS claim movements: during recovery a claim
// arriving is a name being restored, not a name moving, and narrating each one
// would bury the case that matters (a member leaving and taking a short name with
// it) under a burst of noise after every takeover.
func (r *Router) Recovering() bool {
	r.aliasMu.RLock()
	defer r.aliasMu.RUnlock()
	return r.recoveringLocked()
}

// Resolve routes "host:port".
//
// A published local name is checked FIRST and wins outright (KindLocal). It must
// outrank the rules: the name a UI is published under is a reserved claim, and a
// rule would otherwise shadow it — a catch-all "^(.*)$" rule swallows everything,
// and even a service-host suffix spelled without its leading dot (an accepted
// configuration) makes the default rule match the suffix's own apex, rewrite it to
// ":port", and fail the CONNECT rather than fall through.
//
// Otherwise the rules are tried in order. On the first match it applies the
// replacement and splits the result into a service name and port; a registered
// alias then remaps the resulting service label to its real deployment name (so a
// suffix rule's "web" becomes "demo-web"). A match whose rewritten result is not a
// valid "service:port" returns KindService with a non-nil error (the CONNECT fails
// rather than leaking to direct egress). When no rule matches, a bare host that
// exactly matches an alias is routed inward; anything else returns KindDirect and a
// nil error (dial host:port directly).
func (r *Router) Resolve(host string, port int) (Result, error) {
	if d := r.lookupLocal(host, port); d != nil {
		return Result{Kind: KindLocal, Local: d}, nil
	}
	subject := host + ":" + strconv.Itoa(port)
	for _, rl := range r.rules {
		loc := rl.re.FindStringSubmatchIndex(subject)
		if loc == nil {
			continue
		}
		out := string(rl.re.ExpandString(nil, rl.repl, subject, loc))
		svc, p, err := splitServicePort(out)
		if err != nil {
			return Result{Kind: KindService}, fmt.Errorf("resolution rule rewrote %q to %q: %w", subject, out, err)
		}
		// Remap an unqualified service label onto its real deployment name; a label
		// with no alias (e.g. an already-qualified "demo-web") passes through, and
		// with it the proxy's own dialer.
		dep, d, recovering := r.claimOrRecovering(svc)
		if dep == "" && recovering {
			// Unclaimed while recovering. Passing it through would tunnel to a service
			// that does not exist and fail, which is the wrong answer to give when the
			// right one is moments away.
			return Result{Kind: KindPending, Label: svc, Port: p}, nil
		}
		if dep != "" {
			svc = dep
		}
		return Result{Kind: KindService, Service: svc, Port: p, Dialer: d}, nil
	}
	// No rule matched. A bare, single-label host is routed inward only when bare
	// aliasing is on and it names a registered service (the most recent claim, if
	// several); everything else egresses directly (the split).
	if r.bareEnabled() {
		dep, d, recovering := r.claimOrRecovering(host)
		if dep != "" {
			return Result{Kind: KindService, Service: dep, Port: port, Dialer: d}, nil
		}
		if recovering && isBareLabel(host) {
			// A bare single-label name is the compose-service form; letting it egress
			// during recovery would send a request meant for a workload out to public
			// DNS, which is the one wrong answer here that leaves the machine.
			//
			// Restricted to single-label names deliberately: a dotted host that matched
			// no rule is ordinary internet traffic and must keep egressing immediately,
			// or a takeover would stall every unrelated request the browser makes.
			return Result{Kind: KindPending, Label: host, Port: port}, nil
		}
	}
	return Result{Kind: KindDirect}, nil
}

// splitServicePort splits a rewritten "service:port" on the last colon and
// validates both halves (non-empty service, port in 1..65535).
func splitServicePort(s string) (string, int, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("no port")
	}
	svc, portStr := s[:i], s[i+1:]
	if svc == "" {
		return "", 0, fmt.Errorf("empty service name")
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("port %q is not a number", portStr)
	}
	if p < 1 || p > 65535 {
		return "", 0, fmt.Errorf("port %d out of range (1-65535)", p)
	}
	return svc, p, nil
}

// translateReplace converts a sed-style replacement template (\1, \2, ...) into
// Go regexp's $-form for Regexp.Expand: a backslash-digit becomes ${N}, any other
// backslash-escape drops the backslash, and a literal $ is escaped to $$ so it is
// not read as a group reference.
func translateReplace(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '$':
			b.WriteString("$$")
		case '\\':
			if i+1 < len(s) {
				n := s[i+1]
				if n >= '0' && n <= '9' {
					b.WriteString("${")
					b.WriteByte(n)
					b.WriteByte('}')
				} else {
					b.WriteByte(n)
				}
				i++
				continue
			}
			b.WriteByte('\\')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Option configures Start.
type Option func(*options)

type options struct {
	logf             func(format string, args ...any)
	direct           DirectDialer
	handshakeTimeout time.Duration
	allowNonLoopback bool
}

// WithLogf routes non-fatal per-connection warnings (default: slog warnings).
func WithLogf(logf func(format string, args ...any)) Option {
	return func(o *options) { o.logf = logf }
}

// WithDirectDialer overrides the dialer used for unmatched (direct-egress)
// targets. The default is &net.Dialer{}; tests inject a fake.
func WithDirectDialer(d DirectDialer) Option {
	return func(o *options) {
		if d != nil {
			o.direct = d
		}
	}
}

// WithHandshakeTimeout overrides how long an accepted connection may take to
// finish the SOCKS5 negotiation and CONNECT before it is reaped (default
// DefaultHandshakeTimeout). A non-positive d disables the deadline. Mainly a
// testing seam.
func WithHandshakeTimeout(d time.Duration) Option {
	return func(o *options) { o.handshakeTimeout = d }
}

// WithAllowNonLoopback permits binding the proxy to a non-loopback address.
//
// Start refuses one by default, because this proxy performs no authentication
// (the SOCKS5 no-auth method is the only one offered) and dials arbitrary
// unmatched destinations from the host it runs on: reachable off-host, it is an
// open proxy for anyone who can route to it, including into this machine's own
// loopback services. Only pass this when the caller has explicitly asked for it
// and understands that; a non-loopback proxy additionally refuses to dial
// loopback and link-local destinations (see loopbackGuard), so it cannot be used
// to pivot into services on the proxy host.
func WithAllowNonLoopback(allow bool) Option {
	return func(o *options) { o.allowNonLoopback = allow }
}

// LooksNonLoopback reports whether a listen-address string will OBVIOUSLY bind a
// non-loopback interface, judging the literal host without binding it. It is a
// cheap pre-flight so a caller (the conduit config layer) can reject a misconfigured
// non-loopback bind early with a friendly, config-oriented error, instead of only at
// Start's post-bind check.
//
// It returns true for a wildcard bind (":port" / "0.0.0.0" / "::" / "*") or a literal
// non-loopback IP, and false for an empty string (Start uses the loopback DefaultListen),
// a loopback literal, or a hostname. A hostname's true landing is only known after
// binding, so LooksNonLoopback defers it to false and Start's authoritative post-bind
// loopbackAddr check remains the backstop (it also catches a poisoned "localhost").
func LooksNonLoopback(addr string) bool {
	if addr == "" {
		return false // Start substitutes the loopback DefaultListen.
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	switch host {
	case "": // ":port" — binds every interface.
		return true
	case "*":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback()
	}
	return false // A hostname: Start's post-bind check decides.
}

// loopbackAddr reports whether a bound listener address is loopback-only.
// It reads the address the kernel actually bound rather than the requested string,
// so a hostname (or a poisoned "localhost") is judged by where it truly landed and
// a wildcard bind ("" / "0.0.0.0" / "::") is correctly rejected as unspecified.
func loopbackAddr(a net.Addr) bool {
	ta, ok := a.(*net.TCPAddr)
	if !ok || ta.IP == nil {
		return false
	}
	return ta.IP.IsLoopback()
}

// loopbackGuard wraps the direct-egress dialer for a proxy bound off-host, and
// refuses connections that landed on the proxy host's own loopback or on a
// link-local address. It checks the established connection's remote address rather
// than resolving the requested name first, so a name that resolves to 127.0.0.1
// (or re-resolves between check and dial) cannot slip through.
type loopbackGuard struct{ inner DirectDialer }

func (g loopbackGuard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c, err := g.inner.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	ta, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok || ta.IP == nil {
		return c, nil
	}
	if ta.IP.IsLoopback() || ta.IP.IsLinkLocalUnicast() || ta.IP.IsLinkLocalMulticast() || ta.IP.IsUnspecified() {
		_ = c.Close()
		return nil, fmt.Errorf("socks5: refusing to dial %s (%s): a non-loopback proxy may not reach the proxy host's loopback or link-local addresses", address, ta.IP)
	}
	return c, nil
}

// Proxy is one live SOCKS5 listener. Its lifetime mirrors portfwd.Group: it
// closes itself when the Start ctx ends, Close tears it down earlier, and in-
// flight connections are severed on teardown.
type Proxy struct {
	ln               net.Listener
	ownsListener     bool // false when Serve was handed a listener the caller keeps
	router           *Router
	dialer           portfwd.Dialer
	direct           DirectDialer
	logf             func(format string, args ...any)
	handshakeTimeout time.Duration
	cancel           context.CancelFunc
	wg               sync.WaitGroup

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	done  bool
}

// Start binds a SOCKS5 listener on addr (DefaultListen when empty) and serves
// CONNECT requests, routing each via router: a matched target tunnels into the
// workload through d.PortForward, an unmatched target is dialed directly (the
// split tunnel). The proxy runs until ctx is cancelled or Close is called, and it
// OWNS the listener it bound — closing the proxy closes it.
func Start(ctx context.Context, d portfwd.Dialer, router *Router, addr string, opts ...Option) (*Proxy, error) {
	if addr == "" {
		addr = DefaultListen
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("socks5: listen on %s: %w", addr, err)
	}
	p, err := serve(ctx, d, router, ln, true, "bind", opts...)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	return p, nil
}

// Serve runs the proxy on a listener the caller already bound, and does NOT close
// it: the caller owns it and closes it when it chooses.
//
// It exists because the bind and the serving have different owners once conduits
// are addressable. The listener has to be bound under the rendezvous lock — that is
// what makes create-or-join atomic — and it has to outlive this proxy, because a
// replica of it is handed to every joiner so ownership of the address can move
// without the address ever going down. A proxy that bound its own socket could
// provide neither.
//
// The non-loopback refusal still applies, and still judges the address the KERNEL
// bound rather than any string: an unauthenticated proxy that dials arbitrary
// destinations is an open proxy off-host however the socket came to exist. On
// refusal the listener is left open, because Serve did not open it.
func Serve(ctx context.Context, d portfwd.Dialer, router *Router, ln net.Listener, opts ...Option) (*Proxy, error) {
	if ln == nil {
		return nil, fmt.Errorf("socks5: nil listener")
	}
	return serve(ctx, d, router, ln, false, "serve", opts...)
}

// verb names what is being refused, so the message describes what the caller
// actually asked for: Start bound the socket and is rejecting the bind, while Serve
// was handed one and is rejecting only its own participation.
func serve(ctx context.Context, d portfwd.Dialer, router *Router, ln net.Listener, ownsListener bool, verb string, opts ...Option) (*Proxy, error) {
	log := logging.FromContext(ctx)
	o := options{
		logf:             func(format string, args ...any) { log.WarnContext(ctx, fmt.Sprintf(format, args...)) },
		direct:           &net.Dialer{},
		handshakeTimeout: DefaultHandshakeTimeout,
	}
	for _, opt := range opts {
		opt(&o)
	}
	// Judge the address the kernel actually bound, not the requested string.
	direct := o.direct
	if !loopbackAddr(ln.Addr()) {
		if !o.allowNonLoopback {
			return nil, fmt.Errorf("socks5: refusing to %s %s: this proxy has no authentication and dials arbitrary destinations from this host, so a non-loopback listener is an open proxy for anyone who can reach it (bind a loopback address, or pass the explicit opt-in if you really mean it)", verb, ln.Addr())
		}
		// Explicitly allowed off-host: at least deny the loopback pivot, so a remote
		// client cannot use the proxy to reach this machine's own services.
		direct = loopbackGuard{inner: direct}
	}
	fctx, cancel := context.WithCancel(ctx)
	p := &Proxy{
		ln:               ln,
		ownsListener:     ownsListener,
		router:           router,
		dialer:           d,
		direct:           direct,
		logf:             o.logf,
		handshakeTimeout: o.handshakeTimeout,
		cancel:           cancel,
		conns:            map[net.Conn]struct{}{},
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.serve(fctx)
		// Hand a borrowed listener back in the state it was lent in. Shutdown wakes
		// the accept loop with a past deadline, which is the only way to unblock
		// Accept without closing the socket — but a deadline persists, so leaving it
		// set would make every later Accept by the OWNER fail instantly. The socket is
		// meant to outlive this proxy and be served by a successor, so clearing it is
		// part of giving it back.
		p.clearBorrowedDeadline()
	}()
	// Tie the proxy's lifetime to ctx so a caller holding a session needs no
	// explicit Close on the cancel path.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		<-fctx.Done()
		p.shutdown()
	}()
	return p, nil
}

// Addr is the actually-bound local listen address (meaningful when addr used
// port 0).
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// Close tears the proxy down: the listener closes, in-flight connections are
// severed, and serving goroutines drain. Idempotent.
func (p *Proxy) Close() {
	p.cancel()
	p.shutdown()
	p.wg.Wait()
}

func (p *Proxy) serve(ctx context.Context) {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		if !p.track(c) {
			_ = c.Close()
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer p.untrack(c)
			defer c.Close()
			p.handle(ctx, c)
		}()
	}
}

// handle runs the SOCKS5 no-auth handshake and one CONNECT, then routes and
// splices. Protocol errors and dial failures are logged, not fatal to the proxy.
func (p *Proxy) handle(ctx context.Context, c net.Conn) {
	// Bound the negotiation + CONNECT reads so a client that connects but sends
	// nothing is reaped instead of parking a goroutine and FD forever. The
	// deadline is cleared before splicing (below) so an idle-but-established
	// tunnel is not torn down.
	if p.handshakeTimeout > 0 {
		_ = c.SetReadDeadline(time.Now().Add(p.handshakeTimeout))
	}
	if err := serveHandshake(c); err != nil {
		p.logf("socks5: handshake from %s failed: %v", c.RemoteAddr(), err)
		return
	}
	host, port, err := readConnect(c)
	if err != nil {
		p.logf("socks5: connect from %s failed: %v", c.RemoteAddr(), err)
		return
	}
	if host == "" {
		// A zero-length domain would otherwise dial ":port" (the proxy host's own
		// localhost); reject it.
		p.logf("socks5: empty destination host from %s", c.RemoteAddr())
		_ = writeReply(c, repHostUnreachable)
		return
	}

	res, rerr := p.router.Resolve(host, port)
	if rerr != nil {
		p.logf("socks5: %v", rerr)
		_ = writeReply(c, repHostUnreachable)
		return
	}

	var upstream net.Conn
	// A conduit-shaped name with no claim yet, during a recovery window: WAIT for the
	// claim rather than answer wrongly. The caller sees latency, which is the one
	// outcome that is neither a failure nor a request sent somewhere it was never
	// meant to go — and it mirrors what the kernel already does a layer down, where a
	// connection made while nobody owns the listening socket queues instead of being
	// refused.
	//
	// The wait is bounded by the recovery window, so a name nothing will ever claim
	// costs that latency once and then answers as it always would.
	if res.Kind == KindPending {
		claimed := p.router.AwaitClaim(ctx, res.Label)
		res, err = p.router.Resolve(host, port)
		if err != nil {
			p.logf("socks5: routing %s:%d failed: %v", host, port, err)
			_ = writeReply(c, repHostUnreachable)
			return
		}
		if !claimed && res.Kind == KindPending {
			// The window closed with nothing claiming it. Fall back to what the router
			// would have said all along.
			res = Result{Kind: KindDirect}
		}
	}

	switch res.Kind {
	case KindLocal:
		// A published name: hand off to the in-process listener. Neither the
		// port-forward transport nor the direct dialer is involved — there is no
		// address to dial.
		upstream, err = res.Local.DialLocal(ctx)
		if err != nil {
			p.logf("socks5: local handoff for %s:%d failed: %v", host, port, err)
			_ = writeReply(c, repHostUnreachable)
			return
		}
	case KindService:
		// The claim's own dialer when it has one, so a consolidated conduit reaches
		// each project through the server that project is actually talking to.
		dialer := res.Dialer
		if dialer == nil {
			dialer = p.dialer
		}
		upstream, err = dialer.PortForward(ctx, res.Service, res.Port, "tcp")
		if err != nil {
			p.logf("socks5: tunnel to %s:%d failed: %v", res.Service, res.Port, err)
			_ = writeReply(c, repHostUnreachable)
			return
		}
	default:
		upstream, err = p.direct.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			p.logf("socks5: direct dial to %s:%d failed: %v", host, port, err)
			_ = writeReply(c, repHostUnreachable)
			return
		}
	}
	if !p.track(upstream) {
		_ = upstream.Close()
		return
	}
	defer p.untrack(upstream)
	defer upstream.Close()
	if err := writeReply(c, repSucceeded); err != nil {
		return
	}
	// Negotiation is done: clear the handshake read deadline so the spliced
	// connection can idle without being reaped.
	if p.handshakeTimeout > 0 {
		_ = c.SetReadDeadline(time.Time{})
	}
	wire.Pipe(c, upstream)
}

// shutdown closes the listener and severs in-flight connections exactly once.
// clearBorrowedDeadline restores a listener this proxy did not open, so its owner
// can go on accepting after the proxy stops. A no-op when the proxy owns it.
func (p *Proxy) clearBorrowedDeadline() {
	if p.ownsListener {
		return
	}
	if d, ok := p.ln.(interface{ SetDeadline(time.Time) error }); ok {
		_ = d.SetDeadline(time.Time{})
	}
}

func (p *Proxy) shutdown() {
	p.mu.Lock()
	if p.done {
		p.mu.Unlock()
		return
	}
	p.done = true
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()

	// Only close a listener this proxy opened. When it was handed one, closing it
	// would tear down an address the caller may be about to hand to a successor —
	// the whole point of Serve is that the socket outlives any one proxy.
	if p.ownsListener {
		_ = p.ln.Close()
	} else {
		// Wake the accept loop without closing the socket: it is another owner's, and
		// closing it would take down an address that is meant to survive this proxy.
		// The deadline is cleared again once the loop has exited (see clearBorrowedDeadline).
		if d, ok := p.ln.(interface{ SetDeadline(time.Time) error }); ok {
			_ = d.SetDeadline(time.Now())
		}
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// track registers an in-flight conn for severing on Close. It reports false — and
// does not register — when the proxy is already shutting down.
func (p *Proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *Proxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

// SOCKS5 wire constants (RFC 1928).
const (
	socksVersion = 0x05
	methodNoAuth = 0x00
	methodNone   = 0xFF

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSucceeded        = 0x00
	repHostUnreachable  = 0x04
	repCmdNotSupported  = 0x07
	repAddrNotSupported = 0x08
)

// serveHandshake reads the client's method-negotiation greeting and selects the
// no-auth method (the only one offered). It errors — after replying "no
// acceptable methods" — when the client does not offer no-auth.
func serveHandshake(c net.Conn) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c, header); err != nil {
		return err
	}
	if header[0] != socksVersion {
		return fmt.Errorf("unsupported version %d", header[0])
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return err
	}
	for _, m := range methods {
		if m == methodNoAuth {
			_, err := c.Write([]byte{socksVersion, methodNoAuth})
			return err
		}
	}
	_, _ = c.Write([]byte{socksVersion, methodNone})
	return fmt.Errorf("client offered no no-auth method")
}

// readConnect reads a CONNECT request and returns its destination host and port.
// A non-CONNECT command or an unsupported address type is answered with the
// matching SOCKS5 error reply before returning an error.
func readConnect(c net.Conn) (string, int, error) {
	header := make([]byte, 4) // VER, CMD, RSV, ATYP
	if _, err := io.ReadFull(c, header); err != nil {
		return "", 0, err
	}
	if header[0] != socksVersion {
		return "", 0, fmt.Errorf("unsupported version %d", header[0])
	}
	if header[1] != cmdConnect {
		_ = writeReply(c, repCmdNotSupported)
		return "", 0, fmt.Errorf("unsupported command %d (only CONNECT)", header[1])
	}

	var host string
	switch header[3] {
	case atypIPv4:
		b := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case atypIPv6:
		b := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case atypDomain:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return "", 0, err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return "", 0, err
		}
		host = string(b)
	default:
		_ = writeReply(c, repAddrNotSupported)
		return "", 0, fmt.Errorf("unsupported address type %d", header[3])
	}

	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return "", 0, err
	}
	return host, int(pb[0])<<8 | int(pb[1]), nil
}

// writeReply writes a SOCKS5 reply with a zero BND.ADDR/BND.PORT (a bound
// address the CONNECT client ignores).
func writeReply(c net.Conn, rep byte) error {
	_, err := c.Write([]byte{socksVersion, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}
