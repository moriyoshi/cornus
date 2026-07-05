package conduithost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// Participant is a handle on a conduit, whether this process hosts it or joined
// one someone else hosts. Callers are meant to program against this and not care
// which they got — that indifference is the point of the redesign, and it is what
// lets a foreground session and the background agent be equally good hosts.
type Participant interface {
	// Addr is the conduit's ACTUAL bind address, which after consolidation may be
	// wider than the one requested.
	Addr() Addr
	Hosting() bool
	// Settings are the conduit's canonical settings, opaque to this package. For a
	// joiner they are the incumbent's, not the ones requested.
	Settings() json.RawMessage
	Banner() []string
	// Register applies one registration under a caller-chosen id, returning a
	// withdraw. Registrations are scoped to this participant: closing it withdraws
	// them all.
	Register(ctx context.Context, id, kind string, payload json.RawMessage) (Withdraw, error)
	// Unregister withdraws the registration made under id.
	//
	// It exists alongside the closure Register returns because ownership MOVES: after
	// a takeover the closure refers to a participant that no longer exists, so a
	// caller holding one would silently fail to withdraw and leave the name
	// registered in the new host. An id outlives the participant; a closure does not.
	Unregister(ctx context.Context, id string) error
	// Done is closed when the conduit ends — for a joiner, because the host exited.
	Done() <-chan struct{}
	Close() error
}

var (
	_ Participant = (*Host)(nil)
	_ Participant = (*Joiner)(nil)
)

// Config describes the conduit a caller wants.
type Config struct {
	// Registry is the rendezvous directory. Required unless Addr is ephemeral.
	Registry *Registry
	// Addr is the requested bind address. An ephemeral one (port 0) is private:
	// there is no agreed address for anyone to rendezvous on, so it is neither
	// advertised nor joinable.
	Addr Addr
	// Settings and Banner describe the conduit if this call ends up creating it.
	// They are IGNORED when it joins one, because the incumbent's win.
	Settings json.RawMessage
	Banner   []string
	// BannerFor, when set, replaces Banner once the address is BOUND, and is given
	// that address.
	//
	// It exists because the two can differ: Go binds a wildcard request as a
	// dual-stack "[::]", so a conduit asked for "0.0.0.0:10080" advertises
	// "[::]:10080" while a banner built before the bind still says 0.0.0.0. The
	// banner is the line telling a user where to point their browser, and it is
	// stored and handed to every joiner, so naming an address the listener does not
	// have propagates the error to everyone who joins.
	BannerFor func(bound Addr) []string
	// Registrar applies registrations when this process hosts. Required.
	Registrar Registrar
	// Listen binds the conduit's own listener. It is injectable so the caller keeps
	// control of socket options — socks5.Start's non-loopback refusal, for one —
	// and so tests can bind without a real proxy. nil uses net.Listen("tcp", addr).
	Listen func(addr Addr) (net.Listener, error)
	// Logf receives non-fatal notices: a listener replica that could not be
	// obtained, a registration that could not be restored after a takeover. Every
	// one of them is a degradation the caller should see but none is worth failing
	// a call over. nil logs a slog warning.
	Logf func(format string, args ...any)
	// RecoveryWindow bounds how long a takeover holds requests for names whose
	// claims have not been restored yet. Zero uses DefaultRecoveryWindow.
	//
	// It is a bound, not a wait: the window ends the moment every claim it was
	// waiting for arrives. Its length only decides how long a name that nobody is
	// coming back to claim costs, and — the reason not to make it generous — how
	// long a deployment-QUALIFIED name stalls, since that needs no claim but cannot
	// be told apart from a short name whose owner is still reconnecting.
	RecoveryWindow time.Duration
}

// DefaultRecoveryWindow is how long a takeover waits for the other participants to
// re-register before it starts answering unclaimed names as unknown.
//
// Sized to cover the slowest step a survivor actually goes through — noticing EOF,
// contending for the port lock, probing the advertisements it finds (up to
// probeTimeout each), then re-registering — rather than to a round number.
const DefaultRecoveryWindow = 5 * time.Second

func (c Config) recoveryWindow() time.Duration {
	if c.RecoveryWindow > 0 {
		return c.RecoveryWindow
	}
	return DefaultRecoveryWindow
}

