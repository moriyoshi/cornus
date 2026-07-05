// Package attachsession holds one deploy-attach session — a DeployAttach kept
// running in the background so a workload's client-local 9P mounts stay live for
// its lifetime. It is the single imperative primitive beneath both the client
// reconcile engine's mountController and the Docker API proxy's per-container
// sessions: the engine drives it from a desired set, the proxy drives it directly
// from imperative docker verbs, but the open-hold-until-stopped mechanics
// (background-rooted attach, readiness latch, self-exit signalling) are identical.
package attachsession

import (
	"context"
	"sync"

	"cornus/pkg/api"
	"cornus/pkg/deploywire"
)

// Attacher is the one capability a session needs: open a long-lived deploy-attach
// stream for a spec, serving caller-local mounts over 9P until ctx ends or the
// stream fails. *client.Client satisfies it; so do the dockerproxy and clientagent
// attacher interfaces (structurally — this signature is a subset of theirs), so no
// new import edge is introduced.
type Attacher interface {
	DeployAttach(ctx context.Context, spec api.DeploySpec, events func(deploywire.Event)) error
}

// Session is a running deploy-attach for one workload. DeployAttach runs in a
// goroutine rooted at context.Background() — decoupled from the request that
// started it, so a returning caller does not tear the long-lived mount down —
// until Stop (or a self-exit) ends it.
type Session struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	last      api.DeployStatus
	hasStatus bool
	ready     bool
	err       error
	readyC    chan struct{}
}

// Option configures Open.
type Option func(*options)

type options struct {
	onEvent func(deploywire.Event)
}

// WithEventHook registers f, called for every DeployAttach event before the
// session's own readiness/status handling. The reconcile engine uses it to stream
// deploy logs (e.Log) and surface deploy errors (e.Err); dockerproxy passes none
// (docker logs arrive over a separate endpoint). f must not block.
func WithEventHook(f func(deploywire.Event)) Option {
	return func(o *options) {
		if f != nil {
			o.onEvent = f
		}
	}
}

// Open starts the deploy-attach in the background and returns immediately. When
// DeployAttach returns — whether via Stop or the workload self-exiting — the
// goroutine closes Done() AND cancels the session context, so anything parented on
// Context() (e.g. port exposure) is withdrawn with the session.
func Open(a Attacher, spec api.DeploySpec, opts ...Option) *Session {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{ctx: ctx, cancel: cancel, done: make(chan struct{}), readyC: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer cancel() // a self-exit cancels the context so exposure parented on it is withdrawn
		err := a.DeployAttach(ctx, spec, func(e deploywire.Event) {
			if o.onEvent != nil {
				o.onEvent(e)
			}
			// Ready marks the deployment up; the status is captured when present (the
			// real server and dockerproxy always send it on Ready, so this also serves
			// dockerproxy's status seam). A Ready without a status still readies waiters.
			if e.Ready {
				s.mu.Lock()
				if e.Status != nil {
					s.last = *e.Status
					s.hasStatus = true
				}
				s.markReadyLocked()
				s.mu.Unlock()
			}
		})
		s.mu.Lock()
		s.err = err
		s.markReadyLocked() // unblock waiters even if Ready never arrived
		s.mu.Unlock()
	}()
	return s
}

func (s *Session) markReadyLocked() {
	if !s.ready {
		s.ready = true
		close(s.readyC)
	}
}

// WaitReady blocks until the deployment reports ready (returning the attach error,
// nil on success) or ctx is cancelled. ctx governs only the wait — the caller
// passes a start-timeout (dockerproxy) or an operation context (the reconcile
// engine's Ctrl-C-abortable up); it does NOT bound the session, which lives until
// Stop or self-exit.
func (s *Session) WaitReady(ctx context.Context) error {
	select {
	case <-s.readyC:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done is closed when the DeployAttach goroutine returns (Stop or self-exit).
func (s *Session) Done() <-chan struct{} { return s.done }

// Context is the session's lifetime context, cancelled on Stop or self-exit.
// Callers parent dependent resources (e.g. port exposure) on it so they are
// withdrawn with the session.
func (s *Session) Context() context.Context { return s.ctx }

// Status reports the latest deployment status observed on a Ready event, and
// whether any has been observed yet.
func (s *Session) Status() (api.DeployStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.hasStatus
}

// Stop tears the session down (cancel the attach -> the server removes the
// workload) and blocks until the goroutine has returned.
func (s *Session) Stop() {
	s.cancel()
	<-s.done
}
