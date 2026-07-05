//go:build linux

package containerdhost

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

func TestApplyWithEgressCompanion(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:   "web",
		Image:  "nginx:alpine",
		Egress: &api.EgressSpec{Mode: "proxy", Rules: []api.EgressRule{{Pattern: "*.internal", Route: "cluster"}}},
	}
	egress := &deploy.AttachEgress{
		Session:    "sess-1",
		RelayURL:   "ws://cornus.host:5000/.cornus/v1/caretaker/attach",
		AgentImage: "cornus:latest",
		Spec:       spec.Egress,
	}
	st, err := b.ApplyWithEgress(ctx, spec, egress)
	if err != nil {
		t.Fatalf("ApplyWithEgress: %v", err)
	}

	// Status reports exactly one instance — the app, not the companion.
	if len(st.Instances) != 1 {
		t.Fatalf("Status instances = %d, want 1 (companion filtered out)", len(st.Instances))
	}

	// Both an app instance and a companion exist; the companion joins the app's
	// pinned netns and carries the role label + the agent image.
	app, ok := f.containers["cornus-web-0"]
	if !ok {
		t.Fatalf("no app instance; created=%v", f.created)
	}
	comp, ok := f.containers["cornus-web-egress-0"]
	if !ok {
		t.Fatalf("no egress companion; created=%v", f.created)
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
	// The companion must NOT own a netns (it joins the app's) or networks label.
	if comp.labels[labelNetNS] != "" || comp.labels[labelNetworks] != "" {
		t.Errorf("companion must not carry netns/networks labels: %v", comp.labels)
	}
	// The app's pinned netns is what the companion joined (appNetnsPath found it).
	if !strings.HasPrefix(app.labels[labelNetNS], "/run/cornus/netns/") {
		t.Errorf("app netns label = %q", app.labels[labelNetNS])
	}
	// The agent image was pulled.
	pulled := strings.Join(f.pulled, " ")
	if !strings.Contains(pulled, "cornus:latest") {
		t.Errorf("agent image not pulled: %v", f.pulled)
	}

	// Delete reaps BOTH the app and the companion.
	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := f.containers["cornus-web-egress-0"]; ok {
		t.Error("Delete left the egress companion")
	}
	if _, ok := f.containers["cornus-web-0"]; ok {
		t.Error("Delete left the app instance")
	}
}

// TestApplyWithEgressTransparent confirms the transparent path runs and creates the
// companion joining the app's netns. The caretaker Config content (transparent +
// SetupRedirect + Mark) and the NET_ADMIN capability ride opaque OCI SpecOpts the
// fake does not materialize; that content is asserted in the dockerhost egress test,
// which builds the identical caretaker.Config and captures the create body.
func TestApplyWithEgressTransparent(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{Name: "web", Image: "nginx:alpine", Egress: &api.EgressSpec{Mode: "transparent"}}
	egress := &deploy.AttachEgress{Session: "s", RelayURL: "ws://x", AgentImage: "cornus:latest", Spec: spec.Egress}
	if _, err := b.ApplyWithEgress(ctx, spec, egress); err != nil {
		t.Fatalf("ApplyWithEgress(transparent): %v", err)
	}
	comp, ok := f.containers["cornus-web-egress-0"]
	if !ok {
		t.Fatalf("no companion; created=%v", f.created)
	}
	if !isCompanion(comp.labels) {
		t.Errorf("companion missing role label: %v", comp.labels)
	}
}

func TestApplyWithEgressRejects(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	ctx := context.Background()

	env := &deploy.AttachEgress{Session: "s", RelayURL: "ws://x", AgentImage: "cornus:latest", Spec: &api.EgressSpec{Mode: "env"}}
	if _, err := b.ApplyWithEgress(ctx, api.DeploySpec{Name: "a", Image: "img", Egress: env.Spec}, env); err == nil {
		t.Error("env mode is not a relay mode and should be rejected here")
	}
	noimg := &deploy.AttachEgress{Session: "s", RelayURL: "ws://x", Spec: &api.EgressSpec{Mode: "proxy"}}
	if _, err := b.ApplyWithEgress(ctx, api.DeploySpec{Name: "a", Image: "img", Egress: noimg.Spec}, noimg); err == nil {
		t.Error("egress without an agent image should be rejected")
	}
}

func TestApplyWithEgressReplicas(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{Name: "web", Image: "nginx:alpine", Replicas: 3, Egress: &api.EgressSpec{Mode: "proxy"}}
	egress := &deploy.AttachEgress{Session: "s", RelayURL: "ws://x", AgentImage: "cornus:latest", Spec: spec.Egress}
	st, err := b.ApplyWithEgress(ctx, spec, egress)
	if err != nil {
		t.Fatalf("ApplyWithEgress(replicas=3): %v", err)
	}
	// Status reports the 3 app instances only (companions filtered).
	if len(st.Instances) != 3 {
		t.Fatalf("Status instances = %d, want 3 (companions filtered)", len(st.Instances))
	}
	// One companion per replica, each joining a distinct app instance's pinned netns.
	for i := 0; i < 3; i++ {
		comp, ok := f.containers[fmt.Sprintf("cornus-web-egress-%d", i)]
		if !ok {
			t.Fatalf("missing companion for replica %d; created=%v", i, f.created)
		}
		if !isCompanion(comp.labels) {
			t.Errorf("companion %d missing role label", i)
		}
	}

	// Delete reaps all 3 apps + 3 companions.
	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for id := range f.containers {
		if strings.Contains(id, "web") {
			t.Fatalf("Delete left %q", id)
		}
	}
}

func TestMergeEgressProxyEnv(t *testing.T) {
	got := mergeEgressProxyEnv(
		map[string]string{"EXISTING": "keep", "HTTP_PROXY": "user"},
		api.EgressSpec{Rules: []api.EgressRule{{Pattern: "*.internal", Route: "cluster"}}},
		15002,
	)
	if got["EXISTING"] != "keep" {
		t.Error("existing non-proxy env should be preserved")
	}
	if got["HTTP_PROXY"] != "http://127.0.0.1:15002" {
		t.Errorf("HTTP_PROXY = %q, want the caretaker proxy (authoritative)", got["HTTP_PROXY"])
	}
	if !strings.Contains(got["NO_PROXY"], "*.internal") {
		t.Errorf("NO_PROXY = %q, want the cluster-route pattern", got["NO_PROXY"])
	}
}
