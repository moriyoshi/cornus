package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/creddelivery"
	"cornus/pkg/credential"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/logging"
	"cornus/pkg/netnsbind"
)

// credEndpointBasePort is the first loopback port an endpoint delivery is
// assigned inside the workload's namespace. It matches the kubernetes backend's
// base deliberately: the same credential should be found at the same place
// whichever backend a workload lands on, or an app's configuration stops being
// portable for no reason a user could see.
const credEndpointBasePort = 9200

// credRebindDelay is how long a serve loop waits before trying again after the
// namespace could not be resolved or the listener died. It is deliberately not a
// backoff: the dominant cause is a container that has not started yet or is
// restarting, which resolves in seconds, and a growing delay would turn a
// two-second restart into a much longer credential outage.
const credRebindDelay = time.Second

// credNetnsPollInterval is how often a serving endpoint re-checks that the
// namespace it is bound in is still the workload's. It costs one stat, so it can
// be brisk; the bound is how long a restarted workload waits for its credential.
const credNetnsPollInterval = 2 * time.Second

// canEnterNetns is netnsbind.CanEnter, indirected so tests can drive the routing
// without root. `go test ./...` must pass unprivileged, and the real probe
// answers "no" on any developer machine — which would make every endpoint
// routing test assert the refusal instead of the route.
//
// The refusal itself is covered by a test that sets this to a failure.
var canEnterNetns = netnsbind.CanEnter

// credentialEndpoints is one deploy's endpoint deliveries, resolved to addresses
// before the workload exists.
//
// Assignment has to happen first because the app discovers its endpoint through
// environment variables, and those are fixed into the create request. Binding
// cannot happen first, because on some backends there is no namespace to bind in
// until the container starts. Splitting the two is what lets both be true.
type credentialEndpoints struct {
	endpoints []deploy.CredentialEndpoint
	env       map[string]string
}

// prepareCredentialEndpoints resolves every endpoint delivery in spec to an
// address and the environment variables that advertise it. It touches no
// namespace and starts nothing; it is pure planning.
//
// Returns nil when spec declares no endpoint delivery.
func prepareCredentialEndpoints(ctx context.Context, spec api.DeploySpec) (*credentialEndpoints, error) {
	if spec.Credentials == nil {
		return nil, nil
	}
	log := logging.FromContext(ctx, slog.String("component", "credential-endpoints"))
	ce := &credentialEndpoints{env: map[string]string{}}
	port := credEndpointBasePort
	for _, src := range spec.Credentials.Sources {
		for _, d := range src.Deliveries {
			if !isEndpointDelivery(d) {
				continue
			}
			var cfg map[string]string
			if d.Upstream != "" {
				cfg = map[string]string{"upstream": d.Upstream}
			}
			ep, err := creddelivery.Open(d.Provider, cfg)
			if err != nil {
				return nil, fmt.Errorf("credential %q: %w", src.Name, err)
			}
			addr := ""
			wk := false
			if d.WellKnown && ep.WellKnownAddr() != "" {
				addr = ep.WellKnownAddr()
				wk = true
			} else {
				if d.WellKnown {
					// Asked for a canonical address from a provider that has
					// none. Falling back silently would leave an SDK looking at
					// a link-local address nothing is bound to.
					log.WarnContext(ctx, "credential provider has no well-known address; using a loopback port and the provider's env instead",
						"credential", src.Name, "provider", deliveryProvider(d))
				}
				addr = fmt.Sprintf("127.0.0.1:%d", port)
				port++
			}
			// A later source overwriting an earlier one's variable is legitimate
			// here, and refusing it would reject specs kubernetes accepts. The
			// generic provider deliberately emits BOTH a name-qualified variable
			// and a shared CORNUS_CREDENTIALS_URL convenience for the common
			// single-source case, documenting the shared one as last-write-wins;
			// kubernetes appends in order and lets the last win. Sources are
			// walked in spec order, so which one wins is at least deterministic,
			// and the name-qualified variable still names every credential.
			for k, v := range ep.Env(src.Name, addr) {
				ce.env[k] = v
			}
			ce.endpoints = append(ce.endpoints, deploy.CredentialEndpoint{
				Name: src.Name, Provider: d.Provider, Addr: addr, WellKnown: wk, Upstream: d.Upstream,
			})
		}
	}
	if len(ce.endpoints) == 0 {
		return nil, nil
	}
	return ce, nil
}

