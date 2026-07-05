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

// caretakerCfg decodes the CORNUS_CARETAKER_CONFIG env var a companion was
// created with.
func caretakerCfg(t *testing.T, body *createBody) caretaker.Config {
	t.Helper()
	for _, e := range body.Env {
		if v, ok := strings.CutPrefix(e, "CORNUS_CARETAKER_CONFIG="); ok {
			var cfg caretaker.Config
			if err := json.Unmarshal([]byte(v), &cfg); err != nil {
				t.Fatalf("unmarshal caretaker config: %v", err)
			}
			return cfg
		}
	}
	t.Fatalf("no CORNUS_CARETAKER_CONFIG in env %v", body.Env)
	return caretaker.Config{}
}

// companionByRole returns the created container carrying labelRole == role.
func companionByRole(t *testing.T, f *fakeDocker, role string) *createBody {
	t.Helper()
	for i := range f.created {
		if f.created[i].Labels[labelRole] == role {
			return &f.created[i]
		}
	}
	t.Fatalf("no companion with role %q among %d created", role, len(f.created))
	return nil
}

// countCompanions returns how many of the created containers are companions.
func countCompanions(f *fakeDocker) int {
	n := 0
	for i := range f.created {
		if f.created[i].Labels[labelRole] != "" {
			n++
		}
	}
	return n
}

// TestRemoteAndEgressShareOneCompanion proves remote mode plus transparent
// egress produces exactly ONE companion carrying every role, not the two that
// used to share a netns.
//
// Two mattered because the redirect is programmed with
// netredirect.Setup(port, 0, egressMark) — a nat OUTPUT chain scoped to the
// NETWORK NAMESPACE, so it captured the sibling companion's traffic too,
// exempting only loopback and that one mark. An unmarked remote companion had
// its CORNUS_ADVERTISE_URL dial redirected into the egress proxy and routed by
// the egress policy, silently breaking port-forward / tunnel /
// exec-agent-forwarding on the next redial. (Only the next: NAT is consulted on
// a flow's first packet, so the connection opened before the egress companion
// existed survived — which is what made it intermittent rather than obvious.)
// With one process there is no sibling to capture, and the process-wide Mark
// covers every role's dials at once.
func TestRemoteAndEgressShareOneCompanion(t *testing.T) {
	f := &fakeDocker{}
	b := newRemoteTestBackend(t, f)
	b.agentImage = "cornus:latest"
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus.host:5000")
	ctx := context.Background()

	spec := api.DeploySpec{Name: "web", Image: "nginx:alpine", Egress: &api.EgressSpec{Mode: "transparent"}}
	egress := &deploy.AttachEgress{Session: "s", RelayURL: "ws://x", AgentImage: "cornus:latest", Spec: spec.Egress}
	if _, err := b.ApplyWithEgress(ctx, spec, egress); err != nil {
		t.Fatalf("ApplyWithEgress(transparent, remote): %v", err)
	}

	if n := countCompanions(f); n != 1 {
		t.Fatalf("companions = %d, want 1 (every role folds into one caretaker)", n)
	}
	// In remote mode the survivor keeps the mount caretaker's identity: it exists
	// for every instance regardless of the plan, so tooling filtering on that
	// label keeps working.
	comp := companionByRole(t, f, roleMountCaretaker)
	cfg := caretakerCfg(t, comp)

	if cfg.Egress == nil || cfg.Egress.Mode != "transparent" || !cfg.Egress.SetupRedirect {
		t.Errorf("companion egress role = %+v, want transparent + SetupRedirect", cfg.Egress)
	}
	if cfg.Mark != egressMark {
		t.Errorf("companion Mark = %d, want %d (it programs the redirect it must escape)", cfg.Mark, egressMark)
	}
	if cfg.PortForward == nil || cfg.AgentRelay == nil {
		t.Errorf("companion lost its remote-mode roles: PortForward=%+v AgentRelay=%+v", cfg.PortForward, cfg.AgentRelay)
	}
	if len(comp.HostConfig.CapAdd) != 1 || comp.HostConfig.CapAdd[0] != "NET_ADMIN" {
		t.Errorf("companion CapAdd = %v, want [NET_ADMIN] (it programs nftables)", comp.HostConfig.CapAdd)
	}
}

