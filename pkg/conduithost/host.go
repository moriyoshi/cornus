package conduithost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"cornus/pkg/listenerpass"
)

// helloTimeout bounds how long a freshly accepted connection may take to send its
// hello. A peer that connects and then says nothing would otherwise park an
// accept goroutine and its descriptor for the conduit's whole life — the same
// unbounded-resource shape socks5.DefaultHandshakeTimeout guards against, and the
// same one the agent's requestReadTimeout guards against.
const helloTimeout = 30 * time.Second

// Withdraw undoes one registration. It must be safe to call more than once: a
// caller's explicit OpWithdraw and the implicit teardown when the connection drops
// can both reach the same registration.
type Withdraw func()

// Registrar applies the registrations a host accepts. It is the seam between this
// package — which knows about rendezvous, sockets, and lifetimes — and the caller,
// which knows what a registration MEANS. pkg/clientconduit supplies the real one,
// backed by a socks5.Router; tests supply a fake.
//
// Payload is opaque here by design: putting service specs, ingress specs, or
// port mappings in this package would drag pkg/api and the client packages into
// something that ought to stay a transport.
type Registrar interface {
	Register(ctx context.Context, reg Registration) (Withdraw, error)
}

// Recoverer is an optional Registrar capability: the ability to hold requests for
// a name whose claim has not been restored yet.
//
// It is separate from Registrar because a registrar that cannot do it still works —
// the conduit serves, and the only cost is that during the seconds after a takeover
// an unrestored name is answered as unknown rather than waited for. Takeover says so
// when it finds a registrar without it, rather than degrading quietly.
type Recoverer interface {
	// BeginRecovery declares that the routing table is incomplete until at most
	// until, so a conduit-shaped name with no claim should wait rather than be
	// answered. Implementations are expected to release every waiter when the
	// deadline passes, so an unknown name costs one window of latency and no more.
	BeginRecovery(until time.Time)
}

// Registration is one claim applied to a conduit.
type Registration struct {
	// Kind selects which of the caller's registration shapes Payload holds.
	Kind    string
	Payload json.RawMessage
	// Seq is the claim's precedence, assigned by whichever host first accepted it and
	// preserved across every later host. A registrar that orders claims must order
	// them by this and not by arrival.
	Seq  uint64
	Peer Peer
}

// Peer is what the host knows about a registering participant.
type Peer struct {
	// Pid is self-reported (see HelloRequest.Pid). Local is true for the host's own
	// registrations, which never cross a socket.
	Pid   int
	Local bool
}

// Host is a conduit this process bound. It owns the conduit's real listener, the
// control socket beside it, and every registration made against it.
type Host struct {
	addr     Addr
	ln       net.Listener // the conduit's own listener (the socks5 bind)
	ctl      net.Listener // control socket; nil for an ephemeral, unadvertised conduit
	reg      Registrar
	registry *Registry
	settings json.RawMessage
	banner   []string
	logf     func(format string, args ...any)

	// accept is the exclusive right to accept on ln, held for as long as this
	// process hosts. nil only for an ephemeral conduit, which nobody else can find
	// and so cannot contend for.
	accept *lease

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	seqNext uint64              // next precedence to assign
	own     map[string]Withdraw // this process's own registrations
	// conns tracks accepted control connections. Closing the LISTENER does not
	// disturb connections already accepted, so without this Close would block
	// forever waiting on handle goroutines parked in Decode — which is exactly what
	// a host with a live joiner always has.
	conns map[net.Conn]struct{}
}

// track registers an accepted connection, reporting false if the host is already
// closing so the caller hangs up instead of serving a connection nothing will
// ever tear down.
func (h *Host) track(c net.Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.conns[c] = struct{}{}
	return true
}

func (h *Host) untrack(c net.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, c)
}

// Addr is the conduit's normalized bind address.
func (h *Host) Addr() Addr { return h.addr }

// Listener is the conduit's real listener — the one a proxy serves on. The caller
// runs its own accept loop; this package only owns the binding, so that the bind
// happens under the rendezvous lock and so the listener is available to replicate
// to joiners later.
func (h *Host) Listener() net.Listener { return h.ln }

// Hosting reports that this participant bound the address. Always true for a Host;
// it exists so callers can treat Host and Joiner through one interface.
func (h *Host) Hosting() bool { return true }

// Settings are the conduit's canonical settings — for a host, its own.
func (h *Host) Settings() json.RawMessage { return h.settings }

// Banner is the session-level description to print.
func (h *Host) Banner() []string { return h.banner }

// Done is closed when the conduit stops. For a host that is its own Close, so it
// exists only to satisfy the same interface a Joiner implements, where it carries
// real news.
func (h *Host) Done() <-chan struct{} { return h.ctx.Done() }

