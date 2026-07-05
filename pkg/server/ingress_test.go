package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// ingressSpec is a deployment that publishes one port and asks for an ingress on
// the given hosts.
func ingressSpec(name string, port int, hosts ...string) api.DeploySpec {
	return api.DeploySpec{
		Name:    name,
		Ports:   []api.PortMapping{{Container: port}},
		Ingress: &api.IngressSpec{Enabled: true, Hosts: hosts},
	}
}

func TestIngressManagerPublishesAndWithdrawsRoutes(t *testing.T) {
	m := newIngressManager(t.TempDir())
	ctx := context.Background()

	m.apply(ctx, ingressSpec("web", 8080, "app.example.com"))
	if got := m.table.Hosts(); len(got) != 1 || got[0] != "app.example.com" {
		t.Fatalf("Hosts() = %v, want [app.example.com]", got)
	}
	route, _, ok := m.table.Lookup("app.example.com", "/")
	if !ok || route.Workload != "web" || route.TargetPort != 8080 {
		t.Fatalf("Lookup = %+v (ok=%v), want web:8080", route, ok)
	}

	m.remove(ctx, "web")
	if !m.table.Empty() {
		t.Error("routes should be withdrawn when the deployment is deleted")
	}
}

func TestIngressManagerRedeployWithoutIngressWithdraws(t *testing.T) {
	m := newIngressManager(t.TempDir())
	ctx := context.Background()

	m.apply(ctx, ingressSpec("web", 8080, "app.example.com"))
	// The same deployment redeployed with the ingress turned off must stop being
	// routed — otherwise a removed ingress keeps serving until the server bounces.
	m.apply(ctx, api.DeploySpec{Name: "web", Ports: []api.PortMapping{{Container: 8080}}})
	if !m.table.Empty() {
		t.Error("a redeploy without an ingress must withdraw the previous routes")
	}
	if _, err := os.Stat(filepath.Join(m.dir, "web.json")); !os.IsNotExist(err) {
		t.Error("the persisted route must be forgotten too")
	}
}

// TestIngressManagerUnresolvableSpecDoesNotFailDeploy pins the compatibility
// promise: host backends ignored IngressSpec entirely before the front door
// existed, so a spec that cannot be resolved is skipped with a warning rather
// than breaking a deploy that worked yesterday.
func TestIngressManagerUnresolvableSpecDoesNotFailDeploy(t *testing.T) {
	m := newIngressManager(t.TempDir())
	// Enabled, but no host and no CORNUS_INGRESS_DOMAIN to derive one from.
	m.apply(context.Background(), api.DeploySpec{
		Name:    "web",
		Ports:   []api.PortMapping{{Container: 80}},
		Ingress: &api.IngressSpec{Enabled: true},
	})
	if !m.table.Empty() {
		t.Error("an unresolvable ingress must not be routed")
	}
}

// TestIngressManagerSkipsClientEmulated proves a client-emulated ingress stays
// the client's job: the server must not also route it, or the same hostname
// would be served twice with different semantics.
func TestIngressManagerSkipsClientEmulated(t *testing.T) {
	m := newIngressManager(t.TempDir())
	spec := ingressSpec("web", 8080, "app.example.com")
	spec.Ingress.ClientEmulated = true
	m.apply(context.Background(), spec)
	if !m.table.Empty() {
		t.Error("a client-emulated ingress must not be served by the server")
	}
}

func TestIngressManagerRecoversPersistedRoutes(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	first := newIngressManager(dir)
	first.apply(ctx, ingressSpec("web", 8080, "app.example.com"))
	first.apply(ctx, ingressSpec("gone", 9090, "gone.example.com"))

	// A fresh manager over the same data directory stands for a restarted server.
	// The backend still has "web" but not "gone", so only "web" survives.
	restarted := newIngressManager(dir)
	restarted.recover(ctx, &listBackend{names: []string{"web"}})

	if got := restarted.table.Hosts(); len(got) != 1 || got[0] != "app.example.com" {
		t.Fatalf("recovered hosts = %v, want [app.example.com]", got)
	}
	route, _, ok := restarted.table.Lookup("app.example.com", "/")
	if !ok || route.Workload != "web" || route.TargetPort != 8080 {
		t.Fatalf("recovered route = %+v (ok=%v), want web:8080", route, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "ingress", "gone.json")); !os.IsNotExist(err) {
		t.Error("a route for a deployment the backend no longer has must be pruned from disk")
	}
}

// TestIngressManagerRecoveryKeepsRoutesWhenListFails proves a backend that
// cannot be listed does not silently drop every route: an unreachable daemon at
// startup must not look like "every deployment was deleted".
func TestIngressManagerRecoveryKeepsRoutesWhenListFails(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	newIngressManager(dir).apply(ctx, ingressSpec("web", 8080, "app.example.com"))

	restarted := newIngressManager(dir)
	restarted.recover(ctx, &listBackend{err: os.ErrDeadlineExceeded})
	if restarted.table.Empty() {
		t.Error("routes must survive a failed deployment list")
	}
}