// withEnv merges the endpoint environment into spec, refusing a collision with a
// variable the caller already set.
//
// Refusing rather than overwriting is the same call WithCredentialEnv makes for
// the egress proxy vars, for the same reason: a workload that comes up healthy
// pointing at the wrong endpoint is indistinguishable from success.
func (ce *credentialEndpoints) withEnv(spec api.DeploySpec) (api.DeploySpec, error) {
	if ce == nil || len(ce.env) == 0 {
		return spec, nil
	}
	var clashes []string
	for k, v := range ce.env {
		if prev, ok := spec.Env[k]; ok && prev != v {
			clashes = append(clashes, k)
		}
	}
	if len(clashes) > 0 {
		sort.Strings(clashes)
		return spec, fmt.Errorf(
			"credential endpoint delivery collides with the deployment's own environment: %v already set; "+
				"rename the variable or drop it from the spec", clashes)
	}
	env := make(map[string]string, len(spec.Env)+len(ce.env))
	for k, v := range spec.Env {
		env[k] = v
	}
	for k, v := range ce.env {
		env[k] = v
	}
	spec.Env = env
	return spec, nil
}

// serveCredentialEndpoints starts one supervised serve loop per (replica,
// endpoint) and returns a teardown that stops them all.
//
// It runs AFTER Apply, which is not a detail: on dockerhost there is no network
// namespace to enter until the container has started, so there is nothing to
// bind into before then. The consequence is a startup window in which the app
// can reach its endpoint before the listener exists. That is accepted for this
// delivery kind — a connection refused is retryable and every SDK that reads a
// credential endpoint already retries it — where the same window would be
// unacceptable for a file, which is opened once with no second chance.
func (s *Server) serveCredentialEndpoints(ctx context.Context, ce *credentialEndpoints, sess *deploywire.ServerSession, spec api.DeploySpec, binder deploy.CredentialEndpointBinder) func() {
	if ce == nil || len(ce.endpoints) == 0 {
		return func() {}
	}
	replicas := spec.Replicas
	if replicas < 1 {
		replicas = 1
	}
	serveCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	fetchers := map[string]*credFetcher{}
	var wg sync.WaitGroup
	for _, ep := range ce.endpoints {
		// One fetcher per credential NAME, shared across replicas and
		// deliveries, so N replicas do not mint N credentials from a source that
		// rate-limits or that issues a distinct token per call.
		f, ok := fetchers[ep.Name]
		if !ok {
			f = &credFetcher{sess: sess, name: ep.Name, ttl: credentialTTL(spec, ep.Name)}
			fetchers[ep.Name] = f
		}
		for replica := 0; replica < replicas; replica++ {
			wg.Add(1)
			go func(ep deploy.CredentialEndpoint, replica int, f *credFetcher) {
				defer wg.Done()
				s.serveOneCredentialEndpoint(serveCtx, ep, replica, spec.Name, f, binder)
			}(ep, replica, f)
		}
	}
	return func() { stop(); wg.Wait() }
}