// Register applies one registration locally. It does NOT go through the control
// socket: dialing our own listener to talk to ourselves would add a hop, a
// failure mode, and a deadlock risk during shutdown for no gain. The agent already
// makes this distinction with agentSelfView (cmd/cornus/internal/clientagent/web.go).
func (h *Host) Register(ctx context.Context, id, kind string, payload json.RawMessage) (Withdraw, error) {
	_, w, err := h.register(ctx, id, RegisterRequest{Kind: kind, Payload: payload}, Peer{Pid: os.Getpid(), Local: true})
	if err != nil {
		return nil, err
	}
	return w, nil
}

// RegisterAt is Register with an explicit precedence, for replaying a claim whose
// sequence a previous host assigned.
func (h *Host) RegisterAt(ctx context.Context, id, kind string, payload json.RawMessage, seq uint64) (Withdraw, error) {
	_, w, err := h.register(ctx, id, RegisterRequest{Kind: kind, Payload: payload, Seq: seq}, Peer{Pid: os.Getpid(), Local: true})
	return w, err
}

// nextSeq assigns a precedence, or adopts an explicit one and keeps the counter
// above it — so a claim made after a takeover outranks everything inherited rather
// than landing silently underneath it.
func (h *Host) nextSeq(explicit uint64) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if explicit != 0 {
		if explicit >= h.seqNext {
			h.seqNext = explicit + 1
		}
		return explicit
	}
	if h.seqNext == 0 {
		h.seqNext = 1
	}
	seq := h.seqNext
	h.seqNext++
	return seq
}

func (h *Host) register(ctx context.Context, id string, req RegisterRequest, peer Peer) (uint64, Withdraw, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return 0, nil, errors.New("conduit is closed")
	}
	h.mu.Unlock()

	seq := h.nextSeq(req.Seq)
	w, err := h.reg.Register(ctx, Registration{Kind: req.Kind, Payload: req.Payload, Seq: seq, Peer: peer})
	if err != nil {
		return 0, nil, err
	}
	once := onceWithdraw(w)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		once()
		return 0, nil, errors.New("conduit is closed")
	}
	h.own[id] = once
	h.mu.Unlock()
	return seq, func() {
		h.mu.Lock()
		delete(h.own, id)
		h.mu.Unlock()
		once()
	}, nil
}

// Unregister withdraws this process's own registration made under id.
func (h *Host) Unregister(_ context.Context, id string) error {
	h.mu.Lock()
	w := h.own[id]
	delete(h.own, id)
	h.mu.Unlock()
	if w != nil {
		w()
	}
	return nil
}

// serve accepts control connections until the host is closed.
func (h *Host) serve() {
	defer h.wg.Done()
	for {
		c, err := h.ctl.Accept()
		if err != nil {
			return // closed
		}
		if !h.track(c) {
			_ = c.Close()
			return
		}
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			defer h.untrack(c)
			h.handle(c)
		}()
	}
}

// handle serves one joiner for the life of its connection, and — this is the
// point — withdraws everything it registered when that connection ends.
//
// The kernel is the liveness authority. A joiner that is SIGKILLed cannot send a
// withdrawal, and the host does not own the joiner's process, so nothing else
// could observe its death; the closed descriptor is the one signal that is always
// delivered. This is the same contract the agent's web-serve already relies on
// (cmd/cornus/internal/clientagent/agent.go:187-192).
func (h *Host) handle(c net.Conn) {
	defer c.Close()
	held := map[string]Withdraw{}
	defer func() {
		for _, w := range held {
			w()
		}
	}()

	dec := json.NewDecoder(c)
	enc := json.NewEncoder(c)

	// The first frame is bounded; everything after it is not, because a joiner
	// legitimately sits idle for the whole session between registrations.
	_ = c.SetReadDeadline(time.Now().Add(helloTimeout))
	var first Frame
	if err := dec.Decode(&first); err != nil {
		return
	}
	if first.Op == OpPing {
		// Answer and hang up. Deliberately handled before the hello handshake so a
		// prober needs to know nothing about versions: a host that has become
		// unresponsive must be distinguishable from one that merely speaks a different
		// protocol version, and conflating them would reap a healthy conduit.
		_ = enc.Encode(Reply{ID: first.ID, OK: true})
		return
	}
	if first.Op == OpAdopt {
		// A handoff connection: replicate the listener and hang up. It registers
		// nothing, so the deferred withdrawals above are a no-op for it.
		h.handleAdopt(c, first)
		return
	}
	peer, err := h.readHello(first, enc)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})

	for {
		var f Frame
		if err := dec.Decode(&f); err != nil {
			return // EOF, reset, or garbage: the deferred withdrawals run
		}
		reply := h.dispatch(f, peer, held)
		reply.ID = f.ID
		if err := enc.Encode(reply); err != nil {
			return
		}
	}
}

// notify reports a degradation through the configured Logf, or slog by default.
func (h *Host) notify(format string, args ...any) {
	if h.logf != nil {
		h.logf(format, args...)
		return
	}
	defaultLogf(format, args...)
}

