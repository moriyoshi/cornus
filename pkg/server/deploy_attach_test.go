package server

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/wire"
)

// TestDeployAttachLifecycle drives the real /.cornus/v1/deploy/attach handler over a
// WebSocket with no client-local mounts (so it runs unprivileged): the backend
// receives the apply, and disconnecting tears the deployment down. That the
// WebSocket upgrade succeeds also proves mux precedence routes "attach" here
// rather than to handleDeployItem (which would treat it as a deployment name).
func TestDeployAttachLifecycle(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/deploy/attach"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	as := deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{Name: "web", Image: "localhost:5000/web:v1"},
	}

	ready := make(chan *api.DeployStatus, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- deploywire.Serve(ctx, wsURL, as, nil, func(e deploywire.Event) {
			if e.Ready {
				select {
				case ready <- e.Status:
				default:
				}
			}
		}, nil, wire.ClientTransport{})
	}()

	select {
	case st := <-ready:
		if st == nil || st.Name != "web" {
			t.Fatalf("ready status = %+v", st)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for ready event")
	}
	if !fb.hasApplied("web") {
		t.Fatal("backend did not receive apply")
	}

	// Disconnect -> the server must tear the deployment down.
	cancel()
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after disconnect")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fb.wasDeleted("web") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("deployment was not torn down on disconnect")
}

// TestDeployAttachStopsTunnelOnTeardown proves a deploy-attach teardown tears
// down a tunnel opened for the same deployment name, mirroring the DELETE
// handler. Without it the tunnel outlives the ephemeral deployment as a leaked
// goroutine and open public endpoint.
func TestDeployAttachStopsTunnelOnTeardown(t *testing.T) {
	clearAuthEnv(t)
	fb := &fakeBackend{}
	prov := &fakeTunnelProvider{conns: make(chan net.Conn, 1), url: "https://attach.example"}
	s, srv := newTunnelTestServer(t, fb, prov)

	// Open a tunnel for the name the attach session will use.
	if _, err := s.tunnels.start(fb, "web", "tok", nil, 8080, "http"); err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if !s.tunnels.status("web").Active {
		t.Fatal("tunnel not active after start")
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/deploy/attach"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	as := deploywire.DeployAttachSpec{Spec: api.DeploySpec{Name: "web", Image: "localhost:5000/web:v1"}}

	ready := make(chan *api.DeployStatus, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- deploywire.Serve(ctx, wsURL, as, nil, func(e deploywire.Event) {
			if e.Ready {
				select {
				case ready <- e.Status:
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

	// Disconnect: teardown must remove the deployment AND stop its tunnel.
	cancel()
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after disconnect")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !s.tunnels.status("web").Active {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("tunnel was not stopped on deploy-attach teardown")
}

// TestDeployAttachNotTreatedAsName confirms a plain (non-WebSocket) GET to
// /.cornus/v1/deploy/attach is not routed to handleDeployItem as a deployment named
// "attach": that handler would return 200 with a status JSON, whereas the
// WebSocket handler rejects a non-upgrade request.
func TestDeployAttachNotTreatedAsName(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.cornus/v1/deploy/attach")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("plain GET /.cornus/v1/deploy/attach returned 200 (treated as a deployment name)")
	}
}

// TestRejectFileMounts guards the sidecar/attachment (kubernetes) mount paths:
// a single-file client-local mount (LocalMount.Subpath set) is rejected because
// the 9P mount sidecar can only propagate a directory mount, not place one file at
// an arbitrary rootfs target. Directory mounts (no Subpath) pass through.
func TestRejectFileMounts(t *testing.T) {
	dirOnly := []deploywire.LocalMount{{Index: 0, Name: "m0"}}
	if err := rejectFileMounts(dirOnly, "kubernetes"); err != nil {
		t.Errorf("directory mount rejected: %v", err)
	}
	withFile := []deploywire.LocalMount{
		{Index: 0, Name: "m0"},
		{Index: 1, Name: "m1", Subpath: "app.conf"},
	}
	err := rejectFileMounts(withFile, "kubernetes")
	if err == nil {
		t.Fatal("file mount (Subpath set) was not rejected")
	}
	if !strings.Contains(err.Error(), "kubernetes") {
		t.Errorf("error should name the backend, got %q", err.Error())
	}
}

// fakeRemoteMountingBackend adds RemoteCapable to fakeMountingBackend so
// useSidecarMounts's gating can be exercised directly.
type fakeRemoteMountingBackend struct {
	fakeMountingBackend
	remote bool
}

func (f *fakeRemoteMountingBackend) Remote() bool { return f.remote }

// TestUseSidecarMounts proves the RemoteCapable gate added alongside
// dockerhost/containerdhost's new ApplyWithMounts: a MountingBackend that does
// NOT implement RemoteCapable (kubernetes — no host-mount fallback to prefer
// instead) is always eligible for the sidecar path; one that DOES implement it
// is eligible only when Remote() is true — so simply adding ApplyWithMounts to
// dockerhost/containerdhost never silently steals their deploys away from the
// existing co-located fast path (applyWithHostMounts) unless the operator has
// explicitly opted in (CORNUS_DOCKER_REMOTE / CORNUS_CONTAINERD_REMOTE).
func TestUseSidecarMounts(t *testing.T) {
	mb := &fakeMountingBackend{mounts: make(chan []deploy.AttachMount, 1)}
	if !useSidecarMounts(mb) {
		t.Error("a MountingBackend with no RemoteCapable should always use the sidecar path")
	}

	notRemote := &fakeRemoteMountingBackend{fakeMountingBackend: fakeMountingBackend{mounts: make(chan []deploy.AttachMount, 1)}, remote: false}
	if useSidecarMounts(notRemote) {
		t.Error("RemoteCapable.Remote()==false should NOT use the sidecar path")
	}

	remote := &fakeRemoteMountingBackend{fakeMountingBackend: fakeMountingBackend{mounts: make(chan []deploy.AttachMount, 1)}, remote: true}
	if !useSidecarMounts(remote) {
		t.Error("RemoteCapable.Remote()==true should use the sidecar path")
	}
}