// TestApplyWithAttachmentsMountsAndEgress covers the combination the server used
// to reject outright on this backend: a deploy declaring BOTH client-local
// mounts and client-side egress. The dispatch routes anything with egress
// through an AttachingBackend and only falls back to EgressBackend for an
// egress-ONLY deploy, so before dockerhost implemented AttachingBackend this
// pairing failed with "client-side egress is not yet supported by the dockerhost
// backend" even though each feature worked alone.
func TestApplyWithAttachmentsMountsAndEgress(t *testing.T) {
	f := &fakeDocker{}
	b := newRemoteTestBackend(t, f)
	b.agentImage = "cornus:latest"
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus.host:5000")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:   "web",
		Image:  "nginx:alpine",
		Mounts: []api.Mount{{Source: "/never/used", Target: "/data"}},
		Egress: &api.EgressSpec{Mode: "proxy"},
	}
	mounts := []deploy.AttachMount{{
		Target: "/data", Session: "sess-1", Name: "m0",
		RelayURL: "ws://cornus.host:5000", AgentImage: "cornus:latest",
	}}
	egress := &deploy.AttachEgress{Session: "sess-1", RelayURL: "ws://cornus.host:5000", AgentImage: "cornus:latest", Spec: spec.Egress}

	if _, err := b.ApplyWithAttachments(ctx, spec, mounts, nil, egress); err != nil {
		t.Fatalf("ApplyWithAttachments(mounts+egress): %v", err)
	}
	if n := countCompanions(f); n != 1 {
		t.Fatalf("companions = %d, want 1", n)
	}
	comp := companionByRole(t, f, roleMountCaretaker)
	cfg := caretakerCfg(t, comp)
	if len(cfg.Mounts) != 1 || cfg.Mounts[0].Name != "m0" {
		t.Errorf("companion mount roles = %+v, want the m0 relay", cfg.Mounts)
	}
	if cfg.Egress == nil || cfg.Egress.Mode != "proxy" {
		t.Errorf("companion egress role = %+v, want proxy mode", cfg.Egress)
	}
	// Proxy mode is authoritative for the app's proxy env, and the mount's
	// propagation bind must still reach the app container.
	var app *createBody
	for i := range f.created {
		if f.created[i].Labels[labelRole] == "" {
			app = &f.created[i]
		}
	}
	if app == nil {
		t.Fatal("no app container created")
	}
	foundProxy := false
	for _, e := range app.Env {
		if strings.HasPrefix(e, "HTTP_PROXY=") {
			foundProxy = true
		}
	}
	if !foundProxy {
		t.Errorf("app env = %v, want the proxy-mode HTTP_PROXY injected", app.Env)
	}
	findMount(t, app.HostConfig.Mounts, "/data")
}

// TestApplyWithAttachmentsRejectsCredentials pins the one attachment this
// backend still cannot realize, so it fails at the API with a clear message
// rather than silently dropping the credential.
func TestApplyWithAttachmentsRejectsCredentials(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()

	creds := []deploy.AttachCredential{{Name: "aws", Session: "s", RelayURL: "ws://x"}}
	_, err := b.ApplyWithAttachments(ctx, api.DeploySpec{Name: "web", Image: "nginx:alpine"}, nil, creds, nil)
	if err == nil {
		t.Fatal("client-sourced credentials should be rejected on dockerhost")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("error = %v, want it to name credentials", err)
	}
}

// TestRemoteCompanionUnmarkedWithoutTransparentEgress is the negative half: no
// redirect is programmed in proxy mode (the app is pointed at the proxy by env
// instead), so the companion must not stamp a mark it has no reason to.
func TestRemoteCompanionUnmarkedWithoutTransparentEgress(t *testing.T) {
	for _, tc := range []struct {
		name   string
		egress *api.EgressSpec
	}{
		{"proxy mode", &api.EgressSpec{Mode: "proxy"}},
		{"no egress at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDocker{}
			b := newRemoteTestBackend(t, f)
			b.agentImage = "cornus:latest"
			t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus.host:5000")
			ctx := context.Background()

			spec := api.DeploySpec{Name: "web", Image: "nginx:alpine", Egress: tc.egress}
			var err error
			if tc.egress != nil {
				egress := &deploy.AttachEgress{Session: "s", RelayURL: "ws://x", AgentImage: "cornus:latest", Spec: tc.egress}
				_, err = b.ApplyWithEgress(ctx, spec, egress)
			} else {
				_, err = b.Apply(ctx, spec)
			}
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := caretakerCfg(t, companionByRole(t, f, roleMountCaretaker)).Mark; got != 0 {
				t.Errorf("remote companion Mark = %d, want 0 (no redirect is programmed here)", got)
			}
		})
	}
}