// handleAdopt replicates the conduit's listener onto this connection.
//
// Failure is reported and then dropped: replication is what allows ownership to
// MIGRATE when this host dies, and a conduit that cannot migrate still serves
// perfectly well. Refusing the join over it would trade a working conduit for no
// conduit, which is the wrong way round.
func (h *Host) handleAdopt(c net.Conn, f Frame) {
	var req AdoptRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return
		}
	}
	if !listenerpass.Supported() {
		return
	}
	if err := listenerpass.Send(c, h.ln, listenerpass.Peer{Pid: req.Pid}); err != nil {
		h.notify("conduit %s: could not replicate the listener to pid %d (ownership cannot migrate from this host): %v", h.addr, req.Pid, err)
	}
}

func (h *Host) readHello(f Frame, enc *json.Encoder) (Peer, error) {
	if f.Op != OpHello {
		_ = enc.Encode(Reply{ID: f.ID, Error: fmt.Sprintf("first frame must be %q, got %q", OpHello, f.Op)})
		return Peer{}, errors.New("no hello")
	}
	var req HelloRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			_ = enc.Encode(Reply{ID: f.ID, Error: "malformed hello: " + err.Error()})
			return Peer{}, err
		}
	}
	if req.Version != ProtocolVersion {
		// Refuse rather than adapt: a joiner that speaks another version would
		// otherwise register frames this host misreads, and a conduit is shared
		// state where that lands on someone else's traffic.
		msg := fmt.Sprintf("conduit control protocol version %d, joiner speaks %d (the two cornus builds differ; use one build for both)", ProtocolVersion, req.Version)
		_ = enc.Encode(Reply{ID: f.ID, Error: msg})
		return Peer{}, errors.New(msg)
	}
	resp := HelloResponse{
		Version:  ProtocolVersion,
		Bind:     h.addr.String(),
		Settings: h.settings,
		Banner:   h.banner,
		HostPid:  os.Getpid(),
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return Peer{}, err
	}
	if err := enc.Encode(Reply{ID: f.ID, OK: true, Payload: b}); err != nil {
		return Peer{}, err
	}
	return Peer{Pid: req.Pid}, nil
}

func (h *Host) dispatch(f Frame, peer Peer, held map[string]Withdraw) Reply {
	switch f.Op {
	case OpRegister:
		if f.ID == "" {
			return Reply{Error: "register: missing id"}
		}
		if _, dup := held[f.ID]; dup {
			return Reply{Error: fmt.Sprintf("register: id %q already in use on this connection", f.ID)}
		}
		var req RegisterRequest
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return Reply{Error: "register: malformed payload: " + err.Error()}
		}
		seq := h.nextSeq(req.Seq)
		w, err := h.reg.Register(h.ctx, Registration{Kind: req.Kind, Payload: req.Payload, Seq: seq, Peer: peer})
		if err != nil {
			return Reply{Error: err.Error()}
		}
		held[f.ID] = onceWithdraw(w)
		body, err := json.Marshal(RegisterResponse{Seq: seq})
		if err != nil {
			return Reply{Error: err.Error()}
		}
		return Reply{OK: true, Payload: body}
	case OpWithdraw:
		if w := held[f.ID]; w != nil {
			delete(held, f.ID)
			w()
		}
		// Withdrawing an unknown id succeeds: teardown is idempotent by design, and a
		// joiner racing its own connection close must not see a spurious failure.
		return Reply{OK: true}
	case OpHello:
		return Reply{Error: "hello may only be the first frame"}
	default:
		return Reply{Error: "unknown op: " + f.Op}
	}
}

// Close stops hosting: the control socket and the conduit listener close, joiners
// see EOF and withdraw, this process's own registrations are undone, and the
// advertisement is removed so the address is immediately reusable.
func (h *Host) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	own := h.own
	h.own = map[string]Withdraw{}
	conns := make([]net.Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.conns = map[net.Conn]struct{}{}
	h.mu.Unlock()

	h.cancel()
	if h.ctl != nil {
		_ = h.ctl.Close()
	}

	// Sever accepted connections too. Each handle goroutine is parked in Decode and
	// only a closed descriptor wakes it; it then runs its deferred withdrawals, so
	// this is also what makes teardown withdraw every joiner's registrations.
	for _, c := range conns {
		_ = c.Close()
	}
	err := h.ln.Close()
	// Release the accepting right only AFTER the listener is closed. The caller runs
	// the accept loop on this listener, so releasing first would open a window in
	// which a follower acquires the lease and starts accepting while the outgoing
	// caller is still inside Accept — two accepters on one socket, which is the exact
	// state the lease exists to make impossible.
	h.accept.release()
	for _, w := range own {
		w()
	}
	h.wg.Wait()
	if h.ctl != nil {
		h.registry.removeEntry(h.addr)
	}
	return err
}

// onceWithdraw makes a withdrawal idempotent, so an explicit OpWithdraw followed
// by the connection's implicit teardown does not run it twice.
func onceWithdraw(w Withdraw) Withdraw {
	if w == nil {
		return func() {}
	}
	var once sync.Once
	return func() { once.Do(w) }
}
