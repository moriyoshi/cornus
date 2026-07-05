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

// joinTimeout bounds dialing an advertised control socket and completing the
// hello. It is held while the port lock is taken, so a wedged host must not be
// able to block every other process on this port indefinitely.
const joinTimeout = 10 * time.Second

// ErrHostGone reports that the conduit's host exited. It is a distinct error
// because it is not a failure of the operation that hit it — the conduit is
// simply no longer there, and the caller's response is to re-open the rendezvous
// (electing a new host) rather than to retry the registration.
var ErrHostGone = errors.New("conduit host exited")

// Joiner is a participant in a conduit hosted by another process.
type Joiner struct {
	addr     Addr
	conn     net.Conn
	socket   string
	settings json.RawMessage
	banner   []string
	hostPid  int
	cfg      Config
	// replica is this process's own reference to the conduit's listening socket,
	// obtained at JOIN time rather than at the host's exit. A host that is SIGKILLed
	// hands nothing to anyone, so taking the copy up front is the only way the
	// address can survive an arbitrary death — while any participant holds a
	// reference, the socket stays bound and its backlog stays alive, so a client
	// dialing across the handover is never refused. nil when replication is
	// unsupported or failed, which costs migration and nothing else.
	replica net.Listener

	enc *json.Encoder
	// dec is created ONCE and shared by the hello handshake and the reader
	// goroutine. A json.Decoder buffers ahead, so a second decoder built on the same
	// connection can silently lose whatever the first one had already read — a
	// corruption that only shows up when replies happen to arrive back-to-back, i.e.
	// under load and never in a simple test.
	dec *json.Decoder

	mu      sync.Mutex
	pending map[string]chan Reply
	closed  bool
	nextID  uint64
	// regs remembers what this participant registered, so a takeover can replay it.
	// Registrations are scoped to the control connection and therefore die WITH the
	// host; every survivor has to re-apply its own, and only the survivor still knows
	// what they were.
	regs     map[string]RegisterRequest
	regOrder []string
	tookOver bool

	done     chan struct{}
	doneOnce sync.Once
	readErr  error
}

// Addr is the conduit's bind address — the incumbent's, which after consolidation
// is legitimately not the address this process asked for.
func (j *Joiner) Addr() Addr { return j.addr }

// Hosting reports that this participant did not bind the address.
func (j *Joiner) Hosting() bool { return false }

// Settings are the conduit's canonical settings, as decided by whoever created it.
func (j *Joiner) Settings() json.RawMessage { return j.settings }

// Banner is the host's session-level description, so a joiner prints the line the
// host would rather than one describing settings it did not get.
func (j *Joiner) Banner() []string { return j.banner }

// HostPid names the process to blame when this conduit dies.
func (j *Joiner) HostPid() int { return j.hostPid }

// Done is closed when the control connection ends — normally because the host
// exited. A caller watching this is how takeover starts.
func (j *Joiner) Done() <-chan struct{} { return j.done }

// Err reports why the connection ended, once Done is closed.
func (j *Joiner) Err() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.readErr
}

// dialJoin connects to an advertised conduit and completes the hello.
func dialJoin(a Addr, socket string, cfg Config) (*Joiner, error) {
	c, err := net.DialTimeout("unix", socket, joinTimeout)
	if err != nil {
		return nil, fmt.Errorf("join conduit at %s: %w", a, err)
	}
	j := &Joiner{
		addr:    a,
		conn:    c,
		socket:  socket,
		cfg:     cfg,
		enc:     json.NewEncoder(c),
		dec:     json.NewDecoder(c),
		pending: map[string]chan Reply{},
		regs:    map[string]RegisterRequest{},
		done:    make(chan struct{}),
	}
	if err := j.hello(); err != nil {
		_ = c.Close()
		return nil, err
	}
	j.replica = fetchReplica(socket, j.logf)
	go j.read()
	return j, nil
}