func TestIngressManagerHandlerServesRoutes(t *testing.T) {
	m := newIngressManager(t.TempDir())
	ctx := context.Background()
	m.apply(ctx, ingressSpec("web", 8080, "app.example.com"))

	h := m.handler(ctx, &listBackend{names: []string{"web"}})
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.Host = "app.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// The fake backend's ForwardPort never connects anything, so the proxy answers
	// 502 — what matters here is that the request ROUTED (a 421 would mean the
	// table never saw the host).
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (routed but unreachable)", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "http://other.example.com/", nil)
	req.Host = "other.example.com"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("unknown host status = %d, want 421", rec.Code)
	}
}

// TestNilIngressManagerIsInert proves a Server assembled without an ingress
// manager (tests, registry-only wiring) still takes deploys and answers /info.
func TestNilIngressManagerIsInert(t *testing.T) {
	var m *ingressManager
	m.apply(context.Background(), ingressSpec("web", 80, "app.example.com"))
	m.remove(context.Background(), "web")
	if m.front() {
		t.Error("a nil manager must not advertise a front door")
	}
}

// listBackend is a deploy.Backend that only answers List and refuses to forward
// ports, standing in for a daemon whose workloads are not actually reachable.
type listBackend struct {
	deploy.Backend
	names []string
	err   error
}

func (b *listBackend) List(context.Context) ([]api.DeployStatus, error) {
	if b.err != nil {
		return nil, b.err
	}
	out := make([]api.DeployStatus, 0, len(b.names))
	for _, n := range b.names {
		out = append(out, api.DeployStatus{Name: n})
	}
	return out, nil
}

func (b *listBackend) ForwardPort(context.Context, string, int, string, io.ReadWriteCloser) error {
	return os.ErrClosed
}

// hangingBackend blocks in List until released, standing in for a wedged daemon.
type hangingBackend struct {
	deploy.Backend
	release chan struct{}
	called  chan struct{}
	once    sync.Once
}

func (b *hangingBackend) List(ctx context.Context) ([]api.DeployStatus, error) {
	b.once.Do(func() { close(b.called) })
	select {
	case <-b.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *hangingBackend) ForwardPort(context.Context, string, int, string, io.ReadWriteCloser) error {
	return os.ErrClosed
}

// TestIngressRecoverBoundedByListTimeout proves an unresponsive daemon costs a
// pruning pass, not the front door: recover returns on the bounded List context
// instead of blocking forever, and the persisted routes are served unpruned.
func TestIngressRecoverBoundedByListTimeout(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	newIngressManager(dir).apply(ctx, ingressSpec("web", 8080, "app.example.com"))

	be := &hangingBackend{release: make(chan struct{}), called: make(chan struct{})}
	defer close(be.release)

	// Shrink the bound so the test does not wait out the production timeout.
	saved := ingressListTimeout
	ingressListTimeout = 50 * time.Millisecond
	defer func() { ingressListTimeout = saved }()

	done := make(chan struct{})
	m := newIngressManager(dir)
	go func() { m.recover(ctx, be); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("recover blocked on an unresponsive backend instead of bounding its List")
	}
	if m.table.Empty() {
		t.Error("routes must survive a List that timed out")
	}
}

// TestIngressRecoverDoesNotClobberConcurrentDeploy proves recovery yields to a
// route a deploy already published: the persisted copy is by definition no newer.
func TestIngressRecoverDoesNotClobberConcurrentDeploy(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	// An older persisted route for "web" on the old host and port.
	newIngressManager(dir).apply(ctx, ingressSpec("web", 8080, "old.example.com"))

	restarted := newIngressManager(dir)
	// A deploy lands before recovery runs, publishing a newer route.
	restarted.apply(ctx, ingressSpec("web", 9090, "new.example.com"))
	restarted.recover(ctx, &listBackend{names: []string{"web"}})

	route, _, ok := restarted.table.Lookup("new.example.com", "/")
	if !ok || route.TargetPort != 9090 {
		t.Fatalf("live route = %+v (ok=%v), want the freshly deployed port 9090", route, ok)
	}
	if _, known, _ := restarted.table.Lookup("old.example.com", "/"); known {
		t.Error("recovery resurrected the stale host over the live one")
	}
}

// TestLazyIngressHandlerDoesNotBlockOnBackend proves the front door binds and
// answers without ever waiting on the deploy backend at startup: a backend that
// cannot be constructed yields a 503 per request, not a hung server.
func TestLazyIngressHandlerDoesNotBlockOnBackend(t *testing.T) {
	s := &Server{ingress: newIngressManager(t.TempDir())}
	s.newBackend = func() (deploy.Backend, error) { return nil, errors.New("daemon unreachable") }

	h := s.lazyIngressHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the deploy backend cannot be constructed", rec.Code)
	}

	// Resolution is retried, not latched: a daemon that recovers must un-break the
	// front door without a server restart.
	s.newBackend = func() (deploy.Backend, error) { return &listBackend{names: []string{"web"}}, nil }
	s.ingress.apply(context.Background(), ingressSpec("web", 8080, "app.example.com"))
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.Host = "app.example.com"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (routed once the backend became available)", rec.Code)
	}
}
