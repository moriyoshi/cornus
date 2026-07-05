package conduithost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"cornus/pkg/listenerpass"
)

// defaultLogf routes this package's non-fatal notices. Everything it reports is a
// degradation rather than a failure — no replica, a lost handoff — so it warns and
// carries on rather than failing a call the caller could still complete.
func defaultLogf(format string, args ...any) {
	slog.Warn(fmt.Sprintf(format, args...), "component", "conduithost")
}

func (j *Joiner) logf(format string, args ...any) {
	if j != nil && j.cfg.Logf != nil {
		j.cfg.Logf(format, args...)
		return
	}
	defaultLogf(format, args...)
}

// CanTakeOver reports whether this participant holds a reference to the conduit's
// listening socket, and could therefore become its host if the current one dies.
//
// A false answer is not an error. It means migration is unavailable on this
// platform or the handoff did not happen, and the conduit simply dies with its
// host — a weaker guarantee, not a broken one.
func (j *Joiner) CanTakeOver() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.replica != nil && !j.tookOver
}

// Takeover runs the election after the host has gone, and returns this process's
// new handle on the conduit.
//
// It returns either a *Host — this process won and is now serving on the socket
// reference it took at join time — or a *Joiner attached to whichever survivor won
// instead. Either way the registrations this participant held are replayed onto
// the result before it is returned, because registrations are scoped to a control
// connection and every one of them died with the host. Only the survivor still
// knows what it had registered, so only the survivor can restore it.
//
// The whole decision runs under the port lock, so several survivors racing produce
// exactly one host. Calling it while the host is still alive is a caller error and
// is refused: it would unlink a live host's control socket.
func (j *Joiner) Takeover(ctx context.Context) (Participant, error) {
	select {
	case <-j.done:
	default:
		return nil, errors.New("conduithost: Takeover called while the host is still live")
	}

	j.mu.Lock()
	if j.tookOver {
		j.mu.Unlock()
		return nil, errors.New("conduithost: Takeover already ran on this participant")
	}
	j.tookOver = true
	replica := j.replica
	j.replica = nil
	regs := make([]RegisterRequest, 0, len(j.regOrder))
	ids := make([]string, 0, len(j.regOrder))
	for _, id := range j.regOrder {
		if r, ok := j.regs[id]; ok {
			ids = append(ids, id)
			regs = append(regs, r)
		}
	}
	cfg := j.cfg
	j.mu.Unlock()

	if cfg.Registry == nil || cfg.Registrar == nil {
		if replica != nil {
			_ = replica.Close()
		}
		return nil, errors.New("conduithost: Takeover needs the Registry and Registrar the conduit was opened with")
	}

	var out Participant
	err := withLock(cfg.Registry.lockPath(j.addr.Port), func() error {
		p, err := j.electLocked(ctx, cfg, replica)
		out = p
		return err
	})
	if err != nil {
		if replica != nil {
			_ = replica.Close()
		}
		return nil, err
	}

	// Open the recovery window BEFORE replaying anything.
	//
	// Registrations are scoped to their control connection, so the previous host's
	// death took the whole routing table with it — not just its own claims. This
	// process can restore only what it registered itself; everything the other
	// participants held is missing until each of them notices, reconnects and
	// re-registers. During that interval a name whose claim has not come back yet
	// would be answered wrongly: the bare form egresses to public DNS, sending a
	// request meant for a workload out of the machine, and the suffixed form resolves
	// to a service that does not exist. The window makes those requests WAIT for the
	// claim instead.
	//
	// Only when this process is the one serving. A survivor that lost the election is
	// registering into somebody else's router, and that host opened its own window.
	if out.Hosting() {
		if rec, ok := cfg.Registrar.(Recoverer); ok {
			rec.BeginRecovery(time.Now().Add(cfg.recoveryWindow()))
		} else {
			// Say so rather than degrade quietly: without it the takeover still works,
			// but for a moment it answers unclaimed names as though they were unknown.
			j.logf("conduit %s: the registrar does not support a recovery window, so names whose owners have not yet re-registered will be answered as unknown rather than waited for", j.addr)
		}
	}

	// Replay in the original order. A failure here is reported but does not undo the
	// takeover: the conduit is up and serving, and losing one name is a smaller
	// wound than tearing the address back down.
	for i, id := range ids {
		// Replay at the ORIGINAL precedence. Registering afresh would renumber these
		// claims in the order this process happened to reconnect, which after an
		// election is arbitrary — so a contested short name would resolve differently
		// after every takeover, with nothing able to detect it.
		if _, err := registerAt(ctx, out, id, regs[i]); err != nil {
			j.logf("conduit %s: could not restore registration %q after taking over: %v", j.addr, id, err)
		}
	}
	return out, nil
}

