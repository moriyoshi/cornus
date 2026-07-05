//go:build linux

package barehost

// Construction-time wiring and the refusals the optional-interface entry points
// make before anything is created. These are the answers the SERVER acts on, so
// a mis-wired option or a swallowed refusal shows up as a half-built deployment
// rather than a clear error.

import (
	"context"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/remotecompanion"
)

// TestOptionsReachTheBackend covers each Option's observable effect. WithPolicy
// is the one that matters most: dropping it would silently downgrade the backend
// to whatever the zero policy allows.
func TestOptionsReachTheBackend(t *testing.T) {
	reg := remotecompanion.NewRegistry()
	b := newBackend(Config{DataDir: t.TempDir(), Runtime: "runc"}, newFakeRuntime(), nil, false,
		WithPolicy(hostpolicy.Policy{AllowPrivileged: true}),
		WithAgentImage("ghcr.io/cornus/cornus:latest"),
		WithCompanionRegistry(reg),
		WithRemote(true),
	)
	if b.agentImage != "ghcr.io/cornus/cornus:latest" {
		t.Errorf("agentImage = %q, want the configured image", b.agentImage)
	}
	if b.companions != reg {
		t.Error("the companion registry was not stored")
	}
	if !b.remote {
		t.Error("WithRemote(true) did not take effect")
	}
	// The policy is enforced through Validate, so assert the behavior rather than
	// the field: a privileged spec must now pass where the default-deny zero
	// policy would reject it.
	if err := b.policy.Validate("bare", api.DeploySpec{Name: "web", Image: "x", Privileged: true}); err != nil {
		t.Errorf("configured policy rejected a privileged spec: %v", err)
	}
}

func TestRegistryCredentialsReachImageStoreOptions(t *testing.T) {
	creds := deploy.RegistryCredentials(func(context.Context, string) (deploy.RegistryCredential, bool, error) {
		return deploy.RegistryCredential{}, false, nil
	})
	o := resolveOptions(WithRegistryCredentials(creds))
	if o.creds == nil {
		t.Fatal("registry credential resolver was not stored")
	}
}

// TestDefaultPolicyIsDenyByDefault is the counterpart: without WithPolicy the
// backend must not accept host-privileged workloads.
func TestDefaultPolicyIsDenyByDefault(t *testing.T) {
	b, _ := newTestBackend(t)
	_, err := b.Apply(t.Context(), api.DeploySpec{Name: "web", Image: "nginx", Privileged: true})
	if err == nil {
		t.Fatal("a privileged spec under the zero policy: want a refusal")
	}
}

func TestDetectSystemdCgroupFollowsTheEnvironment(t *testing.T) {
	t.Setenv("CORNUS_BARE_SYSTEMD_CGROUP", "")
	if detectSystemdCgroup() {
		t.Error("the default cgroup driver must be cgroupfs (no systemd dependency)")
	}
	t.Setenv("CORNUS_BARE_SYSTEMD_CGROUP", "1")
	if !detectSystemdCgroup() {
		t.Error("CORNUS_BARE_SYSTEMD_CGROUP=1 must select the systemd driver")
	}
}

