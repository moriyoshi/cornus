package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/ingressmux"
	"cornus/pkg/ingressroute"
	"cornus/pkg/logging"
)

// ingressManager owns the server's own ingress front door — the realization of
// DeploySpec.Ingress for backends that have no ingress controller of their own
// (dockerhost, containerd, bare, incus). It keeps a live host/path routing table
// and serves it through an ingressmux.Proxy, which is what an ingress tunnel
// fronts on those backends and what CORNUS_INGRESS_LISTEN exposes directly.
//
// On kubernetes the real cluster controller plays this role, so the manager is
// only the fallback there (a cluster with no discoverable controller).
//
// Routes are derived at deploy time and persisted under DataDir/ingress so they
// survive a server restart: the backends do not report an IngressSpec back, and
// a front door that forgot its routes whenever the server bounced would be
// useless for anything long-lived. Recovery prunes against the backend's own
// deployment list, so a workload deleted while the server was down does not
// linger as a route.
type ingressManager struct {
	table    *ingressmux.Table
	defaults ingressroute.Defaults
	// dir is DataDir/ingress, where each routed deployment gets a JSON file.
	// Empty disables persistence (the table stays in memory only).
	dir string

	mu        sync.Mutex
	recovered bool
}

// newIngressManager builds the manager. dataDir is the server's data directory;
// an empty one disables persistence.
func newIngressManager(dataDir string) *ingressManager {
	m := &ingressManager{table: ingressmux.NewTable(), defaults: ingressroute.DefaultsFromEnv()}
	if dataDir != "" {
		m.dir = filepath.Join(dataDir, "ingress")
	}
	return m
}

// routeFile is where workload's persisted route lives, or "" for a name that must
// not be turned into a path.
//
// The Base guard is LOAD-BEARING, not decorative: nothing upstream constrains a
// deployment name to a DNS label, so names carrying a separator ("../escape",
// "a/b") do reach here. Rejecting anything that is not its own Base keeps them
// from escaping the directory; "." and ".." survive the check but degrade to the
// harmless in-directory names "..json" and "...json". Do not drop this on the
// assumption that the API boundary already validated the name.
func (m *ingressManager) routeFile(workload string) string {
	if m.dir == "" || workload != filepath.Base(workload) {
		return ""
	}
	return filepath.Join(m.dir, workload+".json")
}

// apply records the routes spec's ingress asks for, replacing any the deployment
// previously had, and withdraws them when it asks for none.
//
// Resolution failures are logged and skipped rather than failing the deploy.
// These backends ignored IngressSpec entirely before this front door existed, so
// a spec that cannot be resolved — most often one with no host and no
// CORNUS_INGRESS_DOMAIN configured — must not start breaking deploys that worked
// yesterday. The warning names the knob that fixes it.
func (m *ingressManager) apply(ctx context.Context, spec api.DeploySpec) {
	if m == nil {
		return
	}
	log := logging.FromContext(ctx)
	if !ingressroute.Enabled(spec.Ingress) {
		m.remove(ctx, spec.Name)
		return
	}
	route, err := ingressroute.Route(spec.Ingress, spec.Ports, spec.Name, m.defaults)
	if err != nil {
		log.WarnContext(ctx, "ingress not served: could not resolve the requested ingress",
			"deployment", spec.Name, "error", err)
		m.remove(ctx, spec.Name)
		return
	}
	m.table.Set(spec.Name, []api.IngressRoute{route})
	m.persist(ctx, spec.Name, route)
	log.InfoContext(ctx, "ingress route published", "deployment", spec.Name,
		"hosts", route.Hosts, "port", route.TargetPort)
}

// remove withdraws workload's routes and forgets its persisted copy.
func (m *ingressManager) remove(ctx context.Context, workload string) {
	if m == nil {
		return
	}
	m.table.Remove(workload)
	if path := m.routeFile(workload); path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logging.FromContext(ctx).WarnContext(ctx, "could not forget persisted ingress route",
				"deployment", workload, "error", err)
		}
	}
}

// persist writes the resolved route so a restarted server can serve it again.
// Best-effort: a write failure costs restart-survival, not the live route.
func (m *ingressManager) persist(ctx context.Context, workload string, route api.IngressRoute) {
	path := m.routeFile(workload)
	if path == "" {
		return
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "could not persist ingress route", "deployment", workload, "error", err)
		return
	}
	b, err := json.Marshal(route)
	if err != nil {
		return
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "could not persist ingress route", "deployment", workload, "error", err)
	}
}