// fetchReplica asks the host for a reference to its listening socket, on a
// connection used for nothing else.
//
// The separate connection is load-bearing, not tidy: on unix the descriptor rides
// as ancillary data attached to particular bytes, and the main control connection
// has a json.Decoder reading ahead on it. Sharing one connection would work right
// up until two frames arrived back-to-back — under load, never in a test.
//
// Every failure here is soft. Without a replica this participant cannot take
// ownership if the host dies, but it can still register, still resolve, and still
// use the conduit exactly as before.
func fetchReplica(socket string, logf func(string, ...any)) net.Listener {
	if !listenerpass.Supported() {
		return nil
	}
	c, err := net.DialTimeout("unix", socket, joinTimeout)
	if err != nil {
		logf("conduit: could not open a handoff connection (this process cannot take over if the host dies): %v", err)
		return nil
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(joinTimeout))
	payload, err := json.Marshal(AdoptRequest{Pid: os.Getpid()})
	if err != nil {
		return nil
	}
	// Write only. No decoder is ever created on this connection, which is precisely
	// what keeps the ancillary data from being read past.
	if err := json.NewEncoder(c).Encode(Frame{Op: OpAdopt, Payload: payload}); err != nil {
		logf("conduit: requesting a listener replica: %v", err)
		return nil
	}
	ln, err := listenerpass.Receive(c)
	if err != nil {
		logf("conduit: no listener replica (this process cannot take over if the host dies): %v", err)
		return nil
	}
	return ln
}

// hello runs the version handshake synchronously, before the reader goroutine
// starts, so the one frame whose reply must be read in lockstep is not racing the
// dispatcher.
func (j *Joiner) hello() error {
	_ = j.conn.SetDeadline(time.Now().Add(joinTimeout))
	defer j.conn.SetDeadline(time.Time{})

	payload, err := json.Marshal(HelloRequest{Version: ProtocolVersion, Pid: os.Getpid()})
	if err != nil {
		return err
	}
	if err := j.enc.Encode(Frame{ID: "hello", Op: OpHello, Payload: payload}); err != nil {
		return fmt.Errorf("conduit at %s: sending hello: %w", j.addr, err)
	}
	var reply Reply
	if err := j.dec.Decode(&reply); err != nil {
		return fmt.Errorf("conduit at %s: reading hello reply: %w", j.addr, err)
	}
	if !reply.OK {
		return fmt.Errorf("conduit at %s refused the join: %s", j.addr, reply.Error)
	}
	var resp HelloResponse
	if err := json.Unmarshal(reply.Payload, &resp); err != nil {
		return fmt.Errorf("conduit at %s: malformed hello reply: %w", j.addr, err)
	}
	// Trust the host's own account of its address over the entry we read from disk:
	// the advertisement can be stale in a way the live connection cannot.
	if bind, err := ParseAddr(resp.Bind); err == nil {
		j.addr = bind
	}
	j.settings, j.banner, j.hostPid = resp.Settings, resp.Banner, resp.HostPid
	return nil
}

// read owns the connection's read side for the joiner's whole life, dispatching
// replies to whoever is waiting on them.
//
// A dedicated reader — rather than a read per request — is what makes host death
// OBSERVABLE. With lockstep reads, a joiner sitting idle between registrations
// would not learn its conduit had died until the next time it happened to
// register something, which for a compose session that has finished coming up is
// never.
func (j *Joiner) read() {
	for {
		var reply Reply
		if err := j.dec.Decode(&reply); err != nil {
			j.fail(err)
			return
		}
		j.mu.Lock()
		ch := j.pending[reply.ID]
		delete(j.pending, reply.ID)
		j.mu.Unlock()
		if ch != nil {
			ch <- reply
		}
		// A reply nobody is waiting for is dropped: the waiter timed out or the
		// caller went away, and neither is worth tearing the connection down over.
	}
}

// fail marks the connection dead and wakes every waiter, so a caller blocked in
// Register returns instead of hanging until its context expires.
func (j *Joiner) fail(err error) {
	j.mu.Lock()
	if j.readErr == nil {
		j.readErr = err
	}
	j.closed = true
	pending := j.pending
	j.pending = map[string]chan Reply{}
	j.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	j.doneOnce.Do(func() { close(j.done) })
}

