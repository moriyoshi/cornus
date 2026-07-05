package server

import (
	"context"
	"strings"
	"sync"
	"time"

	"cornus/pkg/hub"
	"cornus/pkg/supervisor"
)

// catalogPollInterval is the fallback poll period of the catalog notifier: local
// registrations and disconnects kick an immediate recheck, so the poll only has
// to catch changes made on OTHER replicas of a distributed store (RedisStore /
// KubeStore expose no portable change signal, so a hash-compare poll is the v1).
const catalogPollInterval = 3 * time.Second

// catalogWatch returns the server's catalog notifier, created lazily on first
// use so it binds the FINAL hub store (tests may swap s.hub right after New,
// before any request arrives). Until something subscribes it costs nothing.
func (s *Server) catalogWatch() *catalogNotifier {
	// The notifier reads the catalog through mountFilteredStore so watch pushes
	// never carry reserved mount-session routing records (see mount_relay.go).
	s.catalogOnce.Do(func() { s.catalog = newCatalogNotifier(mountFilteredStore{s.hub}, catalogPollInterval) })
	return s.catalog
}

// catalogNotifier fans hub-catalog changes out to the control streams of spokes
// that registered with the Watch capability. Change detection is a catalog-hash
// compare, rechecked on an explicit kick (local register/disconnect) and on a
// slow poll (cross-replica changes in a distributed store). The poll goroutine
// runs only while at least one subscriber exists, so a hub with no watching
// spokes never touches the store.
type catalogNotifier struct {
	store hub.Store
	poll  time.Duration
	kick  chan struct{} // buffered(1): coalesced immediate-recheck signal

	mu   sync.Mutex
	subs map[chan []string]struct{}
	last string // hash (joined names) of the last delivered catalog
	// sup hosts the poll loop (runPoll) as a supervised child while at least one
	// subscriber exists; cancel stops it. Both nil while no subscriber exists, so
	// the loop — and the store traffic it does — cost nothing when nobody watches.
	// The policy is supervisor.Restart: the poll loop is process-lifetime within a
	// subscription window and holds no per-iteration stream state, so a panic
	// should be recovered and the loop relaunched (capped backoff) rather than
	// silently lost, which would strand every watching spoke with a frozen catalog.
	sup    *supervisor.Supervisor
	cancel context.CancelFunc
}

func newCatalogNotifier(store hub.Store, poll time.Duration) *catalogNotifier {
	return &catalogNotifier{store: store, poll: poll, kick: make(chan struct{}, 1), subs: map[chan []string]struct{}{}}
}

// changed signals that the registration set may have changed (a local register
// or disconnect); the poll loop rechecks immediately. Non-blocking and safe to
// call with no subscribers (a stale kick at most causes one redundant compare).
func (n *catalogNotifier) changed() {
	select {
	case n.kick <- struct{}{}:
	default:
	}
}

// subscribe registers a watcher and immediately delivers the current catalog as
// its first update (the initial snapshot — a spoke needs no separate catalog
// fetch). The returned cancel removes the watcher and closes its channel; the
// poll loop starts with the first subscriber and stops with the last.
func (n *catalogNotifier) subscribe() (<-chan []string, func()) {
	ch := make(chan []string, 1)
	n.mu.Lock()
	n.subs[ch] = struct{}{}
	if n.sup == nil {
		ctx, cancel := context.WithCancel(context.Background())
		n.cancel = cancel
		n.sup = supervisor.New(ctx, nil)
		n.sup.AddSystem("hub-catalog-poll", supervisor.ServiceFunc(n.runPoll), supervisor.Restart)
	}
	n.mu.Unlock()

	names := n.store.Catalog()
	n.mu.Lock()
	if _, ok := n.subs[ch]; ok {
		n.last = strings.Join(names, "\n")
		offerCatalog(ch, names)
	}
	n.mu.Unlock()

	cancel := func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if _, ok := n.subs[ch]; !ok {
			return
		}
		delete(n.subs, ch)
		close(ch)
		if len(n.subs) == 0 {
			// Stop the poll loop. cancel only signals (it never blocks under n.mu);
			// runPoll observes ctx.Done() and exits, and its RemoveOnExit-on-cancel
			// path in the supervisor drops the child — matching the old fire-and-
			// forget close(stop), which also did not wait.
			n.cancel()
			n.sup = nil
			n.cancel = nil
		}
	}
	return ch, cancel
}

// runPoll is the recheck loop, run as a supervised child while subscribers
// exist: on a kick or a poll tick it reads the catalog and, when its hash moved,
// delivers the new snapshot to every subscriber. It returns (no restart) when
// ctx is cancelled — the last subscriber left; a panic is instead recovered by
// the supervisor, which relaunches it. It always returns nil; the error result
// exists only to satisfy supervisor.ServiceFunc.
func (n *catalogNotifier) runPoll(ctx context.Context) error {
	t := time.NewTicker(n.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-n.kick:
		case <-t.C:
		}
		names := n.store.Catalog()
		n.mu.Lock()
		if h := strings.Join(names, "\n"); h != n.last {
			n.last = h
			for ch := range n.subs {
				offerCatalog(ch, names)
			}
		}
		n.mu.Unlock()
	}
}

// offerCatalog delivers a snapshot to one subscriber channel, replacing any
// undelivered older snapshot (only the latest catalog matters — a slow control
// stream never backs the notifier up). Caller holds n.mu, so the channel cannot
// be closed concurrently.
func offerCatalog(ch chan []string, names []string) {
	for {
		select {
		case ch <- names:
			return
		default:
			select {
			case <-ch:
			default:
			}
		}
	}
}
