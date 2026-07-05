package dockerproxy

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/attachsession"
	"cornus/pkg/deploywire"
)

// blockingAttacher stays "attached" until its context is cancelled, so
// attachsession.Open yields a session whose Done()/Stop() behave like a live
// deploy-attach: Stop cancels the context, DeployAttach returns, and Done()
// closes.
type blockingAttacher struct{}

func (blockingAttacher) DeployAttach(ctx context.Context, _ api.DeploySpec, _ func(deploywire.Event)) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestSetExitedWaitsForCleanup guards the port-teardown race. Session.Stop wakes
// the self-exit watcher (Proxy.start) on the very channel it returns on, so a
// /stop (or /rm) handler and the watcher both call setExited for the same
// session. Only the winner runs cleanup — portfwd.Group.Close, which closes the
// published listener. Before the fix the loser returned immediately, so the
// handler could write its 204 (and a client could reconnect to the port) while
// the listener was still being torn down in the watcher goroutine. setExited
// must not return until this session's cleanup has completed, for the winner and
// the loser alike.
func TestSetExitedWaitsForCleanup(t *testing.T) {
	for i := 0; i < 200; i++ {
		sess := attachsession.Open(blockingAttacher{}, api.DeploySpec{})

		var cleaned atomic.Bool
		cleanup := func() {
			// A non-trivial teardown window: portfwd.Group.Close drains in-flight
			// tunnels and waits on its accept goroutines, so cleanup is never
			// instantaneous. The sleep makes a premature return observable.
			time.Sleep(2 * time.Millisecond)
			cleaned.Store(true)
		}

		rec := &containerRecord{startedC: make(chan struct{})}
		rec.setRunning(sess, cleanup)

		// Every setExited(sess) caller must observe cleanup as complete the
		// instant its call returns — that is the ordering a /stop response relies
		// on to promise the port is released.
		check := func() {
			rec.setExited(sess)
			if !cleaned.Load() {
				t.Fatalf("iter %d: setExited returned before cleanup completed", i)
			}
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-sess.Done(); check() }() // self-exit watcher
		go func() { defer wg.Done(); sess.Stop(); check() }()   // /stop handler
		wg.Wait()
	}
}
