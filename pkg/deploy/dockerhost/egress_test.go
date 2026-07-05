package dockerhost

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
)

func TestApplyWithEgressCompanion(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "web",
		Image: "nginx:alpine",
		Egress: &api.EgressSpec{
			Mode:  "proxy",
			Rules: []api.EgressRule{{Pattern: "*.internal", Route: "cluster"}},
		},
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
		t.Fatalf("Status instances = %d, want 1 (companion must be filtered out)", len(st.Instances))
	}

	// The app container got the proxy env pointed at the loopback proxy.
	var appBody, compBody *createBody
	for i := range f.created {
		c := &f.created[i]
		if c.Labels[labelRole] == roleEgressCaretaker {
			compBody = c
		} else if c.Labels[deploy.LabelApp] == "web" {
			appBody = c
		}
	}
	if appBody == nil || compBody == nil {
		t.Fatalf("want an app AND a companion container; created=%d", len(f.created))
	}
	env := strings.Join(appBody.Env, " ")
	if !strings.Contains(env, "HTTP_PROXY=http://127.0.0.1:15002") {
		t.Errorf("app HTTP_PROXY not injected: %v", appBody.Env)
	}
	if !strings.Contains(env, "*.internal") {
		t.Errorf("app NO_PROXY should carry the cluster-route pattern: %v", appBody.Env)
	}

	// The companion shares the app's netns and carries the egress caretaker config.
	if !strings.HasPrefix(compBody.HostConfig.NetworkMode, "container:") {
		t.Errorf("companion NetworkMode = %q, want container:<app>", compBody.HostConfig.NetworkMode)
	}
	if compBody.Image != "cornus:latest" || strings.Join(compBody.Cmd, " ") != "caretaker" {
		t.Errorf("companion image/cmd = %q/%v", compBody.Image, compBody.Cmd)
	}
	var cfg caretaker.Config
	for _, e := range compBody.Env {
		if strings.HasPrefix(e, "CORNUS_CARETAKER_CONFIG=") {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(e, "CORNUS_CARETAKER_CONFIG=")), &cfg)
		}
	}
	if cfg.Egress == nil || cfg.Egress.Session != "sess-1" || cfg.Egress.Mode != "proxy" || cfg.Egress.ListenPort != 15002 {
		t.Fatalf("companion egress role = %+v", cfg.Egress)
	}
	if cfg.Egress.Server != "ws://cornus.host:5000/.cornus/v1/caretaker/attach" {
		t.Errorf("companion relay server = %q", cfg.Egress.Server)
	}

	// Delete reaps BOTH the app and the companion.
	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.containers) != 0 {
		t.Fatalf("Delete left %d containers, want 0 (app + companion reaped)", len(f.containers))
	}
}

func TestApplyWithEgressTransparent(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{Name: "web", Image: "nginx:alpine", Egress: &api.EgressSpec{Mode: "transparent"}}
	egress := &deploy.AttachEgress{Session: "s", RelayURL: "ws://x", AgentImage: "cornus:latest", Spec: spec.Egress}
	if _, err := b.ApplyWithEgress(ctx, spec, egress); err != nil {
		t.Fatalf("ApplyWithEgress(transparent): %v", err)
	}
	var comp, app *createBody
	for i := range f.created {
		if f.created[i].Labels[labelRole] == roleEgressCaretaker {
			comp = &f.created[i]
		} else if f.created[i].Labels[deploy.LabelApp] == "web" {
			app = &f.created[i]
		}
	}
	if comp == nil || app == nil {
		t.Fatalf("want app + companion; created=%d", len(f.created))
	}
	// Transparent: the companion has NET_ADMIN (redirect + SO_MARK); the app gets NO
	// proxy env (all TCP is captured by the redirect).
	if len(comp.HostConfig.CapAdd) != 1 || comp.HostConfig.CapAdd[0] != "NET_ADMIN" {
		t.Errorf("companion CapAdd = %v, want [NET_ADMIN]", comp.HostConfig.CapAdd)
	}
	for _, e := range app.Env {
		if strings.HasPrefix(e, "HTTP_PROXY=") {
			t.Errorf("transparent mode must not inject proxy env, found %q", e)
		}
	}
	var cfg caretaker.Config
	for _, e := range comp.Env {
		if strings.HasPrefix(e, "CORNUS_CARETAKER_CONFIG=") {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(e, "CORNUS_CARETAKER_CONFIG=")), &cfg)
		}
	}
	if cfg.Egress == nil || cfg.Egress.Mode != "transparent" || !cfg.Egress.SetupRedirect {
		t.Errorf("companion egress role = %+v, want transparent + SetupRedirect", cfg.Egress)
	}
	if cfg.Mark != egressMark {
		t.Errorf("companion Mark = %d, want %d (redirect exemption)", cfg.Mark, egressMark)
	}
}

func TestApplyWithEgressRejects(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
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
	f := &fakeDocker{}
	b := newTestBackend(t, f)
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
	// Each replica has its OWN companion.
	var companions []*createBody
	for i := range f.created {
		if c := &f.created[i]; c.Labels[labelRole] == roleEgressCaretaker {
			companions = append(companions, c)
		}
	}
	if len(companions) != 3 {
		t.Fatalf("want 3 companions, got %d", len(companions))
	}
	// Each companion joins a DISTINCT app instance's netns.
	netmodes := map[string]bool{}
	for _, c := range companions {
		netmodes[c.HostConfig.NetworkMode] = true
	}
	if len(netmodes) != 3 {
		t.Fatalf("companions must join distinct app netns, got %v", netmodes)
	}

	// Delete reaps all 3 apps + 3 companions.
	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.containers) != 0 {
		t.Fatalf("Delete left %d containers, want 0", len(f.containers))
	}
}
