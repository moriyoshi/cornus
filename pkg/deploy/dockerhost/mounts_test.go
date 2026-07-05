package dockerhost

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/remotecompanion"
)

// findMount returns the mountSpec targeting dst, failing the test if none
// matches.
func findMount(t *testing.T, mounts []mountSpec, dst string) mountSpec {
	t.Helper()
	for _, m := range mounts {
		if m.Target == dst {
			return m
		}
	}
	t.Fatalf("no mount targeting %q among %+v", dst, mounts)
	return mountSpec{}
}

// newRemoteTestBackend is newTestBackend with WithRemote(true) — the mode
// ApplyWithMounts is actually invoked in (see deploy.RemoteCapable /
// useSidecarMounts), so tests exercising its always-on remote companion
// construct the backend this way rather than with the plain (co-located)
// newTestBackend.
func newRemoteTestBackend(t *testing.T, f *fakeDocker) *Backend {
	t.Helper()
	b := newTestBackend(t, f)
	b.remote = true
	return b
}

// TestApplyWithMounts proves the shared-propagation mechanism end to end
// against the fake Engine API: one Docker volume is created per (replica,
// mount) pair, its Mountpoint is inspected and bound into BOTH the app
// container (rslave) and a dedicated, Privileged caretaker companion
// (rshared) carrying the mount's caretaker.MountRole — and that the attach
// target never rides the app container as a plain host bind.
func TestApplyWithMounts(t *testing.T) {
	f := &fakeDocker{}
	b := newRemoteTestBackend(t, f)
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

	// Status reports exactly one instance — the app, not the mount caretaker.
	if len(st.Instances) != 1 {
		t.Fatalf("Status instances = %d, want 1 (mount caretaker must be filtered out)", len(st.Instances))
	}

	var appBody, careBody *createBody
	for i := range f.created {
		c := &f.created[i]
		if c.Labels[labelRole] == roleMountCaretaker {
			careBody = c
		} else if c.Labels[deploy.LabelApp] == "web" {
			appBody = c
		}
	}
	if appBody == nil || careBody == nil {
		t.Fatalf("want an app AND a mount-caretaker container; created=%d", len(f.created))
	}

	// The app container's mount is realized purely via the propagation bind —
	// never the spec's own (bogus) Source, and never a plain "volume" mount.
	if len(appBody.HostConfig.Binds) != 0 {
		t.Errorf("app Binds = %v, want none (the attach target must not ride a plain host bind)", appBody.HostConfig.Binds)
	}
	// Two mounts on the app side: the /data mount-relay bind, plus the
	// always-on agent-relay scratch bind every remote-mode instance now gets
	// regardless of --mount (see remotecompanion.AgentScratchDir).
	if len(appBody.HostConfig.Mounts) != 2 {
		t.Fatalf("app HostConfig.Mounts = %d, want 2 (mount-relay + agent-relay scratch)", len(appBody.HostConfig.Mounts))
	}
	appMount := findMount(t, appBody.HostConfig.Mounts, "/data")
	if appMount.Type != "bind" || appMount.Target != "/data" || !appMount.ReadOnly {
		t.Errorf("app mount = %+v", appMount)
	}
	if appMount.BindOptions == nil || appMount.BindOptions.Propagation != "rslave" {
		t.Errorf("app mount propagation = %+v, want rslave", appMount.BindOptions)
	}
	if !strings.HasPrefix(appMount.Source, "/var/lib/docker/volumes/") {
		t.Errorf("app mount source = %q, want the daemon-host volume Mountpoint", appMount.Source)
	}

	// The app container itself carries no elevated privilege for this.
	if appBody.HostConfig.Privileged {
		t.Error("app container must not be Privileged")
	}

	// The caretaker is Privileged (needed for its own kernel 9P mount), runs
	// `cornus caretaker`, shares the app container's network namespace (so its
	// PortForwardRole can reach the app's own ports), and mounts the SAME
	// volume at a scratch path with rshared propagation.
	if !careBody.HostConfig.Privileged {
		t.Error("mount caretaker must be Privileged")
	}
	wantNetMode := "container:" + appID(t, f, "web")
	if careBody.HostConfig.NetworkMode != wantNetMode {
		t.Errorf("mount caretaker NetworkMode = %q, want %q (shares the app's netns)", careBody.HostConfig.NetworkMode, wantNetMode)
	}
	if careBody.Image != "cornus:latest" || strings.Join(careBody.Cmd, " ") != "caretaker" {
		t.Errorf("caretaker image/cmd = %q/%v", careBody.Image, careBody.Cmd)
	}
	// Two mounts on the caretaker side too: its own /cornus/mounts/0 relay
	// bind, plus the always-on agent-relay scratch bind (a SEPARATE volume
	// from the mount-relay one, at remotecompanion.AgentScratchDir).
	if len(careBody.HostConfig.Mounts) != 2 {
		t.Fatalf("caretaker HostConfig.Mounts = %d, want 2 (mount-relay + agent-relay scratch)", len(careBody.HostConfig.Mounts))
	}
	careMount := findMount(t, careBody.HostConfig.Mounts, "/cornus/mounts/0")
	if careMount.BindOptions == nil || careMount.BindOptions.Propagation != "rshared" {
		t.Errorf("caretaker mount propagation = %+v, want rshared", careMount.BindOptions)
	}
	if careMount.Source != appMount.Source {
		t.Errorf("caretaker mount source %q != app mount source %q (must be the SAME volume)", careMount.Source, appMount.Source)
	}
	agentScratchMount := findMount(t, careBody.HostConfig.Mounts, remotecompanion.AgentScratchDir)
	if agentScratchMount.BindOptions == nil || agentScratchMount.BindOptions.Propagation != "rshared" {
		t.Errorf("caretaker agent-scratch mount propagation = %+v, want rshared", agentScratchMount.BindOptions)
	}
	if agentScratchMount.Source == careMount.Source {
		t.Error("agent-relay scratch volume must be SEPARATE from the mount-relay volume")
	}

	var cfg caretaker.Config
	for _, e := range careBody.Env {
		if v, ok := strings.CutPrefix(e, "CORNUS_CARETAKER_CONFIG="); ok {
			if err := json.Unmarshal([]byte(v), &cfg); err != nil {
				t.Fatalf("unmarshal caretaker config: %v", err)
			}
		}
	}
	if len(cfg.Mounts) != 1 {
		t.Fatalf("caretaker config mounts = %d, want 1", len(cfg.Mounts))
	}
	role := cfg.Mounts[0]
	if role.Server != mounts[0].RelayURL || role.Session != "sess-1" || role.Name != "m0" || !role.ReadOnly {
		t.Errorf("mount role = %+v", role)
	}
	if role.Target != careMount.Target {
		t.Errorf("mount role target %q != caretaker's own bind target %q", role.Target, careMount.Target)
	}
	if cfg.Instance != "web/0" {
		t.Errorf("caretaker config instance = %q, want %q", cfg.Instance, "web/0")
	}
	if cfg.PortForward == nil || cfg.PortForward.Server != mounts[0].RelayURL {
		t.Errorf("caretaker config PortForward = %+v, want a role targeting %q", cfg.PortForward, mounts[0].RelayURL)
	}
	if cfg.AgentRelay == nil || cfg.AgentRelay.Server != mounts[0].RelayURL || cfg.AgentRelay.SocketPath == "" {
		t.Errorf("caretaker config AgentRelay = %+v, want a role targeting %q with a socket path", cfg.AgentRelay, mounts[0].RelayURL)
	}

	// Delete reaps both the app and the mount caretaker.
	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.containers) != 0 {
		t.Fatalf("Delete left %d containers, want 0 (app + mount caretaker reaped)", len(f.containers))
	}
}

