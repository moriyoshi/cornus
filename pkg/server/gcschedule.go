package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"cornus/pkg/logging"
	"cornus/pkg/supervisor"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	coordclientv1 "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// gcIntervalFromEnv reads CORNUS_GC_INTERVAL, the period of the background
// storage-GC scheduler. Unset/empty disables the feature entirely (zero
// interval → no goroutine, no ticker; the zero-cost-when-off pattern shared by
// the other optional subsystems). A set-but-malformed or non-positive value is
// a hard startup error (fail closed), exactly like the policy env vars — a
// typo'd schedule must not silently disable reclamation.
func gcIntervalFromEnv() (time.Duration, error) {
	raw := os.Getenv("CORNUS_GC_INTERVAL")
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid CORNUS_GC_INTERVAL %q: %w", raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid CORNUS_GC_INTERVAL %q: must be a positive duration", raw)
	}
	return d, nil
}

// defaultGCLeaseName is the coordination.k8s.io Lease the periodic-GC leader
// gate acquires when CORNUS_GC_LEASE=kube gives no explicit name.
const defaultGCLeaseName = "cornus-gc"

// gcLeaseMinDuration / gcLeaseMaxDuration bound the Lease validity window
// derived from the GC interval (see newGCLeaseGate).
const (
	gcLeaseMinDuration = 30 * time.Second
	gcLeaseMaxDuration = time.Hour
)

// parseGCLease parses the CORNUS_GC_LEASE value. Accepted forms:
//
//	kube                     → default Lease name in the default namespace
//	kube:<name>              → explicit Lease name
//	kube:<namespace>/<name>  → explicit namespace and name
//
// The returned namespace is empty unless the value overrides it; the caller
// resolves the default (namespaceFromEnv, the kubernetes deploy backend's
// convention). Anything else — an unknown scheme, empty segments, extra
// slashes — is an error, so a typo is a hard startup failure (fail closed)
// rather than a silently uncoordinated GC.
func parseGCLease(raw string) (namespace, name string, err error) {
	if raw == "kube" {
		return "", defaultGCLeaseName, nil
	}
	v, ok := strings.CutPrefix(raw, "kube:")
	if !ok {
		return "", "", fmt.Errorf("invalid CORNUS_GC_LEASE %q: want \"kube\", \"kube:<name>\", or \"kube:<namespace>/<name>\"", raw)
	}
	if ns, n, found := strings.Cut(v, "/"); found {
		if ns == "" || n == "" || strings.Contains(n, "/") {
			return "", "", fmt.Errorf("invalid CORNUS_GC_LEASE %q: malformed <namespace>/<name>", raw)
		}
		return ns, n, nil
	}
	if v == "" {
		return "", "", fmt.Errorf("invalid CORNUS_GC_LEASE %q: empty lease name", raw)
	}
	return "", v, nil
}

// gcLeaseGateFromEnv builds the periodic-GC leader gate from CORNUS_GC_LEASE.
// Unset/empty returns (nil, nil): no gate, no Kubernetes client, zero cost —
// the scheduler runs unconditionally exactly as before. When set, the env must
// parse (see parseGCLease), CORNUS_GC_INTERVAL must be configured (a lease
// gating a scheduler that never ticks is a misconfiguration), and a Kubernetes
// client config must load (in-cluster, falling back to the local kubeconfig —
// the same resolution the kubernetes deploy backend and kubehub use); each
// failure is a hard startup error.
func gcLeaseGateFromEnv(interval time.Duration) (func(context.Context) (bool, error), error) {
	raw := os.Getenv("CORNUS_GC_LEASE")
	if raw == "" {
		return nil, nil
	}
	namespace, name, err := parseGCLease(raw)
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		return nil, fmt.Errorf("CORNUS_GC_LEASE %q is set but CORNUS_GC_INTERVAL is not; the lease only coordinates the periodic GC scheduler", raw)
	}
	if namespace == "" {
		namespace = namespaceFromEnv()
	}
	cfg, err := gcLeaseKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("CORNUS_GC_LEASE %q: load kubernetes config: %w", raw, err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("CORNUS_GC_LEASE %q: build kubernetes client: %w", raw, err)
	}
	identity := os.Getenv("CORNUS_REPLICA_ID")
	if identity == "" {
		identity = defaultReplicaID()
	}
	gate := newGCLeaseGate(cs, namespace, name, identity, interval)
	ctx := logging.WithAttrs(context.Background(), slog.String("component", "gc"))
	logging.FromContext(ctx).InfoContext(ctx, "periodic GC lease enabled", "lease", namespace+"/"+name, "identity", identity, "leaseDuration", gate.duration)
	return gate.tryAcquire, nil
}

// gcLeaseKubeConfig loads the client config for the GC lease gate: in-cluster
// first, then the local kubeconfig — the same two-step resolution the
// kubernetes deploy backend and kubehub use. Deliberately a small local copy
// (both of theirs are unexported) rather than a new shared export.
func gcLeaseKubeConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

