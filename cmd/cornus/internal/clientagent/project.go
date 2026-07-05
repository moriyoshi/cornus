package clientagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"cornus/pkg/api"
	"cornus/pkg/clientconduit"
	"cornus/pkg/deploywire"
	"cornus/pkg/logging"
)

// Attacher is the server capability a held session needs: stream a deploy-attach
// session for a spec, keeping its client-local 9P mounts alive for the session's
// lifetime. *client.Client satisfies it.
type Attacher interface {
	DeployAttach(ctx context.Context, spec api.DeploySpec, events func(deploywire.Event)) error
}

// DeployNotice is a deploy diagnostic streamed from a held attach session, for
// user-facing display. Terminal distinguishes a transient diagnostic (Terminal
// false — the workload may still become ready, e.g. an image still pulling or a
// brief CrashLoopBackOff during startup) from a terminal failure event (Terminal
// true). Terminal notices also surface as the session's returned error to a
// pre-ready waiter; the notice additionally covers the post-ready hold phase,
// where no waiter is left to observe the error.
type DeployNotice struct {
	Service  string
	Message  string
	Terminal bool
}

// DeployReporter surfaces deploy notices to the user. An interactive frontend
// (e.g. `compose up`) routes them through its cliout reporter — a transient
// diagnostic as a warning, a terminal failure as an error — so they no longer leak
// through slog. When no reporter is set (the background agent, whose output IS its
// slog stream), notices fall back to slog: transient -> Warn, terminal -> Error.
type DeployReporter func(DeployNotice)

// Option configures a Project.
type Option func(*Project)

// WithDeployReporter routes a Project's deploy notices to r instead of the default
// slog fallback. See DeployReporter.
func WithDeployReporter(r DeployReporter) Option {
	return func(p *Project) {
		if r != nil {
			p.reporter = r
		}
	}
}

// Project holds the live client-side sessions for one compose project. It is a
// small declarative reconcile engine: callers Apply a desired set of services and
// Remove services from it, and the Project drives the live resources to match
// through two level-triggered dimension controllers — a mountController (owns the
// 9P deploy-attach sessions) and an exposureController (owns the conduit
// listeners/aliases). It is the reusable session core shared by the compose mounts
// frontend and the unified agent.
//
// Project is the DECLARATIVE-surface adapter: it exists because a compose file IS a
// desired-state description, so reconcile converts it into imperative backend
// operations. The Docker API proxy (pkg/dockerproxy) is the deliberate imperative
// sibling — its surface (create/start/stop/rm) is already imperative and its
// containers are immutable, so it has no desired-set to reconcile and does NOT use
// Project. The two share the layer beneath the reconcile: the per-workload
// deploy-attach hold (pkg/attachsession, which mountController wraps) and the
// conduit exposure primitive.
type Project struct {
	mounts   *mountController
	exposure *exposureController
	reporter DeployReporter // user-facing deploy notices; nil falls back to slog

	reconcileMu sync.Mutex // serializes reconciles (which block on deploy-attach ready)

	mu      sync.Mutex // guards desired + order + live
	desired map[string]Service
	order   []string // desired service names in first-seen order (reconcile order)
	live    map[string]*liveRecord
}

// liveRecord is the reconciled state reported back for a service (its live local
// port-forwards, for Response.Forwards).
type liveRecord struct {
	forwards []string
}

// NewProject builds an empty Project bound to an attacher (for mount sessions)
// and a conduit (for port exposure). Pass WithDeployReporter to route deploy
// diagnostics to a user-facing reporter instead of the default slog fallback.
func NewProject(attacher Attacher, conduit clientconduit.Conduit, opts ...Option) *Project {
	p := &Project{
		exposure: newExposureController(conduit),
		desired:  map[string]Service{},
		live:     map[string]*liveRecord{},
	}
	for _, opt := range opts {
		opt(p)
	}
	p.mounts = newMountController(attacher, p.reporter)
	return p
}