// TestApplyWithMountsReplicasGetDistinctVolumes proves each replica's mount
// gets its OWN Docker volume: sharing one volume's source path across
// replicas would let a mount event from one replica's caretaker propagate
// into a DIFFERENT replica's app container.
func TestApplyWithMountsReplicasGetDistinctVolumes(t *testing.T) {
	f := &fakeDocker{}
	b := newRemoteTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:     "web",
		Image:    "nginx:alpine",
		Replicas: 2,
		Mounts:   []api.Mount{{Target: "/data"}},
	}
	mounts := []deploy.AttachMount{{
		Target: "/data", Session: "sess-1", Name: "m0",
		RelayURL: "ws://x", AgentImage: "cornus:latest",
	}}

	st, err := b.ApplyWithMounts(ctx, spec, mounts)
	if err != nil {
		t.Fatalf("ApplyWithMounts: %v", err)
	}
	if len(st.Instances) != 2 {
		t.Fatalf("Status instances = %d, want 2", len(st.Instances))
	}

	var appSources []string
	var caretakerCount int
	for i := range f.created {
		c := &f.created[i]
		if c.Labels[labelRole] == roleMountCaretaker {
			caretakerCount++
			continue
		}
		if len(c.HostConfig.Mounts) != 2 {
			t.Fatalf("app HostConfig.Mounts = %d, want 2 (mount-relay + agent-relay scratch)", len(c.HostConfig.Mounts))
		}
		appSources = append(appSources, findMount(t, c.HostConfig.Mounts, "/data").Source)
	}
	if caretakerCount != 2 {
		t.Fatalf("want 2 mount caretakers (one per replica), got %d", caretakerCount)
	}
	if len(appSources) != 2 || appSources[0] == appSources[1] {
		t.Fatalf("replicas must get distinct volume sources, got %v", appSources)
	}
}

// TestApplyWithMountsNoAttachMountsDelegatesToApply confirms an empty
// AttachMount list is exactly Apply (no volumes, no caretaker).
func TestApplyWithMountsNoAttachMountsDelegatesToApply(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()

	if _, err := b.ApplyWithMounts(ctx, api.DeploySpec{Name: "web", Image: "nginx:alpine"}, nil); err != nil {
		t.Fatalf("ApplyWithMounts(nil): %v", err)
	}
	if len(f.created) != 1 || f.created[0].Labels[labelRole] == roleMountCaretaker {
		t.Fatalf("no AttachMounts should create exactly one plain app container, got %d created", len(f.created))
	}
}

// TestApplyWithMountsRequiresAgentImage proves a missing agent image is a hard
// error, matching ApplyWithEgress's precedent.
func TestApplyWithMountsRequiresAgentImage(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()

	mounts := []deploy.AttachMount{{Target: "/data", Session: "s", Name: "m0", RelayURL: "ws://x"}}
	if _, err := b.ApplyWithMounts(ctx, api.DeploySpec{Name: "web", Image: "nginx:alpine"}, mounts); err == nil {
		t.Error("client-local mounts without an agent image should be rejected")
	}
}

// appID returns the fake-assigned container id of name's app instance 0 (the
// fake mints "id-"+the create-call's own name, and instanceName(name, 0)
// deterministically names replica 0).
func appID(t *testing.T, f *fakeDocker, name string) string {
	t.Helper()
	return "id-" + instanceName(name, 0)
}