// gcLeaseGate implements the periodic-GC leader election as a single
// coordination.k8s.io/v1 Lease acquired (or renewed) once per tick.
//
// Why not k8s.io/client-go/tools/leaderelection: that machinery maintains
// leadership continuously — a background renewal loop, retry period, release
// callbacks — which buys nothing here. GC runs at most once per interval, so
// leadership only matters at the instant of a tick; a per-tick get-or-create
// with optimistic-concurrency (the Update carries the Get's resourceVersion,
// so a concurrent claimant gets a 409 and loses) is the whole protocol, is
// trivially testable against the fake clientset, and holds zero resources
// between ticks.
type gcLeaseGate struct {
	leases   coordclientv1.LeaseInterface
	name     string
	identity string
	// duration is the Lease validity window: 2x the GC interval (a healthy
	// holder renews every tick with a full interval of slack), clamped to
	// [gcLeaseMinDuration, gcLeaseMaxDuration]. The floor keeps clock skew from
	// flapping ownership at silly-short intervals; the cap bounds how long GC
	// stays paused after the holder dies when the interval is very long (the
	// sweep itself is far shorter than an hour, so an expired-but-alive holder
	// being replaced is harmless — the point is only that two replicas never
	// sweep concurrently).
	duration time.Duration
	// now is injectable for expiry tests; time.Now in production.
	now func() time.Time
}

func newGCLeaseGate(cs kubernetes.Interface, namespace, name, identity string, interval time.Duration) *gcLeaseGate {
	d := 2 * interval
	if d < gcLeaseMinDuration {
		d = gcLeaseMinDuration
	}
	if d > gcLeaseMaxDuration {
		d = gcLeaseMaxDuration
	}
	return &gcLeaseGate{
		leases:   cs.CoordinationV1().Leases(namespace),
		name:     name,
		identity: identity,
		duration: d,
		now:      time.Now,
	}
}

// tryAcquire attempts to take or renew the Lease for this replica and reports
// whether it holds it now. false with a nil error means another live replica
// holds it (this tick's GC should be skipped); an error means the claim could
// not be decided (the caller skips too — fail closed). Losing a create or
// update race to a concurrent claimant is not an error, just "not the holder".
func (g *gcLeaseGate) tryAcquire(ctx context.Context) (bool, error) {
	now := metav1.NewMicroTime(g.now())
	dur := int32(g.duration / time.Second)
	existing, err := g.leases.Get(ctx, g.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		holder := g.identity
		lease := &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: g.name},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: &dur,
				RenewTime:            &now,
			},
		}
		if _, err := g.leases.Create(ctx, lease, metav1.CreateOptions{}); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return false, nil // lost the creation race
			}
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !g.claimable(existing) {
		return false, nil
	}
	holder := g.identity
	existing.Spec.HolderIdentity = &holder
	existing.Spec.LeaseDurationSeconds = &dur
	existing.Spec.RenewTime = &now
	if _, err := g.leases.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) {
			return false, nil // lost the renewal/takeover race
		}
		return false, err
	}
	return true, nil
}

// claimable reports whether this replica may write the Lease: it already holds
// it, nobody does, or the current holder's window (its own LeaseDurationSeconds,
// falling back to ours) has lapsed — a crashed holder is taken over on the
// first tick after expiry. A missing RenewTime counts as expired.
func (g *gcLeaseGate) claimable(l *coordinationv1.Lease) bool {
	holder := ""
	if l.Spec.HolderIdentity != nil {
		holder = *l.Spec.HolderIdentity
	}
	if holder == "" || holder == g.identity {
		return true
	}
	if l.Spec.RenewTime == nil {
		return true
	}
	dur := g.duration
	if l.Spec.LeaseDurationSeconds != nil && *l.Spec.LeaseDurationSeconds > 0 {
		dur = time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second
	}
	return g.now().After(l.Spec.RenewTime.Add(dur))
}

// startPeriodicGC starts the background storage-GC loop when CORNUS_GC_INTERVAL
// is configured (s.gcInterval > 0), as a supervised child of s.sup: a panic or
// transient failure restarts it in place (capped exponential backoff) instead
// of taking down the whole server. When the interval is zero it does nothing —
// no ticker, no supervised child, zero cost. Called by Run once the listener
// is bound; the first run happens after one full interval (not at startup,
// which is busy). closeResources cancels s.sup and waits for it to stop.
func (s *Server) startPeriodicGC() {
	if s.gcInterval <= 0 {
		return
	}
	ctx := logging.WithAttrs(context.Background(), slog.String("component", "gc"))
	logging.FromContext(ctx).InfoContext(ctx, "periodic GC enabled", "interval", s.gcInterval)
	// Tests may inject a deterministic tick source (gcTicks); production always
	// uses the real interval ticker. The loop re-reads this channel on each
	// supervisor restart, so it must be captured once here, not per-restart.
	ticks := s.gcTicks
	if ticks == nil {
		s.gcTicker = time.NewTicker(s.gcInterval)
		ticks = s.gcTicker.C
	}
	s.sup.AddSystem("gc", supervisor.ServiceFunc(func(ctx context.Context) error {
		return s.periodicGCLoop(ctx, ticks)
	}), supervisor.Restart)
}

