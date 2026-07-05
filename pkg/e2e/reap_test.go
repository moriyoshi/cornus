package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/client"
)

// reapServer stands in for a cornus server holding several deployments, and
// records every DELETE it receives.
//
// It answers Status for ANY name — including ones this harness never created —
// which is the whole point: it models a shared daemon that also carries a
// developer's own workloads.
type reapServer struct {
	mu      sync.Mutex
	deleted []string
}

func (s *reapServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/.cornus/v1/deploy") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/.cornus/v1/deploy/")
		switch r.Method {
		case http.MethodDelete:
			s.mu.Lock()
			s.deleted = append(s.deleted, name)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			if name == "" || name == "/.cornus/v1/deploy" {
				// The LIST endpoint. A reap that used it would see every
				// deployment on the daemon — which is precisely the mistake this
				// test exists to prevent, so it answers with the foreign ones.
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]api.DeployStatus{
					{Name: "scenario-app", Instances: []api.InstanceStatus{{ID: "1", State: "running", Running: true}}},
					{Name: "someones-postgres", Instances: []api.InstanceStatus{{ID: "2", State: "running", Running: true}}},
				})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(api.DeployStatus{
				Name:      name,
				Instances: []api.InstanceStatus{{ID: "1", State: "running", Running: true}},
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *reapServer) deletedNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deleted...)
}

func reapHarness(t *testing.T, out io.Writer) (*Harness, *reapServer) {
	t.Helper()
	rs := &reapServer{}
	srv := rs.start(t)
	// A `cornus` stub that records its argv, so the compose-down half is
	// observable without a real CLI. `compose ps --quiet` answers with one resource
	// id, so the reap sees a project that still has workloads and proceeds to down
	// it; TestReapComposeProjectSkipsCleanProject covers the other answer.
	bin := filepath.Join(t.TempDir(), "cornus-stub")
	log := filepath.Join(t.TempDir(), "argv.log")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n" +
		"case \"$*\" in *\"ps --quiet\") echo 'p-web';; esac\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAP_ARGV_LOG", log)
	h := New(nopTarget{}, bin, "", out)
	h.registryHost = strings.TrimPrefix(srv.URL, "http://")
	h.client = client.New(srv.URL)
	h.ctx = context.Background()
	return h, rs
}

// TestReapTouchesOnlyWhatTheScenarioCreated is the regression test for a real
// incident, and the reason the reap tracks names instead of listing them.
//
// The obvious implementation — list the server's deployments and delete them all
// — looks equivalent. It is not: on the docker backend List enumerates by the
// `cornus.managed=true` label across the WHOLE daemon, so on a developer machine
// it deletes their own running deployments. That happened during this feature's
// development and cost seven of them. This test fails if anyone reintroduces it:
// the stub's LIST endpoint deliberately reports a foreign deployment, and the
// assertion is that it SURVIVES.
func TestReapTouchesOnlyWhatTheScenarioCreated(t *testing.T) {
	h, rs := reapHarness(t, io.Discard)

	h.recordDeployed("scenario-app")
	h.reapScenarioWorkloads()

	deleted := rs.deletedNames()
	if len(deleted) != 1 || deleted[0] != "scenario-app" {
		t.Fatalf("deleted = %v, want exactly [scenario-app]", deleted)
	}
	for _, n := range deleted {
		if n == "someones-postgres" {
			t.Fatal("the reap deleted a deployment this scenario never created — it is listing the daemon again")
		}
	}
}

// TestReapSkipsAlreadyRemoved: a scenario that cleaned up after itself is the
// normal case, and it must produce neither a DELETE nor a noisy log line.
func TestReapSkipsAlreadyRemoved(t *testing.T) {
	var out bytes.Buffer
	rs := &reapServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Status reports nothing running: the scenario removed it already.
		if r.Method == http.MethodDelete {
			rs.mu.Lock()
			rs.deleted = append(rs.deleted, r.URL.Path)
			rs.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.DeployStatus{Name: "gone"})
	}))
	defer srv.Close()

	h := New(nopTarget{}, "/bin/true", "", &out)
	h.registryHost = strings.TrimPrefix(srv.URL, "http://")
	h.client = client.New(srv.URL)
	h.ctx = context.Background()
	h.recordDeployed("gone")
	h.reapScenarioWorkloads()

	if got := rs.deletedNames(); len(got) != 0 {
		t.Fatalf("deleted %v, want none — the workload was already gone", got)
	}
	if strings.Contains(out.String(), "reaped") {
		t.Fatalf("logged %q for a scenario that cleaned up after itself", out.String())
	}
}

// TestReapTearsDownComposeProjectsByName: a compose project is removed with its
// own `compose down`, scoped to the project, rather than by guessing which
// deployment names belong to it.
func TestReapComposeProjectUsesComposeDown(t *testing.T) {
	h, _ := reapHarness(t, io.Discard)
	h.recordComposedUp("e2e/scenarios/app.yaml", "proj")
	h.reapScenarioWorkloads()

	raw, err := os.ReadFile(os.Getenv("REAP_ARGV_LOG"))
	if err != nil {
		t.Fatalf("the stub was never invoked: %v", err)
	}
	got := strings.TrimSpace(string(raw))
	want := "compose -f e2e/scenarios/app.yaml -p proj ps --quiet\ncompose -f e2e/scenarios/app.yaml -p proj down"
	if got != want {
		t.Fatalf("argv =\n%q\nwant\n%q", got, want)
	}
}

// TestReapComposeProjectSkipsCleanProject: `compose down` is idempotent and exits
// 0 whether it removed anything or not, so running it proves nothing about
// whether there was a leak. A scenario that tore itself down — the common case —
// must produce neither a `down` nor a "reaped leftover" line, or the teardown
// reads as if nearly every scenario in the suite leaked.
func TestReapComposeProjectSkipsCleanProject(t *testing.T) {
	var out bytes.Buffer
	rs := &reapServer{}
	srv := rs.start(t)

	bin := filepath.Join(t.TempDir(), "cornus-stub")
	log := filepath.Join(t.TempDir(), "argv.log")
	// `compose ps --quiet` prints nothing: no service is created.
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h := New(nopTarget{}, bin, "", &out)
	h.registryHost = strings.TrimPrefix(srv.URL, "http://")
	h.client = client.New(srv.URL)
	h.ctx = context.Background()
	h.recordComposedUp("app.yaml", "proj")
	h.reapScenarioWorkloads()

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the stub was never invoked: %v", err)
	}
	if strings.Contains(string(raw), " down") {
		t.Fatalf("ran `compose down` on a project with nothing deployed: %q", raw)
	}
	if strings.Contains(out.String(), "reaped") {
		t.Fatalf("logged a leak for a project that had none: %q", out.String())
	}
}

// TestReapIsInertWithoutAServer: nothing to ask, nothing to do — and in
// particular no panic on a scenario that never called serve().
func TestReapIsInertWithoutAServer(t *testing.T) {
	h := New(nopTarget{}, "/bin/true", "", io.Discard)
	h.ctx = context.Background()
	h.recordDeployed("never-deployed")
	h.recordComposedUp("f.yaml", "p")
	h.reapScenarioWorkloads() // must not panic
}