// Register applies one registration in the hosted conduit, returning a withdraw
// that drops it early. Dropping the connection withdraws everything, so a caller
// that exits need not withdraw at all.
func (j *Joiner) Register(ctx context.Context, id, kind string, payload json.RawMessage) (Withdraw, error) {
	return j.register(ctx, id, RegisterRequest{Kind: kind, Payload: payload})
}

// RegisterAt is Register with an explicit precedence, for replaying a claim whose
// sequence a previous host assigned.
func (j *Joiner) RegisterAt(ctx context.Context, id, kind string, payload json.RawMessage, seq uint64) (Withdraw, error) {
	return j.register(ctx, id, RegisterRequest{Kind: kind, Payload: payload, Seq: seq})
}

func (j *Joiner) register(ctx context.Context, id string, r RegisterRequest) (Withdraw, error) {
	kind, payload := r.Kind, r.Payload
	req, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	reply, err := j.call(ctx, Frame{ID: id, Op: OpRegister, Payload: req})
	if err != nil {
		return nil, err
	}
	// Keep the precedence the host assigned. Replaying without it after a takeover
	// would re-order claims by reconnect race rather than by when they were made.
	var resp RegisterResponse
	if len(reply.Payload) > 0 {
		_ = json.Unmarshal(reply.Payload, &resp)
	}
	j.mu.Lock()
	if _, seen := j.regs[id]; !seen {
		j.regOrder = append(j.regOrder, id)
	}
	j.regs[id] = RegisterRequest{Kind: kind, Payload: payload, Seq: resp.Seq}
	j.mu.Unlock()
	return onceWithdraw(func() {
		j.mu.Lock()
		delete(j.regs, id)
		j.mu.Unlock()
		// Best-effort and deliberately untimed against the caller's context: a
		// withdrawal during teardown usually runs with an already-cancelled context,
		// and failing to send it is harmless because closing the connection withdraws
		// everything anyway.
		wctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
		defer cancel()
		_, _ = j.call(wctx, Frame{ID: id, Op: OpWithdraw})
	}), nil
}

// Unregister withdraws the registration made under id, and forgets it so a later
// takeover does not replay something the caller has released.
func (j *Joiner) Unregister(ctx context.Context, id string) error {
	j.mu.Lock()
	delete(j.regs, id)
	j.mu.Unlock()
	_, err := j.call(ctx, Frame{ID: id, Op: OpWithdraw})
	return err
}

// call sends one frame and waits for its reply.
func (j *Joiner) call(ctx context.Context, f Frame) (Reply, error) {
	ch := make(chan Reply, 1)
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return Reply{}, j.goneErr()
	}
	if f.ID == "" {
		j.nextID++
		f.ID = fmt.Sprintf("r%d", j.nextID)
	}
	j.pending[f.ID] = ch
	err := j.enc.Encode(f)
	j.mu.Unlock()
	if err != nil {
		j.mu.Lock()
		delete(j.pending, f.ID)
		j.mu.Unlock()
		return Reply{}, fmt.Errorf("conduit at %s: %w", j.addr, err)
	}

	select {
	case reply, ok := <-ch:
		if !ok {
			return Reply{}, j.goneErr()
		}
		if !reply.OK {
			return Reply{}, fmt.Errorf("conduit at %s: %s", j.addr, reply.Error)
		}
		return reply, nil
	case <-ctx.Done():
		j.mu.Lock()
		delete(j.pending, f.ID)
		j.mu.Unlock()
		return Reply{}, ctx.Err()
	case <-j.done:
		return Reply{}, j.goneErr()
	}
}

// goneErr renders host death with the pid to blame, rather than a bare EOF that
// sends the reader looking for a bug in their own process.
func (j *Joiner) goneErr() error {
	return fmt.Errorf("conduit at %s (host pid %d): %w", j.addr, j.hostPid, ErrHostGone)
}

// Close leaves the conduit. Every registration made on this connection is
// withdrawn by the host as a consequence.
func (j *Joiner) Close() error {
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return nil
	}
	j.closed = true
	j.mu.Unlock()
	err := j.conn.Close()
	j.doneOnce.Do(func() { close(j.done) })
	return err
}