// listenFunc resolves the configured binder, defaulting to net.Listen.
func (c Config) listenFunc() func(Addr) (net.Listener, error) {
	if c.Listen != nil {
		return c.Listen
	}
	return func(a Addr) (net.Listener, error) { return net.Listen("tcp", a.String()) }
}

// ConflictError reports that the port is held by a conduit that does not cover the
// requested address, so the request can neither join it nor bind.
//
// It exists as a type because it is the one refusal a caller may want to act on
// rather than merely print: this is the case where a conduit IS running, just not
// one that serves you.
type ConflictError struct {
	Requested Addr
	Incumbent Addr
	HostPid   int
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("cannot serve a conduit on %s: port %d is already held by a conduit bound to %s (host pid %d), which does not cover it — join it by requesting an address it covers, or pick another port",
		e.Requested, e.Requested.Port, e.Incumbent, e.HostPid)
}

// UnresponsiveError reports that a conduit is advertised for this address, and is
// still holding it, but does not answer on its control socket.
//
// It is its own type — and its own outcome, distinct from both a conflict and a
// stale entry — because the only useful action is on the named process. It cannot
// be joined (the join would block), and its address cannot be taken (the wedged
// process still holds the listening socket), so reporting it as either would send
// the reader somewhere they can do nothing.
type UnresponsiveError struct {
	Requested Addr
	Incumbent Addr
	HostPid   int
}

func (e *UnresponsiveError) Error() string {
	return fmt.Sprintf("the conduit at %s (host pid %d) is not answering on its control socket: it still holds the address, so nothing else can bind it, but it cannot be joined either — stop that process (kill %d) and retry",
		e.Incumbent, e.HostPid, e.HostPid)
}

