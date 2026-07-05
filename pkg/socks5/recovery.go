package socks5

import (
	"context"
	"time"
)

// SetRecoveryUntil opens a recovery window ending at t, during which a target that
// looks like a conduit name but has no claim yet resolves to KindPending instead of
// being answered wrongly.
//
// A host calls this the moment it takes over. Everything the OTHER participants had
// claimed died with the previous host — registrations are scoped to their control
// connection — so for a moment the routing table holds only the new host's own
// claims, and it cannot tell a name it has never heard of from one whose owner is
// three milliseconds from re-registering it.
//
// A zero t closes the window immediately.
func (r *Router) SetRecoveryUntil(t time.Time) {
	r.aliasMu.Lock()
	r.recoverUntil = t
	waiters := r.claimWaiters
	if t.IsZero() || !t.After(time.Now()) {
		r.claimWaiters = map[string][]chan struct{}{}
	} else {
		waiters = nil
	}
	r.aliasMu.Unlock()
	// Closing the window releases everyone still waiting on it, so nothing is left
	// parked on a deadline that has been cancelled.
	for _, chans := range waiters {
		for _, ch := range chans {
			close(ch)
		}
	}
}

// recoveringLocked reports whether the recovery window is open. Caller holds aliasMu.
func (r *Router) recoveringLocked() bool {
	return !r.recoverUntil.IsZero() && time.Now().Before(r.recoverUntil)
}

// AwaitClaim blocks until label is claimed, the recovery window closes, or ctx ends,
// and reports whether the label became claimed.
//
// A false answer is not a failure — it means the wait was resolved by time rather
// than by a claim, and the caller should answer with whatever the router says now.
// The window is bounded precisely so an unknown name cannot hang forever: a target
// that nothing will ever claim costs one recovery window of latency, once.
func (r *Router) AwaitClaim(ctx context.Context, label string) bool {
	r.aliasMu.Lock()
	if !r.recoveringLocked() {
		r.aliasMu.Unlock()
		return false
	}
	if len(r.aliases[label]) > 0 {
		r.aliasMu.Unlock()
		return true // claimed between Resolve and here
	}
	ch := make(chan struct{})
	if r.claimWaiters == nil {
		r.claimWaiters = map[string][]chan struct{}{}
	}
	r.claimWaiters[label] = append(r.claimWaiters[label], ch)
	deadline := r.recoverUntil
	r.aliasMu.Unlock()

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		r.dropWaiter(label, ch)
		return false
	case <-ctx.Done():
		r.dropWaiter(label, ch)
		return false
	}
}

func (r *Router) dropWaiter(label string, ch chan struct{}) {
	r.aliasMu.Lock()
	defer r.aliasMu.Unlock()
	chans := r.claimWaiters[label]
	for i, c := range chans {
		if c == ch {
			r.claimWaiters[label] = append(chans[:i:i], chans[i+1:]...)
			break
		}
	}
	if len(r.claimWaiters[label]) == 0 {
		delete(r.claimWaiters, label)
	}
}

// wakeClaimWaitersLocked releases everyone waiting on label. Caller holds aliasMu;
// the channels are closed after it is dropped, so a woken waiter does not have to
// contend for the lock it is about to take.
func (r *Router) wakeClaimWaitersLocked(label string) []chan struct{} {
	chans := r.claimWaiters[label]
	if len(chans) == 0 {
		return nil
	}
	delete(r.claimWaiters, label)
	return chans
}
