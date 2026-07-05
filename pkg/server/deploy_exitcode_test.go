package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploywire"
	"cornus/pkg/wire"
)

// erroringBackend fails every Status call, modelling a backend that is gone or
// wedged when the teardown path asks how the workload ended.
type erroringBackend struct{ fakeBackend }

func (b *erroringBackend) Status(context.Context, string) (api.DeployStatus, error) {
	return api.DeployStatus{}, errors.New("backend unreachable")
}

// exitedWith is a status whose single instance terminated with the given code.
func exitedWith(name string, code int) api.DeployStatus {
	return api.DeployStatus{Name: name, Backend: "fake", Instances: []api.InstanceStatus{
		{ID: name + "-0", State: "exited", Running: false, ExitCode: &code},
	}}
}

// TestFinalExitCode covers the read the deploy-attach teardown takes just before
// deleting the workload — the last moment the exit status exists anywhere. A
// code that is known travels on the terminal event; anything else is nil
// (unknown), never a stand-in 0.
func TestFinalExitCode(t *testing.T) {
	failed := &scriptedBackend{seq: []api.DeployStatus{exitedWith("init", 3)}}
	if got := finalExitCode(failed, "init"); got == nil || *got != 3 {
		t.Fatalf("finalExitCode(exited 3) = %v, want 3", got)
	}

	ok := &scriptedBackend{seq: []api.DeployStatus{completed("init")}}
	if got := finalExitCode(ok, "init"); got == nil || *got != 0 {
		t.Fatalf("finalExitCode(exited 0) = %v, want 0", got)
	}

	// Tearing down a workload that is STILL RUNNING: nobody knows what it would
	// have exited with, so the event must say nothing rather than claim success.
	live := &scriptedBackend{seq: []api.DeployStatus{running("web")}}
	if got := finalExitCode(live, "web"); got != nil {
		t.Fatalf("finalExitCode(running) = %d, want nil (unknown)", *got)
	}

	// A backend that cannot be reached is likewise unknown.
	if got := finalExitCode(&erroringBackend{}, "web"); got != nil {
		t.Fatalf("finalExitCode(status error) = %d, want nil (unknown)", *got)
	}
}

// TestExitCodeOf checks the no-extra-poll variant used on the bring-up failure
// path, where the status has just been read by awaitReady: a workload that
// failed by exiting reports the code it died with, and a status that says
// nothing conclusive stays nil.
func TestExitCodeOf(t *testing.T) {
	if got := exitCodeOf(exitedWith("init", 2)); got == nil || *got != 2 {
		t.Fatalf("exitCodeOf(exited 2) = %v, want 2", got)
	}
	if got := exitCodeOf(pending("init", "ImagePullBackOff")); got != nil {
		t.Fatalf("exitCodeOf(pending) = %d, want nil (unknown)", *got)
	}
	if got := exitCodeOf(api.DeployStatus{Name: "init"}); got != nil {
		t.Fatalf("exitCodeOf(no instances) = %d, want nil (unknown)", *got)
	}
}

// TestDeployAttachTerminalEventCarriesExitCode drives the real attach handler
// end to end and proves the workload's exit status reaches the caller on the
// terminal event. That frame is the only channel that survives teardown: by the
// time the caller could ask, the backend has already deleted the workload and
// with it the exit code. It is what lets `docker wait` through the Docker API
// proxy answer truthfully instead of assuming success.
func TestDeployAttachTerminalEventCarriesExitCode(t *testing.T) {
	// Apply (inherited from fakeBackend) reports a running instance, so bring-up
	// is ready at once; every later Status — including the teardown read — reports
	// the instance as having exited 3.
	fb := &scriptedBackend{seq: []api.DeployStatus{exitedWith("web", 3)}}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/deploy/attach"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	as := deploywire.DeployAttachSpec{Spec: api.DeploySpec{Name: "web", Image: "img"}}
	var (
		mu   sync.Mutex
		last deploywire.Event
	)
	ready := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- deploywire.Serve(ctx, wsURL, as, nil, func(e deploywire.Event) {
			mu.Lock()
			if e.Done {
				last = e
			}
			mu.Unlock()
			if e.Ready {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
		}, nil, wire.ClientTransport{})
	}()

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out waiting for ready event")
	}

	cancel() // graceful "down": the server tears the workload down and finishes
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after disconnect")
	}

	mu.Lock()
	got := last
	mu.Unlock()
	if !got.Done {
		t.Fatal("no terminal event was delivered to the caller")
	}
	if got.ExitCode == nil {
		t.Fatal("terminal event carried no exit code; the caller cannot tell success from failure")
	}
	if *got.ExitCode != 3 {
		t.Fatalf("terminal ExitCode = %d, want 3", *got.ExitCode)
	}
}