// recover repopulates the table from the persisted routes, once per server
// lifetime, pruning any workload the backend no longer knows about. It is called
// before the front door serves anything, so a restarted server answers with the
// routes it had — minus the deployments that went away while it was down.
//
// Pruning is level-triggered against backend.List rather than trusting the files:
// a deployment deleted through another client, or by the daemon itself, must not
// linger as a route that 502s forever.
func (m *ingressManager) recover(ctx context.Context, backend deploy.Backend) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recovered || m.dir == "" {
		return
	}
	m.recovered = true

	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return // nothing persisted yet
	}
	log := logging.FromContext(ctx)

	live := map[string]bool{}
	known := false
	if backend != nil {
		// Bounded: an unreachable daemon must cost us a pruning pass, not the
		// front door. Recovery without pruning is the safe degradation.
		listCtx, cancel := context.WithTimeout(ctx, ingressListTimeout)
		statuses, err := backend.List(listCtx)
		cancel()
		if err == nil {
			known = true
			for _, st := range statuses {
				live[st.Name] = true
			}
		} else {
			log.WarnContext(ctx, "could not list deployments to prune ingress routes; serving them unpruned", "error", err)
		}
	}

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		workload := e.Name()[:len(e.Name())-len(".json")]
		b, err := os.ReadFile(filepath.Join(m.dir, e.Name()))
		if err != nil {
			continue
		}
		var route api.IngressRoute
		if err := json.Unmarshal(b, &route); err != nil {
			log.WarnContext(ctx, "dropping unreadable persisted ingress route", "deployment", workload, "error", err)
			_ = os.Remove(filepath.Join(m.dir, e.Name()))
			continue
		}
		if known && !live[workload] && !m.table.Has(workload) {
			log.InfoContext(ctx, "pruning ingress route for a deployment that no longer exists", "deployment", workload)
			_ = os.Remove(filepath.Join(m.dir, e.Name()))
			continue
		}
		// A deploy that landed while recovery was reading the directory already
		// published the live route; its persisted copy is by definition no newer,
		// so recovery must not overwrite it.
		if m.table.Has(workload) {
			continue
		}
		route.Workload = workload
		m.table.Set(workload, []api.IngressRoute{route})
	}
	if hosts := m.table.Hosts(); len(hosts) > 0 {
		log.InfoContext(ctx, "recovered ingress routes", "hosts", hosts)
	}
}

// ingressListTimeout bounds the deployment list recovery uses to prune stale
// routes. It is deliberately short: pruning is a nicety, and a wedged daemon must
// not hold the front door's first request open. A var, not a const, so tests can
// shrink it rather than waiting out the real timeout (mirrors
// tunnelChannelWaitTimeout).
var ingressListTimeout = 10 * time.Second

// handler returns the front-door handler, recovering persisted routes first.
func (m *ingressManager) handler(ctx context.Context, backend deploy.Backend) http.Handler {
	m.recover(ctx, backend)
	return ingressmux.NewProxy(m.table, deploy.PortForwardDialer{Backend: backend})
}

// lazyHandler is the front door as an http.Handler that resolves the deploy
// backend on FIRST REQUEST rather than at construction.
//
// Constructing the backend (and recovering routes through it) can block on an
// unresponsive daemon. Doing that while the server is still starting would keep
// the API listener from ever binding — no /healthz, nothing to say why — which is
// why preflightBackend next door runs in a goroutine. Deferring it here means a
// wedged daemon costs the front door a 503, and nothing else.
//
// Resolution is retried on later requests rather than latched, so a daemon that
// was down at first contact and later recovers does not leave the front door
// permanently broken.
func (s *Server) lazyIngressHandler() http.Handler {
	var (
		mu      sync.Mutex
		handler http.Handler
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		h := handler
		if h == nil {
			backend, err := s.getBackend()
			if err != nil {
				mu.Unlock()
				logging.FromContext(r.Context()).WarnContext(r.Context(),
					"ingress front door: deploy backend unavailable", "error", err)
				http.Error(w, "ingress front door: deploy backend unavailable", http.StatusServiceUnavailable)
				return
			}
			h = s.ingress.handler(r.Context(), backend)
			handler = h
		}
		mu.Unlock()
		h.ServeHTTP(w, r)
	})
}

// ingressListenAddr is the optional address the emulated front door binds
// directly (CORNUS_INGRESS_LISTEN), for reaching the ingress over the network
// the server sits on without any tunnel. Empty means the front door is only
// reachable through an ingress tunnel.
func ingressListenAddr() string {
	return os.Getenv("CORNUS_INGRESS_LISTEN")
}

// serveIngressListener binds CORNUS_INGRESS_LISTEN and serves the front door
// there until ctx is cancelled. It is a no-op when the variable is unset.
//
// It binds and returns; it never waits on the deploy backend (see
// lazyIngressHandler), so a wedged daemon cannot keep the server from starting.
// A bind failure is fatal to this listener only, not to the server: the front
// door is an optional exposure, and refusing to start the whole server because
// port 80 is taken would be a poor trade.
func (s *Server) serveIngressListener(ctx context.Context) error {
	addr := ingressListenAddr()
	if addr == "" {
		return nil
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("ingress listener on %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           s.lazyIngressHandler(),
		ReadHeaderTimeout: 30 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	logging.FromContext(ctx).InfoContext(ctx, "ingress front door listening", "addr", lis.Addr().String())
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := srv.Serve(lis); err != nil && err != http.ErrServerClosed {
			logging.FromContext(ctx).ErrorContext(ctx, "ingress front door stopped", "error", err)
		}
	}()
	return nil
}

// nil-safety: a Server may be assembled without an ingress manager (tests,
// registry-only wiring). apply/remove above no-op in that case, and these
// accessors keep /info answerable.

// front reports whether the server has an emulated ingress front door at all.
func (m *ingressManager) front() bool { return m != nil }

// routes exposes the routing table for the ingress-tunnel wiring (host aliases).
// A nil manager yields a throwaway table so callers need no nil check; nothing
// serves it, which is the correct behavior for a server with no front door.
func (m *ingressManager) routes() *ingressmux.Table {
	if m == nil {
		return ingressmux.NewTable()
	}
	return m.table
}

// hostsFor reports the ingress hostnames the server routes for workload.
func (m *ingressManager) hostsFor(workload string) []string {
	if m == nil {
		return nil
	}
	return m.table.HostsFor(workload)
}