// TestEnvFalseTreatsUnsetAsEnabled pins the difference between the two env
// helpers: CORNUS_BARE_DNS is default-ON, so an unset value must not read as a
// disable.
func TestEnvFalseTreatsUnsetAsEnabled(t *testing.T) {
	for _, v := range []string{"0", "false", "FALSE", "no", "off"} {
		if !envFalse(v) {
			t.Errorf("envFalse(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "1", "true", "maybe"} {
		if envFalse(v) {
			t.Errorf("envFalse(%q) = true, want false (only an explicit disable counts)", v)
		}
	}
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		if !envTrue(v) {
			t.Errorf("envTrue(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "off", "TrUe"} {
		if envTrue(v) {
			t.Errorf("envTrue(%q) = true, want false", v)
		}
	}
}

// --- volumes ---

// TestSeedVolumesSkipsTheImageStoreWhenThereIsNothingToSeed pins the guard that
// lets a deployment with no volumes run without ever touching the image store —
// the fixture's store is nil, so a regression here is a nil dereference.
func TestSeedVolumesSkipsTheImageStoreWhenThereIsNothingToSeed(t *testing.T) {
	b, _ := newTestBackend(t)
	if b.img != nil {
		t.Fatal("this test needs the fixture's absent image store to be meaningful")
	}
	if err := b.seedVolumes(t.Context(), "sha256:whatever", nil); err != nil {
		t.Errorf("seedVolumes with no volumes = %v, want nil", err)
	}
}

// TestAnonymousVolumesAreNamespacedPerApp matters because anonymous volumes are
// reaped by deployment name: a shared directory would let one app's Delete wipe
// another's data.
func TestAnonymousVolumesAreNamespacedPerApp(t *testing.T) {
	b, _ := newTestBackend(t)
	web, err := b.anonVolumesDir("web")
	if err != nil {
		t.Fatal(err)
	}
	db, err := b.anonVolumesDir("db")
	if err != nil {
		t.Fatal(err)
	}
	if web == db {
		t.Fatalf("both apps share the anonymous volume dir %q", web)
	}
	if !strings.HasSuffix(web, "web") {
		t.Errorf("anonVolumesDir(web) = %q, want it namespaced by the app", web)
	}
}

// --- telemetry ---

// TestWithTelemetryIsInertWithoutTheSpecBlock keeps the common path free of
// companion machinery: a deploy that did not ask for telemetry must be handed
// back untouched (its Env map included).
func TestWithTelemetryIsInertWithoutTheSpecBlock(t *testing.T) {
	b, _ := newTestBackend(t)
	spec := api.DeploySpec{Name: "web", Image: "nginx", Env: map[string]string{"K": "V"}}

	got, hooks, err := b.withTelemetry(t.Context(), spec, applyHooks{})
	if err != nil {
		t.Fatalf("withTelemetry: %v", err)
	}
	if hooks.afterStart != nil {
		t.Error("no telemetry was requested, so no companion hook may be grafted on")
	}
	if len(got.Env) != 1 || got.Env["K"] != "V" {
		t.Errorf("env = %v, want the caller's map untouched", got.Env)
	}
}

// TestWithTelemetryNeedsTheAgentImage surfaces the missing server configuration
// up front instead of failing partway through an Apply.
func TestWithTelemetryNeedsTheAgentImage(t *testing.T) {
	b, _ := newTestBackend(t) // no WithAgentImage
	spec := api.DeploySpec{
		Name: "web", Image: "nginx",
		Telemetry: &api.TelemetrySpec{Enabled: true, Endpoint: "otel:4317"},
	}
	_, _, err := b.withTelemetry(t.Context(), spec, applyHooks{})
	if err == nil {
		t.Fatal("telemetry without an agent image: want a refusal")
	}
	if !strings.Contains(err.Error(), "CORNUS_AGENT_IMAGE") {
		t.Errorf("error = %v, want it to name the setting the operator must supply", err)
	}
}

// --- reboot recovery ---

// TestRepairNetnsIsANoOpForAnInstanceWithNoPin pins that an instance the backend
// never gave a netns is left alone rather than being minted one during recovery.
func TestRepairNetnsIsANoOpForAnInstanceWithNoPin(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.repairNetns(t.Context(), &instanceRecord{ID: "cornus-web-0"}); err != nil {
		t.Errorf("repairNetns for a record with no netns = %v, want nil", err)
	}
}

// TestRecoverInstanceRejectsAnUnusableChainID covers the rebuild's first step:
// without a parseable snapshot chain there is no rootfs to re-mount, and the
// reconcile must skip the instance loudly rather than launch it against nothing.
func TestRecoverInstanceRejectsAnUnusableChainID(t *testing.T) {
	b, rt := newTestBackend(t)
	img, err := newImageStore(t.TempDir(), "native", nil)
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}
	b.img = img
	rec := seedInstance(t, b, rt, "web", 0, false)
	rec.SnapshotKey = "cornus-cornus-web-0"
	rec.RootfsDir = t.TempDir()
	rec.ChainID = "not-a-digest"

	err = b.recoverInstance(t.Context(), rec)
	if err == nil {
		t.Fatal("recoverInstance with an unparseable chainID: want error")
	}
	if !strings.Contains(err.Error(), "chainID") {
		t.Errorf("error = %v, want it to name the bad chainID", err)
	}
}

// --- the optional-interface entry points ---

// TestApplyWithMountsNeedsTheAgentImage covers the sidecar mount path's
// precondition. It is checked while planning, before any rootfs or netns is
// created, so a misconfigured server fails cleanly.
func TestApplyWithMountsNeedsTheAgentImage(t *testing.T) {
	b, _ := newTestBackend(t)
	spec := api.DeploySpec{Name: "web", Image: "nginx"}
	mounts := []deploy.AttachMount{{Target: "/data", Session: "s", Name: "m0", RelayURL: "ws://x"}} // no AgentImage

	_, err := b.ApplyWithMounts(t.Context(), spec, mounts)
	if err == nil {
		t.Fatal("client-local mounts without an agent image: want a refusal")
	}
	if !strings.Contains(err.Error(), "CORNUS_AGENT_IMAGE") {
		t.Errorf("error = %v, want it to name the setting the operator must supply", err)
	}
}

// TestApplyWithEgressRejectsAnUnsupportedMode pins the mode vocabulary at the
// API boundary rather than letting an unknown mode reach a companion that would
// silently proxy nothing.
func TestApplyWithEgressRejectsAnUnsupportedMode(t *testing.T) {
	b, _ := newTestBackend(t)
	spec := api.DeploySpec{Name: "web", Image: "nginx"}
	egress := &deploy.AttachEgress{
		Session: "s", RelayURL: "ws://x", AgentImage: "cornus:latest",
		Spec: &api.EgressSpec{Mode: "tunnel"},
	}

	_, err := b.ApplyWithEgress(t.Context(), spec, egress)
	if err == nil {
		t.Fatal("an unknown egress mode: want a refusal")
	}
	if !strings.Contains(err.Error(), "tunnel") {
		t.Errorf("error = %v, want it to name the rejected mode", err)
	}
}

// TestApplyWithEgressNeedsTheAgentImage: egress is never realizable without a
// companion, so a missing agent image is fatal rather than a silent downgrade to
// unproxied traffic.
func TestApplyWithEgressNeedsTheAgentImage(t *testing.T) {
	b, _ := newTestBackend(t)
	egress := &deploy.AttachEgress{Session: "s", RelayURL: "ws://x", Spec: &api.EgressSpec{Mode: "proxy"}}

	_, err := b.ApplyWithEgress(t.Context(), api.DeploySpec{Name: "web", Image: "nginx"}, egress)
	if err == nil {
		t.Fatal("client-side egress without an agent image: want a refusal")
	}
	if !strings.Contains(err.Error(), "CORNUS_AGENT_IMAGE") {
		t.Errorf("error = %v, want it to name the setting the operator must supply", err)
	}
}
