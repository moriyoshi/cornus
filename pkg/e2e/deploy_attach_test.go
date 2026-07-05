package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.starlark.net/starlark"

	"cornus/pkg/api"
	"cornus/pkg/client"
)

// leftoverStatusServer stands in for a cornus server whose daemon still carries a
// RUNNING workload of that name from an earlier, failed run: every Status call
// answers "1/1 running" — including the ones made before the current run's deploy
// has done anything at all.
func leftoverStatusServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/.cornus/v1/deploy/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/.cornus/v1/deploy/")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.DeployStatus{
			Name:      name,
			Image:     "alpine:3.20",
			Backend:   "dockerhost",
			Instances: []api.InstanceStatus{{ID: "leftover", State: "running", Running: true}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// attachHarness wires a harness whose `cornus` binary is the given shell stub and
// whose server is the always-running status stub above.
func attachHarness(t *testing.T, stub string) *Harness {
	t.Helper()
	srv := leftoverStatusServer(t)
	bin := filepath.Join(t.TempDir(), "cornus-stub")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+stub), 0o755); err != nil {
		t.Fatal(err)
	}
	h := New(nopTarget{}, bin, "", io.Discard)
	h.dataRoot = t.TempDir()
	h.registryHost = strings.TrimPrefix(srv.URL, "http://")
	h.client = client.New(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	t.Cleanup(func() {
		cancel()
		for _, at := range h.attaches {
			select {
			case <-at.done:
			case <-time.After(30 * time.Second):
			}
		}
	})
	return h
}

func callDeployAttach(t *testing.T, h *Harness, name, timeout string) (starlark.Value, error) {
	t.Helper()
	return h.bDeployAttach(nil, nil, nil, []starlark.Tuple{
		{starlark.String("name"), starlark.String(name)},
		{starlark.String("image"), starlark.String("alpine:3.20")},
		{starlark.String("timeout"), starlark.String(timeout)},
	})
}

// TestDeployAttachIgnoresLeftoverWorkload is the regression guard for the flake
// that cost two activity-flight-record runs: deploy_attach used to wait by
// polling Status(name), which ANY running container carrying the deployment's
// label satisfies — including one a previous failed run left on the same daemon.
// The wait then returned instantly, before this run's deploy had done anything,
// and the scenario asserted against a half-empty world.
//
// Here the server reports a leftover as running from the first poll, while the
// deploy child never announces readiness. The builtin must NOT return ready.
func TestDeployAttachIgnoresLeftoverWorkload(t *testing.T) {
	// A deploy that connects and then hangs (e.g. a mount that never settles):
	// it prints progress, but never its readiness line.
	h := attachHarness(t, "echo 'deploying app to ws://x (Ctrl-C to tear down)...'\nexec sleep 60\n")

	done := make(chan error, 1)
	go func() {
		v, err := callDeployAttach(t, h, "app", "2s")
		if err == nil {
			done <- fmt.Errorf("deploy_attach returned ready (%v) while only a LEFTOVER workload was running", v)
			return
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "ready") {
			t.Fatalf("err = %v, want a readiness timeout", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("deploy_attach neither returned nor timed out")
	}
}

// TestDeployAttachWaitsForSessionReady covers the success path: the same
// leftover-reporting server, but now the deploy child announces its own session
// ready, which is the only thing that may satisfy the wait. The returned dict is
// the status read back afterwards.
func TestDeployAttachWaitsForSessionReady(t *testing.T) {
	// Announce only after a beat, so a wait that returns instantly (the bug) is
	// distinguishable from one that actually waited for this session.
	h := attachHarness(t, "sleep 1\necho 'deployed app: 1/1 instances running'\nexec sleep 60\n")

	start := time.Now()
	v, err := callDeployAttach(t, h, "app", "30s")
	if err != nil {
		t.Fatalf("deploy_attach: %v", err)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatalf("returned after %v: it cannot have waited for the session's own readiness", time.Since(start))
	}
	d, ok := v.(*starlark.Dict)
	if !ok {
		t.Fatalf("returned %T, want a status dict", v)
	}
	got, found, err := d.Get(starlark.String("running"))
	if err != nil || !found {
		t.Fatalf("status dict has no `running` key: %v", err)
	}
	if got.String() != "1" {
		t.Fatalf("running = %v, want 1", got)
	}
}

// TestDeployAttachOtherDeploymentReadyLine asserts the wait is scoped to THIS
// deployment as well as this run: a sibling deploy announcing readiness for a
// name that merely shares a prefix must not satisfy it.
func TestDeployAttachOtherDeploymentReadyLine(t *testing.T) {
	h := attachHarness(t, "echo 'deployed app2: 1/1 instances running'\nexec sleep 60\n")

	_, err := callDeployAttach(t, h, "app", "2s")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a readiness timeout", err)
	}
}

// TestDeployAttachChildExitBeforeReady asserts a deploy that dies without
// announcing readiness is reported as such — with its output, which is where the
// reason is — instead of being covered for by a leftover workload's status.
func TestDeployAttachChildExitBeforeReady(t *testing.T) {
	h := attachHarness(t, "echo 'deploy error: image pull failed'\nexit 1\n")

	_, err := callDeployAttach(t, h, "app", "30s")
	if err == nil || !strings.Contains(err.Error(), "exited before the workload became ready") {
		t.Fatalf("err = %v, want an early-exit error", err)
	}
	if !strings.Contains(err.Error(), "image pull failed") {
		t.Fatalf("err = %v, want the child's output included", err)
	}
}

// TestSawAttachReady pins the marker matching itself, including the name-boundary
// rule that keeps one deployment's readiness from satisfying another's wait.
func TestSawAttachReady(t *testing.T) {
	cases := []struct {
		name string
		out  string
		dep  string
		want bool
	}{
		{"server log line", "deployed flight\n", "flight", true},
		{"client result line", "deployed flight: 1/1 instances running\n", "flight", true},
		{"with origin suffix", "deployed flight: 1/1 instances running (origin: host)\n", "flight", true},
		{"progress only", "deploying flight to ws://h (Ctrl-C to tear down)...\n", "flight", false},
		{"empty", "", "flight", false},
		{"longer name", "deployed flight2: 1/1 instances running\n", "flight", false},
		{"hyphenated sibling", "deployed flight-2: 1/1 instances running\n", "flight", false},
		{"shorter name matched by longer output", "deployed ai-app: 1/1 instances running\n", "ai-app", true},
		{"later line", "deploy error: waiting\ndeployed flight\n", "flight", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sawAttachReady(tc.out, tc.dep); got != tc.want {
				t.Fatalf("sawAttachReady(%q, %q) = %v, want %v", tc.out, tc.dep, got, tc.want)
			}
		})
	}
}
