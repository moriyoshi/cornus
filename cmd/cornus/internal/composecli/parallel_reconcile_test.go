package composecli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/clientconduit"
	"cornus/pkg/compose"
)

// TestSuppressCascaded pins suppressCascaded's contract: a genuine error (gctx
// still live) propagates; an error that only surfaced because gctx is already
// done (cancellation fallout from a sibling's failure, or the up's own real
// Ctrl-C/SIGTERM) is suppressed to nil, so errgroup captures exactly the one
// meaningful error instead of racing it against N cancellation-shaped ones.
func TestSuppressCascaded(t *testing.T) {
	live, cancel := context.WithCancel(context.Background())
	defer cancel()

	if got := suppressCascaded(live, nil); got != nil {
		t.Errorf("nil error should stay nil, got %v", got)
	}
	genuine := errNoInstances
	if got := suppressCascaded(live, genuine); got != genuine {
		t.Errorf("genuine error on a live gctx must propagate unchanged, got %v", got)
	}

	cancel()
	if got := suppressCascaded(live, genuine); got != nil {
		t.Errorf("an error surfacing after gctx is done must be suppressed, got %v", got)
	}
	if got := suppressCascaded(live, nil); got != nil {
		t.Errorf("nil error must stay nil regardless of gctx state, got %v", got)
	}
}

// errNoInstances is a stand-in error value for TestSuppressCascaded.
var errNoInstances = &testErr{"no instances"}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }

// deployFake is a minimal httptest-backed POST/GET /.cornus/v1/deploy server:
// each POST records the deployed spec's arrival time (mutex-guarded), and each
// GET /.cornus/v1/deploy/{name} calls that service's scripted statusAt(pollCount)
// function. It exists so UpCmd.upDetached's per-service goroutines can be
// exercised end-to-end (through a real *client.Client) without a live cornus
// server.
type deployFake struct {
	mu         sync.Mutex
	deployedAt map[string]time.Time
	polls      map[string]int
	statusAt   map[string]func(pollCount int) api.DeployStatus
}

func newDeployFake() *deployFake {
	return &deployFake{
		deployedAt: map[string]time.Time{},
		polls:      map[string]int{},
		statusAt:   map[string]func(int) api.DeployStatus{},
	}
}

func (f *deployFake) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/.cornus/v1/deploy", func(w http.ResponseWriter, r *http.Request) {
		var spec api.DeploySpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.deployedAt[spec.Name] = time.Now()
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(api.DeployStatus{Name: spec.Name}) //nolint:errcheck
	})
	mux.HandleFunc("/.cornus/v1/deploy/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/.cornus/v1/deploy/")
		f.mu.Lock()
		f.polls[name]++
		n := f.polls[name]
		fn := f.statusAt[name]
		f.mu.Unlock()
		var st api.DeployStatus
		if fn != nil {
			st = fn(n)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(st) //nolint:errcheck
	})
	return httptest.NewServer(mux)
}

func (f *deployFake) deployTime(name string) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.deployedAt[name]
	return t, ok
}

// runningAfter returns a statusAt func that reports not-yet-running for the
// first n-1 polls, then running from the n'th poll on.
func runningAfter(n int) func(int) api.DeployStatus {
	return func(poll int) api.DeployStatus {
		if poll < n {
			return status(inst("i-0", "pending", false))
		}
		return status(inst("i-0", "running", true))
	}
}

// testRuntime builds a minimal *runtime good enough to drive UpCmd.upDetached
// directly (bypassing cli.load's kong/network setup) against a fake server:
// mount-free, port-free, relay-free specs only, so upDetached returns after its
// per-service loop without ever reaching the background-agent handoff.
func testRuntime(url string, services map[string]compose.ServiceDocument, plans map[string]compose.ServicePlan) *runtime {
	var out bytes.Buffer
	return &runtime{
		out:     testDriver(&out),
		project: compose.NewProject(&compose.ProjectDocument{Services: services}).View(nil),
		plans:   plans,
		client:  client.New(url),
		baseDir: ".",
	}
}

func plan(service, image string) compose.ServicePlan {
	return compose.ServicePlan{
		Service:  service,
		Resource: service,
		Spec:     api.DeploySpec{Name: service, Image: image},
	}
}