// Open joins the conduit serving cfg.Addr, or creates it.
//
// The whole decision runs under an exclusive lock on the port, so two processes
// racing for one address cannot both conclude that nothing is there. Without that,
// both bind, one loses with a bare "address already in use", and the user is told
// about a kernel error instead of about the conduit they could have joined.
func Open(ctx context.Context, cfg Config) (Participant, error) {
	if cfg.Registrar == nil {
		return nil, errors.New("conduithost: Registrar is required")
	}
	// An ephemeral conduit is private by construction, so it skips the rendezvous
	// entirely: nothing is advertised, no control socket is bound, and no other
	// process can find it. This is what replaces the old session-local flag — as a
	// consequence of the address rather than as an independent setting that could
	// contradict it.
	if cfg.Addr.Ephemeral() {
		ln, err := cfg.listenFunc()(cfg.Addr)
		if err != nil {
			return nil, err
		}
		return newHost(ctx, cfg, nil, ln)
	}
	if cfg.Registry == nil {
		return nil, errors.New("conduithost: Registry is required for a non-ephemeral address")
	}
	if err := cfg.Registry.ensurePortDir(cfg.Addr.Port); err != nil {
		return nil, err
	}

	var out Participant
	err := withLock(cfg.Registry.lockPath(cfg.Addr.Port), func() error {
		p, err := openLocked(ctx, cfg)
		out = p
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// openLocked runs the create-or-join decision with the port lock held.
func openLocked(ctx context.Context, cfg Config) (Participant, error) {
	dual := DualStackWildcard()
	entries, err := cfg.Registry.Live(cfg.Addr.Port)
	if err != nil {
		return nil, err
	}

	var blocker *Entry
	for i := range entries {
		incumbent, err := entries[i].Addr()
		if err != nil {
			continue
		}
		if Covers(incumbent, cfg.Addr, dual) {
			if !entries[i].Responsive() {
				// Do NOT attempt the join. It would block until joinTimeout with the port
				// lock held, making one wedged host stall every other participant on this
				// port in turn.
				return nil, &UnresponsiveError{Requested: cfg.Addr, Incumbent: incumbent, HostPid: entries[i].Pid}
			}
			j, err := dialJoin(incumbent, entries[i].Socket, cfg)
			if err != nil {
				// The advertisement passed the liveness probe moments ago but the join
				// failed, so the host is dying right now. Reap it and keep looking rather
				// than refuse: the next iteration, or the bind below, is the right answer.
				cfg.Registry.removeEntry(incumbent)
				continue
			}
			return j, nil
		}
		if blocker == nil {
			e := entries[i]
			blocker = &e
		}
	}
	if blocker != nil {
		incumbent, _ := blocker.Addr()
		return nil, &ConflictError{Requested: cfg.Addr, Incumbent: incumbent, HostPid: blocker.Pid}
	}
	ln, err := cfg.listenFunc()(cfg.Addr)
	if err != nil {
		return nil, err
	}
	return newHost(ctx, cfg, cfg.Registry, ln)
}

// newHost takes ownership of an ALREADY-BOUND listener and, unless registry is
// nil, adds the control socket and advertisement that make it joinable.
//
// The listener is a parameter rather than something this function binds, because
// a takeover hands it one that has been bound since before the previous host
// existed. That is the entire point of migration: the address is never rebound,
// so it is never momentarily down.
func newHost(ctx context.Context, cfg Config, registry *Registry, ln net.Listener) (*Host, error) {
	// Judge the address the kernel actually bound, not the one requested. This is
	// not just about ephemeral ports: Go's net.Listen with network "tcp" and a
	// WILDCARD address prefers an AF_INET6 dual-stack socket, so a request for
	// "0.0.0.0:P" comes back bound as "[::]:P". Coverage is a claim about what the
	// kernel accepts, so the kernel's answer is the identity — keying the rendezvous
	// on the requested spelling would advertise an address the listener does not
	// have, and the next joiner would compute coverage against fiction.
	bound := cfg.Addr
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		if a, err := ParseAddr(tcp.String()); err == nil {
			bound = a
		}
	}

	// Take the accepting right before anything is advertised. Every participant
	// holds a replica of this socket, so nothing but this lease stops two of them
	// serving it at once — and two accepters split connections silently, each
	// answering from its own routing table.
	var accept *lease
	if registry != nil {
		var err error
		accept, err = acquireLease(registry.leasePath(bound))
		if err != nil {
			_ = ln.Close()
			if errors.Is(err, ErrLeaseHeld) {
				return nil, fmt.Errorf("conduit at %s: another process is already accepting on it: %w", bound, err)
			}
			return nil, err
		}
	}

	banner := cfg.Banner
	if cfg.BannerFor != nil {
		banner = cfg.BannerFor(bound)
	}

	hctx, cancel := context.WithCancel(ctx)
	h := &Host{
		accept:   accept,
		addr:     bound,
		ln:       ln,
		reg:      cfg.Registrar,
		registry: registry,
		settings: cfg.Settings,
		banner:   banner,
		logf:     cfg.Logf,
		ctx:      hctx,
		cancel:   cancel,
		own:      map[string]Withdraw{},
		conns:    map[net.Conn]struct{}{},
	}
	if registry == nil {
		return h, nil
	}

	// Advertise the BOUND address throughout, not the requested one. They agree for
	// every non-ephemeral request, but keying the socket off one and the entry off
	// the other is the kind of near-tautology that stops being true the first time
	// normalization changes, and then advertises a conduit at a path nobody looks in.
	socket := registry.SocketPath(bound)
	if err := checkSocketPath(socket); err != nil {
		_ = ln.Close()
		cancel()
		accept.release()
		return nil, err
	}
	// Remove a socket inode left behind by a host that died without unlinking. This
	// is safe here and only here: we hold the port lock, and Live has already
	// established that nothing answers on it.
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = ln.Close()
		cancel()
		accept.release()
		return nil, fmt.Errorf("removing stale conduit control socket %s: %w", socket, err)
	}
	ctl, err := net.Listen("unix", socket)
	if err != nil {
		_ = ln.Close()
		cancel()
		accept.release()
		return nil, fmt.Errorf("binding conduit control socket %s: %w", socket, err)
	}
	h.ctl = ctl

	if err := registry.writeEntry(Entry{
		Bind:     bound.String(),
		Pid:      os.Getpid(),
		Socket:   socket,
		Settings: cfg.Settings,
	}); err != nil {
		_ = ctl.Close()
		_ = os.Remove(socket)
		_ = ln.Close()
		cancel()
		accept.release()
		return nil, err
	}
	h.wg.Add(1)
	go h.serve()
	return h, nil
}
