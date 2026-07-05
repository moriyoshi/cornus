//go:build linux

package containerdhost

import (
	"context"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// newRemoteTestBackend is newTestBackend with WithRemote(true) and an agent
// image configured — the mode ApplyWithMounts/Apply's always-on companion is
// actually exercised in (see deploy.RemoteCapable / useSidecarMounts).
func newRemoteTestBackend(t interface {
	Helper()
	TempDir() string
	Fatalf(string, ...any)
}, f *fakeClient) (*Backend, *fakeNet) {
	t.Helper()
	b, fn := newTestBackend(t, f)
	b.remote = true
	b.agentImage = "cornus:latest"
	return b, fn
}

// TestApplyRemoteCompanionWithoutMounts proves the always-on remote companion
// is created for every instance whenever the backend is in remote mode, even
// when the spec has no client-local mounts at all — the companion is not
// mount-triggered any more (see startRemoteCompanion / apply's remote tail).
func TestApplyRemoteCompanionWithoutMounts(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus.host:5000")
	f := newFakeClient()
	b, _ := newRemoteTestBackend(t, f)
	ctx := context.Background()

	st, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "nginx:alpine"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(st.Instances) != 1 {
		t.Fatalf("Status instances = %d, want 1 (companion filtered out)", len(st.Instances))
	}

	if _, ok := f.containers["cornus-web-0"]; !ok {
		t.Fatalf("no app instance; created=%v", f.created)
	}
	comp, ok := f.containers["cornus-web-mount-0"]
	if !ok {
		t.Fatalf("no remote companion despite remote mode; created=%v", f.created)
	}
	if !isCompanion(comp.labels) {
		t.Errorf("companion missing role label: %v", comp.labels)
	}
	if comp.labels[deploy.LabelApp] != "web" {
		t.Errorf("companion app label = %q, want web", comp.labels[deploy.LabelApp])
	}
	if !strings.Contains(comp.image, "cornus:latest") {
		t.Errorf("companion image = %q, want the cornus agent image", comp.image)
	}
	// The companion must NOT own a netns (it joins the app's) or networks label,
	// mirroring the egress companion's precedent (TestApplyWithEgressCompanion).
	if comp.labels[labelNetNS] != "" || comp.labels[labelNetworks] != "" {
		t.Errorf("companion must not carry netns/networks labels: %v", comp.labels)
	}

	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := f.containers["cornus-web-mount-0"]; ok {
		t.Error("Delete left the remote companion")
	}
	if _, ok := f.containers["cornus-web-0"]; ok {
		t.Error("Delete left the app instance")
	}
}

// TestApplyRemoteRequiresAgentImage proves remote mode without an agent image
// configured is a hard error, matching dockerhost's precedent.
func TestApplyRemoteRequiresAgentImage(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	b.remote = true // no agentImage set
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "nginx:alpine"}); err == nil {
		t.Error("remote mode without an agent image should be rejected")
	}
}

// TestApplyWithMountsRemoteCompanion proves ApplyWithMounts's mount roles ride
// the SAME always-on companion (one companion per instance, not a separate
// mount-only sidecar).
func TestApplyWithMountsRemoteCompanion(t *testing.T) {
	f := newFakeClient()
	b, _ := newRemoteTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "web",
		Image: "nginx:alpine",
		Mounts: []api.Mount{
			{Source: "/should/never/be/used", Target: "/data", ReadOnly: true},
		},
	}
	mounts := []deploy.AttachMount{{
		Target:     "/data",
		ReadOnly:   true,
		Session:    "sess-1",
		Name:       "m0",
		RelayURL:   "ws://cornus.host:5000/.cornus/v1/caretaker/attach",
		AgentImage: "cornus:latest",
	}}

	st, err := b.ApplyWithMounts(ctx, spec, mounts)
	if err != nil {
		t.Fatalf("ApplyWithMounts: %v", err)
	}
	if len(st.Instances) != 1 {
		t.Fatalf("Status instances = %d, want 1", len(st.Instances))
	}
	comp, ok := f.containers["cornus-web-mount-0"]
	if !ok {
		t.Fatalf("no remote companion; created=%v", f.created)
	}
	if !isCompanion(comp.labels) {
		t.Errorf("companion missing role label: %v", comp.labels)
	}

	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