// mountFingerprint is a stable identity of a service's mount dimension — the
// deployed workload the deploy-attach session holds. It hashes the whole resolved
// DeploySpec, so any workload-shape change recreates the workload (docker compose
// recreate semantics). Managed ingress certificate bytes are transport-only
// Kubernetes Secret content: rotation reconciles the Secret without restarting the
// workload. The host-to-Secret mapping remains part of the fingerprint.
// encoding/json emits struct fields in order and sorts map keys, so equal specs
// always hash equal.
func mountFingerprint(s Service) (string, error) {
	return hashJSON("mount", s.Name, mountFingerprintSpec(s.Spec))
}

func mountFingerprintSpec(spec api.DeploySpec) api.DeploySpec {
	if spec.Ingress == nil || spec.Ingress.TLS == nil || len(spec.Ingress.TLS.ManagedCertificates) == 0 {
		return spec
	}
	copySpec := spec
	copyIngress := *spec.Ingress
	copyTLS := *spec.Ingress.TLS
	copyTLS.ManagedCertificates = append([]api.ManagedIngressCertificate(nil), copyTLS.ManagedCertificates...)
	for i := range copyTLS.ManagedCertificates {
		copyTLS.ManagedCertificates[i].CertificatePEM = nil
		copyTLS.ManagedCertificates[i].PrivateKeyPEM = nil
	}
	copyIngress.TLS = &copyTLS
	copySpec.Ingress = &copyIngress
	return copySpec
}

// exposureFingerprint identifies a service's exposure dimension: the deployment
// name the conduit targets, the alias it registers, and the effective ports. A
// change here re-registers exposure without necessarily touching the mount (e.g.
// toggling port-forwarding keeps the 9P session).
func exposureFingerprint(s Service) (string, error) {
	ports := s.Spec.Ports
	if !s.ForwardPorts {
		ports = nil
	}
	return hashJSON("exposure", s.Name, struct {
		Deployment string
		Alias      string
		Ports      []api.PortMapping
	}{Deployment: s.Spec.Name, Alias: s.Name, Ports: ports})
}

