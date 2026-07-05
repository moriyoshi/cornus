// Package supervisor is a minimal in-process supervisor tree for long-lived
// child goroutines. Unlike an errgroup (which cancels every sibling when one
// child fails), each child here is isolated: a panic or early return is caught
// and handled per the child's Policy, never taking down the tree or the other
// children. It is the shared resilience substrate for cornus's own long-lived
// processes that host multiple independent miniservices: the CLI's client-agent
// daemon (a docker frontend crashing must not disturb held compose sessions),
// the server (an internal loop like periodic GC must not take down the whole
// process), and the caretaker sidecar (one mount/credential/egress role failing
// must not tear down its siblings on the same connection).
//
// Children are added and removed at runtime. "Counted" children (e.g. the
// client agent's real work — projects and docker frontends) drive an idle hook
// that fires when the last one goes away; "system" children (e.g. a
// control-socket accept loop) run supervised but do not count toward idleness.
package supervisor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"cornus/pkg/logging"
)

// Service is a supervised child. Serve runs until it returns or ctx is
// cancelled; a returned error (or a panic, which is recovered) is handled per
// the Policy the child was added with.
type Service interface {
	Serve(ctx context.Context) error
}

// ServiceFunc adapts a function to a Service.
type ServiceFunc func(ctx context.Context) error

// Serve implements Service.
func (f ServiceFunc) Serve(ctx context.Context) error { return f(ctx) }

// Policy decides what happens when a child's Serve returns (or panics) on its
// own, i.e. not because the supervisor cancelled it.
type Policy int

const (
	// RemoveOnExit drops the child when it exits (the default). Its slot is freed
	// and, if it was counted, the idle hook may fire.
	RemoveOnExit Policy = iota
	// Restart relaunches the child with capped exponential backoff until it is
	// Removed or the supervisor context is cancelled.
	Restart
)

const (
	minBackoff = 100 * time.Millisecond
	maxBackoff = 30 * time.Second
)

// Token is an opaque handle to one added child, used to Remove it.
type Token struct {
	name    string
	counted bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// Supervisor runs a set of isolated child Services under one context.
type Supervisor struct {
	ctx  context.Context
	logf func(format string, args ...any)

	mu       sync.Mutex
	children map[*Token]struct{}
	counted  int
	idleHook func()
}

// New returns a Supervisor whose children run under ctx: cancelling ctx signals
// every child to return. logf routes child-exit / restart / panic messages
// (nil defaults to slog warnings).
func New(ctx context.Context, logf func(format string, args ...any)) *Supervisor {
	if logf == nil {
		log := logging.FromContext(ctx)
		logf = func(format string, args ...any) { log.WarnContext(ctx, fmt.Sprintf(format, args...)) }
	}
	return &Supervisor{ctx: ctx, logf: logf, children: map[*Token]struct{}{}}
}

// SetIdleHook installs the callback fired whenever the counted-child total
// transitions to zero. The hook runs on its own goroutine (never under the
// supervisor lock), so it may call back into Count/Add/Remove.
func (s *Supervisor) SetIdleHook(fn func()) {
	s.mu.Lock()
	s.idleHook = fn
	s.mu.Unlock()
}

// Count reports the number of live counted children (the agent's real work),
// used to decide idleness.
func (s *Supervisor) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counted
}

// Add supervises svc as a counted child and returns its Token.
func (s *Supervisor) Add(name string, svc Service, policy Policy) *Token {
	return s.add(name, svc, policy, true)
}

// AddSystem supervises svc as an uncounted infrastructure child (e.g. the
// control-socket accept loop) — same isolation/restart, but it does not keep the
// agent from going idle.
func (s *Supervisor) AddSystem(name string, svc Service, policy Policy) *Token {
	return s.add(name, svc, policy, false)
}

func (s *Supervisor) add(name string, svc Service, policy Policy, counted bool) *Token {
	cctx, cancel := context.WithCancel(s.ctx)
	t := &Token{name: name, counted: counted, cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.children[t] = struct{}{}
	if counted {
		s.counted++
	}
	s.mu.Unlock()
	go s.run(cctx, t, svc, policy)
	return t
}

// Remove cancels the child and waits for it to drain. Idempotent and safe to
// call on a child that already self-exited (RemoveOnExit).
func (s *Supervisor) Remove(t *Token) {
	if t == nil {
		return
	}
	t.cancel()
	<-t.done
	s.forget(t)
}

// Wait blocks until every currently-registered child has drained. Call it after
// cancelling the supervisor context to make shutdown synchronous.
func (s *Supervisor) Wait() {
	s.mu.Lock()
	toks := make([]*Token, 0, len(s.children))
	for t := range s.children {
		toks = append(toks, t)
	}
	s.mu.Unlock()
	for _, t := range toks {
		<-t.done
	}
}

// run is the per-child goroutine: serve (with panic isolation), then act on the
// Policy. A supervisor-context cancellation always ends the child with no
// restart.
func (s *Supervisor) run(ctx context.Context, t *Token, svc Service, policy Policy) {
	defer close(t.done)
	backoff := minBackoff
	for {
		err := safeServe(ctx, svc)
		if ctx.Err() != nil {
			return // cancelled (Remove or supervisor shutdown): never restart
		}
		if policy == RemoveOnExit {
			if err != nil {
				s.logf("supervisor: child %q exited: %v", t.name, err)
			}
			s.forget(t)
			return
		}
		s.logf("supervisor: child %q exited (%v); restarting in %s", t.name, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// forget removes t from the registry exactly once and fires the idle hook when
// the last counted child goes away.
func (s *Supervisor) forget(t *Token) {
	s.mu.Lock()
	if _, ok := s.children[t]; !ok {
		s.mu.Unlock()
		return
	}
	delete(s.children, t)
	idle := false
	if t.counted {
		s.counted--
		idle = s.counted == 0
	}
	hook := s.idleHook
	s.mu.Unlock()
	if idle && hook != nil {
		go hook()
	}
}

func safeServe(ctx context.Context, svc Service) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return svc.Serve(ctx)
}