// serveOneCredentialEndpoint binds and serves one endpoint for one replica,
// rebinding whenever the namespace goes away, until ctx is cancelled.
//
// The rebind loop is what makes a workload restart survivable. On dockerhost a
// restarted container has a new pid and therefore a new namespace path; on
// containerd and bare the pin normally survives a task restart, but not the
// namespace rebuild that reboot recovery performs. Re-resolving through the
// backend on every attempt — rather than caching the path from the first one —
// is what covers both.
func (s *Server) serveOneCredentialEndpoint(ctx context.Context, ep deploy.CredentialEndpoint, replica int, app string, f *credFetcher, binder deploy.CredentialEndpointBinder) {
	log := logging.FromContext(ctx,
		slog.String("component", "credential-endpoints"),
		slog.String("deployment", app),
		slog.String("credential", ep.Name),
		slog.Int("replica", replica))
	provider, err := creddelivery.Open(ep.Provider, endpointProviderConfig(ep))
	if err != nil {
		log.ErrorContext(ctx, "credential endpoint provider is unavailable; this endpoint will not be served", "error", err.Error())
		return
	}
	// Bind failures are expected for the first moment or two — the container is
	// still starting — and pathological after that. Logging every attempt at warn
	// would bury a real problem in noise from the normal case; logging them all at
	// debug would make "the workload never got its credential" a silent failure
	// whose only symptom is inside the application. So the loop stays quiet until
	// the delay stops being explicable by startup, and says so once.
	failures := 0
	const failuresBeforeComplaining = 10 // ~10s at credRebindDelay
	for {
		if ctx.Err() != nil {
			return
		}
		ln, bound, err := s.bindCredentialEndpoint(ctx, ep, replica, app, binder)
		if err != nil {
			failures++
			switch {
			case failures == failuresBeforeComplaining:
				log.WarnContext(ctx, "credential endpoint still cannot be bound; the workload cannot read this credential and will keep failing until it can",
					"addr", ep.Addr, "attempts", failures, "error", err.Error())
			default:
				log.DebugContext(ctx, "credential endpoint not bound yet; retrying", "error", err.Error())
			}
			if !sleepCtx(ctx, credRebindDelay) {
				return
			}
			continue
		}
		if failures >= failuresBeforeComplaining {
			log.InfoContext(ctx, "credential endpoint recovered", "addr", ep.Addr, "afterAttempts", failures)
		}
		failures = 0
		log.InfoContext(ctx, "serving credential endpoint inside the workload's network namespace", "addr", ep.Addr)
		// Watch for the namespace being replaced underneath us, and close the
		// listener when it is.
		//
		// This is not belt-and-braces; without it a restarted workload never gets
		// its endpoint back. A listener whose network namespace was destroyed does
		// NOT fail — the listener itself holds a reference to the namespace, so the
		// socket stays open, Accept blocks forever on a namespace nothing else can
		// reach, and Serve never returns. The loop below would then wait on a call
		// that has no reason to come back, while the workload gets connection
		// refused from its shiny new namespace. Measured, not theorised: this is
		// what credentials-endpoint-host.star's restart arm caught.
		watchCtx, stopWatch := context.WithCancel(ctx)
		var replaced atomic.Bool
		go watchNetnsReplaced(watchCtx, ln, bound, log, &replaced)
		// A deliberate close is the NORMAL end of a serve: the watcher saw the
		// namespace replaced and closed the listener to unblock Serve, which then
		// reports "use of closed network connection". Warning about it would put a
		// scary line in the log on every ordinary workload restart, right beside
		// the one that already explains what happened.
		if err := provider.Serve(ctx, ln, f.get); err != nil && ctx.Err() == nil && !replaced.Load() {
			log.WarnContext(ctx, "credential endpoint stopped; rebinding", "error", err.Error())
		}
		stopWatch()
		_ = ln.Close()
		if !sleepCtx(ctx, credRebindDelay) {
			return
		}
	}
}

// watchNetnsReplaced closes ln once the namespace it was bound in is gone or has
// been replaced, which is what lets the serve loop rebind.
//
// It compares the IDENTITY of the namespace handle rather than merely checking
// that the path still exists. On dockerhost the handle is /proc/<pid>/ns/net, and
// a restarted container gets a new pid — but a pid can also be REUSED, in which
// case the old path exists again and names a completely different namespace. An
// existence check would call that healthy and keep serving a credential into
// somewhere it does not belong.
//
// A poll rather than an event: nothing here can watch a namespace for
// destruction, and this costs one stat per interval with no call to the runtime.
func watchNetnsReplaced(ctx context.Context, ln net.Listener, bound netnsHandle, log *slog.Logger, replaced *atomic.Bool) {
	for {
		if !sleepCtx(ctx, credNetnsPollInterval) {
			return
		}
		if bound.stillCurrent() {
			continue
		}
		log.InfoContext(ctx, "the workload's network namespace was replaced; rebinding the credential endpoint",
			"netns", bound.path)
		// Set BEFORE closing, so the serve loop can tell this close from a real
		// failure by the time Serve returns.
		replaced.Store(true)
		// Closing makes Serve return, which is the only way to unblock it.
		_ = ln.Close()
		return
	}
}

// netnsHandle is a namespace path plus the identity it had when it was bound, so
// a later look can tell "same namespace" from "same path".
type netnsHandle struct {
	path string
	info os.FileInfo
}

func newNetnsHandle(path string) netnsHandle {
	info, err := os.Stat(path)
	if err != nil {
		// Leave info nil: stillCurrent then reports false and the loop rebinds,
		// which is the safe direction — a namespace we cannot identify is one we
		// should not keep serving into.
		return netnsHandle{path: path}
	}
	return netnsHandle{path: path, info: info}
}

func (h netnsHandle) stillCurrent() bool {
	if h.info == nil {
		return false
	}
	now, err := os.Stat(h.path)
	if err != nil {
		return false
	}
	return os.SameFile(h.info, now)
}