// periodicGCLoop runs the shared GC core (runGC, the same logic POST /.cornus/v1/gc
// executes) on every tick until ctx is cancelled. When a leader gate is
// configured (CORNUS_GC_LEASE), each tick first tries to acquire/renew the
// Lease and only the holder proceeds; non-holders skip with a debug log and a
// gate error skips too (fail closed — better a missed sweep than a concurrent
// one). Runs never overlap: the run is synchronous in the loop and the
// gcRunning guard skips a tick (with a log line) if a previous run is somehow
// still in flight. Errors are logged, never fatal — a failed sweep just waits
// for the next tick. The ticks channel is injected so tests can drive the loop
// without wall-clock waits. It always returns nil; the return value exists to
// satisfy supervisor.ServiceFunc.
func (s *Server) periodicGCLoop(ctx context.Context, ticks <-chan time.Time) error {
	run := s.gcRun
	if run == nil {
		run = s.runGC
	}
	gate := s.gcGate
	gctx := logging.WithAttrs(context.Background(), slog.String("component", "gc"))
	log := logging.FromContext(gctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
			if gate != nil {
				held, err := gate(context.Background())
				if err != nil {
					log.WarnContext(gctx, "lease check failed; skipping tick", "error", err)
					continue
				}
				if !held {
					log.DebugContext(gctx, "lease held by another replica; skipping tick")
					continue
				}
			}
			if !s.gcRunning.CompareAndSwap(false, true) {
				log.InfoContext(gctx, "previous run still in progress; skipping tick")
				continue
			}
			s.runGCTick(gctx, gate, run, log)
		}
	}
}

// runGCTick executes one GC sweep and unconditionally clears gcRunning on the
// way out via defer — including on a panic. periodicGCLoop now runs as a
// supervised child (supervisor.Restart): a panic here is recovered and the
// loop is relaunched as a FRESH invocation with no memory of having been
// mid-tick, so without this defer a panic mid-sweep would leave gcRunning
// stuck at true forever and every future tick would skip as "already running".
func (s *Server) runGCTick(gctx context.Context, gate func(context.Context) (bool, error), run func(context.Context) (gcResponse, error), log *slog.Logger) {
	defer s.gcRunning.Store(false)
	log.InfoContext(gctx, "run started")
	start := time.Now()
	// Keep the lease fresh for the whole sweep: a run longer than the lease
	// window (>= 2x interval) would otherwise let its lease lapse mid-sweep and
	// a second replica take it over and sweep concurrently, violating the
	// single-sweeper invariant. Renew in the background until the run returns.
	var renewStop chan struct{}
	if gate != nil {
		renewStop = make(chan struct{})
		go s.renewGCLeaseDuringRun(gate, renewStop)
	}
	resp, err := run(context.Background())
	if renewStop != nil {
		close(renewStop)
	}
	elapsed := time.Since(start)
	if err != nil {
		log.ErrorContext(gctx, "run failed", "error", err, "elapsed", elapsed)
		return
	}
	if resp.LocalCacheError != "" {
		log.WarnContext(gctx, "localcache prune failed", "error", resp.LocalCacheError)
	}
	log.InfoContext(gctx, "run completed",
		"blobsFreed", resp.BlobsFreed,
		"localCacheFreed", resp.LocalCacheFreed,
		"elapsed", elapsed)
}

// renewGCLeaseDuringRun periodically renews the GC lease (via the same gate the
// tick uses) until stop is closed, so a sweep that outlasts the lease window
// cannot let the lease lapse and a second replica start a concurrent sweep. It
// renews every gcInterval, which is strictly shorter than the lease window
// (max(2*interval, 30s)), so a healthy holder always refreshes with slack before
// expiry. A failed or lost renewal is logged, not fatal — the run continues, and
// the worst case is the same expired-but-alive-holder takeover the lease design
// already tolerates.
func (s *Server) renewGCLeaseDuringRun(gate func(context.Context) (bool, error), stop <-chan struct{}) {
	interval := s.gcInterval
	if interval <= 0 {
		interval = gcLeaseMinDuration
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	ctx := logging.WithAttrs(context.Background(), slog.String("component", "gc"))
	log := logging.FromContext(ctx)
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			held, err := gate(context.Background())
			if err != nil {
				log.WarnContext(ctx, "lease renewal failed during sweep", "error", err)
			} else if !held {
				log.WarnContext(ctx, "lease lost during sweep; another replica may take over")
			}
		}
	}
}

// stopPeriodicGC signals the periodic-GC loop to exit and waits for it, so no
// run can start after shutdown. Safe to call multiple times and safe when the
// loop was never started (gcTicker nil) — s.sup.Wait (closeResources) is what
// actually blocks until the loop has exited; this just releases the ticker's
// own timer resource.
func (s *Server) stopPeriodicGC() {
	if s.gcTicker != nil {
		s.gcTicker.Stop()
	}
}