func hashJSON(dim, name string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("fingerprint %s dimension of service %s: %w", dim, name, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// needsMount reports whether a service holds a client-side deploy-attach session.
// A ForwardOnly service was already deployed fire-and-forget by the caller, so the
// project only holds its exposure; every other service opens a deploy-attach
// session (mounted or not) to keep its client-local 9P mounts alive.
func needsMount(s Service) bool { return !s.ForwardOnly }

// Apply merges services into the desired set and reconciles the whole project
// toward it, returning the per-service outcome (Status* values) for the applied
// services. Re-`up` follows docker compose semantics: a service already running an
// identical spec is left alone (StatusUpToDate); one running a changed spec is
// recreated (StatusRecreated); a new service is started (StatusStarted). Reconciles
// are serialized so concurrent requests can't double-start; the desired/live state
// is read and written under a short-held lock so Running/Forwards never block on a
// slow deploy-attach. ctx governs the reconcile's readiness waits only (a
// foreground Ctrl-C aborts a slow pre-ready attach); resources that go live are held
// under their own contexts and outlive ctx.
func (p *Project) Apply(ctx context.Context, services []Service) (map[string]string, error) {
	p.reconcileMu.Lock()
	defer p.reconcileMu.Unlock()

	p.mu.Lock()
	for _, s := range services {
		if _, seen := p.desired[s.Name]; !seen {
			p.order = append(p.order, s.Name)
		}
		p.desired[s.Name] = s
	}
	p.mu.Unlock()

	statuses, err := p.reconcile(ctx)
	applied := make(map[string]string, len(services))
	log := logging.FromContext(ctx)
	for _, s := range services {
		status := statuses[s.Name]
		applied[s.Name] = status
		switch status {
		case StatusUpToDate:
			log.InfoContext(ctx, "service is up-to-date", "service", s.Name)
		case StatusRecreated:
			log.InfoContext(ctx, "recreating service: configuration changed", "service", s.Name)
		}
	}
	if err != nil {
		return applied, err
	}
	return applied, nil
}

// ApplyExact sets the desired set to EXACTLY the given services (dropping any
// previously-desired service not listed) and reconciles the whole project toward
// it, returning the per-service outcome for the listed services. Unlike Apply —
// which merges into the desired set and never drops — ApplyExact makes the
// listed services the complete desired set, so services removed from it are torn
// down by the reconcile (mounted sessions withdrawn, exposures removed). It is
// the reconcile-to-declared-state entry point used by `--watch` reloads, where
// the freshly-loaded compose file IS the complete desired set. Serialized with
// Apply/Remove through reconcileMu.
func (p *Project) ApplyExact(ctx context.Context, services []Service) (map[string]string, error) {
	p.reconcileMu.Lock()
	defer p.reconcileMu.Unlock()

	p.mu.Lock()
	desired := make(map[string]Service, len(services))
	order := make([]string, 0, len(services))
	for _, s := range services {
		if _, seen := desired[s.Name]; !seen {
			order = append(order, s.Name)
		}
		desired[s.Name] = s
	}
	p.desired = desired
	p.order = order
	p.mu.Unlock()

	statuses, err := p.reconcile(ctx)
	applied := make(map[string]string, len(services))
	log := logging.FromContext(ctx)
	for _, s := range services {
		status := statuses[s.Name]
		applied[s.Name] = status
		switch status {
		case StatusUpToDate:
			log.InfoContext(ctx, "service is up-to-date", "service", s.Name)
		case StatusRecreated:
			log.InfoContext(ctx, "recreating service: configuration changed", "service", s.Name)
		}
	}
	if err != nil {
		return applied, err
	}
	return applied, nil
}

// Remove drops the named services from the desired set (or all when names is
// empty) and reconciles, tearing down any resources no longer desired.
func (p *Project) Remove(names []string) {
	p.reconcileMu.Lock()
	defer p.reconcileMu.Unlock()

	p.mu.Lock()
	if len(names) == 0 {
		names = make([]string, 0, len(p.desired))
		for n := range p.desired {
			names = append(names, n)
		}
	}
	for _, n := range names {
		delete(p.desired, n)
	}
	p.order = filterPresent(p.order, p.desired)
	p.mu.Unlock()

	// Teardown-only reconcile: removing desired services and re-touching the
	// survivors (which are up-to-date, so no readiness wait) — a background context
	// suffices.
	_, _ = p.reconcile(context.Background())
}

// reconcile drives the live resources to equal the desired set: it tears down
// resources for services no longer desired, then, for each desired service, brings
// its mount and exposure dimensions to their desired fingerprints. It runs under
// reconcileMu (never holding p.mu while a controller blocks on deploy-attach
// ready). It returns per-service statuses and the first error encountered; on
// error, already-reconciled services stay live (like the previous per-service
// loop).
func (p *Project) reconcile(ctx context.Context) (map[string]string, error) {
	p.mu.Lock()
	desired := make(map[string]Service, len(p.desired))
	for n, s := range p.desired {
		desired[n] = s
	}
	order := append([]string(nil), p.order...)
	p.mu.Unlock()

	// Tear down anything live but no longer desired.
	for _, name := range p.liveNames() {
		if _, ok := desired[name]; ok {
			continue
		}
		p.exposure.remove(name)
		p.mounts.remove(name)
		p.mu.Lock()
		delete(p.live, name)
		p.mu.Unlock()
	}

	// Reconcile desired services in first-seen order: each mount blocks until ready
	// before the next starts, so a service listed after its dependency comes up after
	// it (the dependency ordering the CLI encodes in the request order).
	statuses := make(map[string]string, len(desired))
	for _, name := range order {
		svc, ok := desired[name]
		if !ok {
			continue
		}
		p.mu.Lock()
		_, existed := p.live[name]
		p.mu.Unlock()

		changed, err := p.reconcileService(ctx, name, svc)
		if err != nil {
			return statuses, fmt.Errorf("%s: %w", name, err)
		}
		switch {
		case !existed:
			statuses[name] = StatusStarted
		case changed:
			statuses[name] = StatusRecreated
		default:
			statuses[name] = StatusUpToDate
		}
		p.mu.Lock()
		p.live[name] = &liveRecord{forwards: p.exposure.forwards(name)}
		p.mu.Unlock()
	}
	return statuses, nil
}

// reconcileService brings one service's mount and exposure dimensions to their
// desired fingerprints, reporting whether any live change happened. Exposure is
// parented on the mount's context for a mounted service (so a dying mount withdraws
// it) and on a background context otherwise; a mount recreate forces exposure to
// re-register under the new context.
func (p *Project) reconcileService(ctx context.Context, name string, svc Service) (bool, error) {
	mfp, err := mountFingerprint(svc)
	if err != nil {
		return false, err
	}
	efp, err := exposureFingerprint(svc)
	if err != nil {
		return false, err
	}

	changed := false
	parent := context.Background()
	if needsMount(svc) {
		mctx, mchanged, err := p.mounts.ensure(ctx, name, svc.Spec, mfp)
		if err != nil {
			return false, err
		}
		parent = mctx
		changed = changed || mchanged
	} else if p.mounts.remove(name) {
		changed = true // dropped a mount it no longer needs (e.g. now ForwardOnly)
	}

	_, echanged, err := p.exposure.ensure(parent, name, svc, efp, changed)
	if err != nil {
		return false, err
	}
	return changed || echanged, nil
}

// filterPresent returns the names still present as keys of desired, order
// preserved.
func filterPresent(names []string, desired map[string]Service) []string {
	out := names[:0]
	for _, n := range names {
		if _, ok := desired[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// liveNames lists every service with any live resource (mount, exposure, or a
// reconciled record), deduplicated.
func (p *Project) liveNames() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(names []string) {
		for _, n := range names {
			if _, ok := seen[n]; !ok {
				seen[n] = struct{}{}
				out = append(out, n)
			}
		}
	}
	p.mu.Lock()
	recorded := make([]string, 0, len(p.live))
	for n := range p.live {
		recorded = append(recorded, n)
	}
	p.mu.Unlock()
	add(recorded)
	add(p.mounts.names())
	add(p.exposure.names())
	return out
}

// StartService applies a single service and returns its outcome. A thin wrapper
// over Apply for callers (and tests) that bring services up one at a time.
func (p *Project) StartService(s Service) (string, error) {
	statuses, err := p.Apply(context.Background(), []Service{s})
	if err != nil {
		return "", err
	}
	return statuses[s.Name], nil
}

// DownServices tears down the named services (or all when names is empty).
func (p *Project) DownServices(names []string) { p.Remove(names) }

// Close tears down every live resource (all mount sessions and exposures).
func (p *Project) Close() {
	p.reconcileMu.Lock()
	defer p.reconcileMu.Unlock()
	p.mu.Lock()
	p.desired = map[string]Service{}
	p.order = nil
	p.live = map[string]*liveRecord{}
	p.mu.Unlock()
	p.exposure.close()
	p.mounts.close()
}

// forwardLines renders bound forwards for Response.Forwards.
func forwardLines(fwds []clientconduit.Forward) []string {
	if len(fwds) == 0 {
		return nil
	}
	out := make([]string, 0, len(fwds))
	for _, f := range fwds {
		out = append(out, fmt.Sprintf("%s -> :%d", f.Local, f.Container))
	}
	return out
}

// Running lists the service names with a live session.
func (p *Project) Running() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.live))
	for n := range p.live {
		out = append(out, n)
	}
	return out
}

// Forwards reports the live local port-forwards per service.
func (p *Project) Forwards() map[string][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string][]string{}
	for n, s := range p.live {
		if len(s.forwards) > 0 {
			out[n] = s.forwards
		}
	}
	return out
}