// bindCredentialEndpoint resolves the replica's namespace and binds the
// endpoint's address inside it.
func (s *Server) bindCredentialEndpoint(ctx context.Context, ep deploy.CredentialEndpoint, replica int, app string, binder deploy.CredentialEndpointBinder) (net.Listener, netnsHandle, error) {
	nsPath, err := binder.InstanceNetns(ctx, app, replica)
	if err != nil {
		return nil, netnsHandle{}, err
	}
	if ep.WellKnown {
		// A link-local address is not carried by a fresh namespace, so it has to
		// be added before anything can bind it. Inside the namespace, so it is
		// the workload that gains the address and not the host.
		host, _, serr := net.SplitHostPort(ep.Addr)
		if serr != nil {
			return nil, netnsHandle{}, fmt.Errorf("well-known address %q: %w", ep.Addr, serr)
		}
		if err := netnsbind.EnsureLocalAddr(nsPath, host); err != nil {
			return nil, netnsHandle{}, err
		}
	}
	// Identity is captured BEFORE the bind, so a namespace swapped between the two
	// is caught by the first poll rather than treated as the one we bound in.
	bound := newNetnsHandle(nsPath)
	ln, err := netnsbind.Listen(nsPath, "tcp", ep.Addr)
	if err != nil {
		return nil, netnsHandle{}, err
	}
	return ln, bound, nil
}

// credFetcher mints and caches one credential over the held deploy-attach
// session, so a burst of requests from the workload does not become a burst of
// mints on the client.
//
// It is the server-side twin of the caretaker's fetcher and shares its TTL
// arithmetic through pkg/credential, which is the point of that package: two
// answers to "when is this credential stale" would only ever surface as one path
// serving a value the other had already replaced.
type credFetcher struct {
	sess *deploywire.ServerSession
	name string
	ttl  time.Duration

	mu     sync.Mutex
	cached credential.Credential
	expiry time.Time
	have   bool
}

func (f *credFetcher) get(ctx context.Context) (credential.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	if f.have && now.Before(f.expiry) {
		return f.cached, nil
	}
	cred, err := fetchCredentialValue(f.sess, f.name)
	if err != nil {
		return credential.Credential{}, err
	}
	f.cached = cred
	f.have = true
	f.expiry = credential.Expiry(now, cred.Expiration, f.ttl)
	return cred, nil
}

// isEndpointDelivery reports whether d is an endpoint delivery. The empty kind
// means endpoint, which is why this is a predicate rather than a comparison.
func isEndpointDelivery(d api.CredentialDelivery) bool {
	return d.Kind == "" || d.Kind == "endpoint"
}

// specHasEndpointDelivery reports whether spec declares any endpoint delivery.
func specHasEndpointDelivery(spec api.DeploySpec) bool {
	if spec.Credentials == nil {
		return false
	}
	for _, src := range spec.Credentials.Sources {
		for _, d := range src.Deliveries {
			if isEndpointDelivery(d) {
				return true
			}
		}
	}
	return false
}

// deliveryProvider names the provider for diagnostics, spelling the default.
func deliveryProvider(d api.CredentialDelivery) string {
	if d.Provider == "" {
		return creddelivery.DefaultProvider
	}
	return d.Provider
}

// endpointProviderConfig rebuilds the non-secret provider config from a resolved
// endpoint, mirroring the caretaker's endpointConfig.
func endpointProviderConfig(ep deploy.CredentialEndpoint) map[string]string {
	if ep.Upstream == "" {
		return nil
	}
	return map[string]string{"upstream": ep.Upstream}
}

// credentialTTL resolves the refresh hint for one source.
func credentialTTL(spec api.DeploySpec, name string) time.Duration {
	if spec.Credentials != nil {
		for _, src := range spec.Credentials.Sources {
			if src.Name == name {
				return credential.ParseTTL(src.TTL)
			}
		}
	}
	return credential.DefaultTTL
}

// sleepCtx waits for d, reporting false if ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// backendBindsCredentialEndpoints reports whether backend can serve endpoint
// deliveries from this server, mirroring backendBindsCredentialDir.
func backendBindsCredentialEndpoints(ctx context.Context, backend deploy.Backend) bool {
	b, ok := backend.(deploy.CredentialEndpointBinder)
	return ok && b.BindsCredentialEndpoints(ctx)
}