// registerAt replays one claim at its original precedence, through whichever kind
// of participant the takeover produced.
func registerAt(ctx context.Context, p Participant, id string, r RegisterRequest) (Withdraw, error) {
	switch t := p.(type) {
	case *Host:
		return t.RegisterAt(ctx, id, r.Kind, r.Payload, r.Seq)
	case *Joiner:
		return t.RegisterAt(ctx, id, r.Kind, r.Payload, r.Seq)
	default:
		return p.Register(ctx, id, r.Kind, r.Payload)
	}
}

// electLocked decides, with the port lock held, whether this process becomes the
// host or joins one that already took over.
func (j *Joiner) electLocked(ctx context.Context, cfg Config, replica net.Listener) (Participant, error) {
	// Re-check first: between noticing the death and taking the lock, another
	// survivor may already have won. Without this every survivor would try to bind,
	// and all but one would fail — the thundering herd the lock exists to prevent.
	dual := DualStackWildcard()
	entries, err := cfg.Registry.Live(j.addr.Port)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		incumbent, err := entries[i].Addr()
		if err != nil || !Covers(incumbent, j.addr, dual) {
			continue
		}
		if !entries[i].Responsive() {
			// Do NOT step past it. That process still holds a reference to the same
			// listening socket, so if it recovers and resumes accepting there are two
			// accepters on one address and the kernel splits connections between them
			// with no error on either side — each answering from its own routing table.
			//
			// Nothing can stop a process resuming, so a process that MIGHT resume must
			// not be fenced. It also still holds the accept lease, which is the
			// enforcement: taking over would fail there anyway. Refusing here just makes
			// the reason legible instead of surfacing as a lease error.
			return nil, &UnresponsiveError{Requested: j.addr, Incumbent: incumbent, HostPid: entries[i].Pid}
		}
		nj, err := dialJoin(incumbent, entries[i].Socket, cfg)
		if err != nil {
			cfg.Registry.removeEntry(incumbent)
			continue
		}
		// Someone else is hosting now, so this process's own reference is surplus.
		// Closing it is safe and correct: the new host holds its own, and the socket
		// lives while any reference does. The new joiner takes a fresh replica of its
		// own during dialJoin, so the migration chain does not end here.
		if replica != nil {
			_ = replica.Close()
		}
		return nj, nil
	}

	// Serve on the reference taken at join time. The address was never unbound —
	// this listener has been holding it the whole time — so a client dialing across
	// the handover sees a queued connection, never a refusal.
	//
	// But only if the reference is SOUND. A replica can be closed, or no longer be a
	// listening socket, and adopting a broken one is the worst available outcome:
	// the new host advertises the conduit and serves nothing, and because a
	// bound-but-unaccepted socket does not refuse connections, clients hang in the
	// backlog instead of failing over to something that works. Silence is exactly
	// what must not happen here, so the descriptor is checked before it is adopted.
	if replica != nil {
		if err := listenerpass.Verify(replica); err != nil {
			j.logf("conduit %s: the inherited listener is not usable (%v); rebinding the address instead", j.addr, err)
			_ = replica.Close()
			replica = nil
		}
	}
	if replica != nil {
		// The replica must still be the socket for the address being advertised.
		// Advertising one address while serving another would publish a conduit that
		// nobody can reach, and every later coverage decision would be computed
		// against the advertisement rather than the truth.
		if err := replicaServesAddr(replica, j.addr); err != nil {
			j.logf("conduit %s: %v; rebinding the address instead", j.addr, err)
			_ = replica.Close()
			replica = nil
		}
	}
	if replica != nil {
		return newHost(ctx, cfg, cfg.Registry, replica)
	}

	// Fall back to binding the address afresh. This is a strictly worse outcome —
	// the address WAS down for the interval between the old host's exit and this
	// bind, so a client dialing in that window was refused — but a conduit that
	// works after a visible gap beats one that is advertised and dead.
	ln, err := cfg.listenFunc()(j.addr)
	if err != nil {
		return nil, fmt.Errorf("conduit at %s: the host exited, this process has no usable reference to the listening socket, and rebinding failed: %w", j.addr, err)
	}
	return newHost(ctx, cfg, cfg.Registry, ln)
}

// replicaServesAddr checks that ln is bound to the address the takeover is about
// to advertise.
//
// Coverage rather than equality, and in this direction specifically: the
// advertised address is the one this participant JOINED, which after consolidation
// can be narrower than what the socket actually serves (joining 127.0.0.1 into a
// 0.0.0.0 conduit). A replica bound wider than the advertisement is correct and
// common; one bound narrower, or elsewhere entirely, is not.
func replicaServesAddr(ln net.Listener, want Addr) error {
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("the inherited listener has a %T address, not TCP", ln.Addr())
	}
	got, err := ParseAddr(tcp.String())
	if err != nil {
		return fmt.Errorf("the inherited listener's address %q is unparseable: %w", tcp.String(), err)
	}
	if !Covers(got, want, DualStackWildcard()) {
		return fmt.Errorf("the inherited listener serves %s, which does not cover the advertised %s", got, want)
	}
	return nil
}