// TestUpDetachedIndependentServicesDeployConcurrently is the regression guard
// for the parallel reconcile scheduler: two services with no depends_on
// relationship must not serialize behind each other's reconcile wait. "slow"
// takes several poll cycles (at the real reconcilePollInterval) to report
// running; "fast" is running from its first poll. Under the OLD sequential
// loop, "fast" would not even get its POST /.cornus/v1/deploy call issued until
// "slow"'s entire reconcile finished — several poll intervals later. Under the
// new concurrent scheduler, both POSTs land close together regardless of
// "slow"'s pace. names is deliberately [slow, fast] (not alphabetical) so a
// sequential loop's dependency-ordering artifact can't accidentally pass this.
func TestUpDetachedIndependentServicesDeployConcurrently(t *testing.T) {
	fake := newDeployFake()
	fake.statusAt["slow"] = runningAfter(3) // ~3 poll cycles before running
	fake.statusAt["fast"] = runningAfter(1) // running from the first poll
	srv := fake.server()
	defer srv.Close()

	services := map[string]compose.ServiceDocument{"slow": {}, "fast": {}}
	plans := map[string]compose.ServicePlan{
		"slow": plan("slow", "img"),
		"fast": plan("fast", "img"),
	}
	rt := testRuntime(srv.URL, services, plans)

	c := &UpCmd{NoForwardPorts: true}
	start := time.Now()
	if err := c.upDetached(&Cmd{}, rt, clientconduit.Config{}, []string{"slow", "fast"}); err != nil {
		t.Fatalf("upDetached: %v", err)
	}

	fastAt, ok := fake.deployTime("fast")
	if !ok {
		t.Fatal("fast was never deployed")
	}
	// "slow" needs 3 poll cycles at reconcilePollInterval before it reports
	// running; if the loop were still sequential, "fast"'s deploy could not have
	// landed before that wait started resolving. A generous fraction of one poll
	// interval is enough headroom to distinguish "issued immediately, in
	// parallel" from "waited behind slow's multi-cycle reconcile".
	if d := fastAt.Sub(start); d > reconcilePollInterval {
		t.Errorf("fast's deploy landed %s after start; want well under one poll interval (%s), proving it did not wait behind slow's reconcile", d, reconcilePollInterval)
	}
}

// TestUpDetachedDependentServiceWaitsForDependency confirms that launching
// every service's goroutine at once does not bypass depends_on: "web" depends
// on "db" (service_started, the default condition) and must not be deployed
// until "db" reports running, even though both goroutines start immediately.
func TestUpDetachedDependentServiceWaitsForDependency(t *testing.T) {
	fake := newDeployFake()
	fake.statusAt["db"] = runningAfter(3)
	fake.statusAt["web"] = runningAfter(1)
	srv := fake.server()
	defer srv.Close()

	services := map[string]compose.ServiceDocument{
		"db":  {},
		"web": {DependsOn: compose.DependsOn{{Service: "db", Condition: compose.DependsOnStarted, Required: true}}},
	}
	plans := map[string]compose.ServicePlan{
		"db":  plan("db", "img"),
		"web": plan("web", "img"),
	}
	rt := testRuntime(srv.URL, services, plans)

	c := &UpCmd{NoForwardPorts: true}
	if err := c.upDetached(&Cmd{}, rt, clientconduit.Config{}, []string{"db", "web"}); err != nil {
		t.Fatalf("upDetached: %v", err)
	}

	dbAt, ok := fake.deployTime("db")
	if !ok {
		t.Fatal("db was never deployed")
	}
	webAt, ok := fake.deployTime("web")
	if !ok {
		t.Fatal("web was never deployed")
	}
	if !webAt.After(dbAt) {
		t.Errorf("web deployed at %s, db at %s; web must not deploy before its dependency", webAt, dbAt)
	}
	// db needs 3 poll cycles to report running; web's own deploy is gated on
	// waitForDependencies observing that, so it must land at least ~2 poll
	// intervals after db's own deploy (1 poll already elapsed by the time db's
	// POST is even issued and observed once).
	if d := webAt.Sub(dbAt); d < reconcilePollInterval {
		t.Errorf("web deployed only %s after db; want at least ~%s (it must have waited for db's condition, not raced ahead)", d, reconcilePollInterval)
	}
}
