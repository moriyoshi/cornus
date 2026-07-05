package kubernetes

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/remotecompanion"
)

// caretakerContainer finds the (sole expected) "cornus-caretaker" init
// container in pod, failing the test if there isn't exactly one.
func caretakerContainer(t *testing.T, pod corev1.PodSpec) *corev1.Container {
	t.Helper()
	var ctr *corev1.Container
	n := 0
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name == "cornus-caretaker" {
			ctr = &pod.InitContainers[i]
			n++
		}
	}
	if n != 1 {
		t.Fatalf("caretaker containers = %d, want exactly 1", n)
	}
	return ctr
}

// TestAgentForwardAloneGetsMinimalCaretaker proves a spec with AgentForward
// set but none of Hub/DNS/Docker gets its own minimal (unprivileged, no
// startup-gating-on-mounts) caretaker carrying just the AgentRelay role, with
// Instance set so the server can register the connection, and a shared
// agent-socket volume mounted into both the app and caretaker containers.
func TestAgentForwardAloneGetsMinimalCaretaker(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{Name: "web", Image: "img", AgentForward: true}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec
	ctr := caretakerContainer(t, pod)

	var cfg caretaker.Config
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			if err := json.Unmarshal([]byte(e.Value), &cfg); err != nil {
				t.Fatalf("unmarshal caretaker config: %v", err)
			}
		}
	}
	if cfg.Instance != "web/0" {
		t.Errorf("cfg.Instance = %q, want web/0", cfg.Instance)
	}
	if cfg.AgentRelay == nil || cfg.AgentRelay.Server != "ws://cornus:5000" || cfg.AgentRelay.SocketPath != remotecompanion.AgentSocketPath {
		t.Errorf("cfg.AgentRelay = %+v, want a role targeting the advertise URL at %s", cfg.AgentRelay, remotecompanion.AgentSocketPath)
	}

	// Shared agent-socket volume mounted into both the app container and the
	// caretaker at the fixed path.
	appMount := findVolumeMount(t, pod.Containers[0].VolumeMounts, remotecompanion.AgentScratchDir)
	careMount := findVolumeMount(t, ctr.VolumeMounts, remotecompanion.AgentScratchDir)
	if appMount.Name != careMount.Name {
		t.Errorf("app mount volume %q != caretaker mount volume %q, want the same shared emptyDir", appMount.Name, careMount.Name)
	}

	// The Deployment carries the agent-forward annotation so AgentForwardEnabled
	// can answer without decoding the caretaker's own config JSON.
	if dep.Annotations[agentForwardAnnotation] != "true" {
		t.Errorf("deployment annotations = %+v, want %s=true", dep.Annotations, agentForwardAnnotation)
	}
}

// TestNoAgentForwardNoCaretaker confirms a plain spec (no Hub/DNS/Docker/
// AgentForward) still gets no caretaker sidecar at all — AgentForward must not
// change behavior when unset.
func TestNoAgentForwardNoCaretaker(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{Name: "web", Image: "img"}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	for _, c := range dep.Spec.Template.Spec.InitContainers {
		if c.Name == "cornus-caretaker" {
			t.Fatalf("plain spec got a caretaker sidecar, want none")
		}
	}
	if dep.Annotations[agentForwardAnnotation] == "true" {
		t.Error("plain spec must not carry the agent-forward annotation")
	}
}

// TestAgentForwardFoldsIntoHubCaretaker proves AgentForward alongside a Hub
// role folds into the SAME caretaker (not a second sidecar), carrying both
// roles.
func TestAgentForwardFoldsIntoHubCaretaker(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:         "web",
		Image:        "img",
		AgentForward: true,
		Hub: &api.HubSpec{
			Export: []api.HubExport{{Name: "web", Port: 8080}},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec
	ctr := caretakerContainer(t, pod)

	var cfg caretaker.Config
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			_ = json.Unmarshal([]byte(e.Value), &cfg)
		}
	}
	if cfg.Hub == nil {
		t.Error("cfg.Hub = nil, want the hub role still present")
	}
	if cfg.AgentRelay == nil {
		t.Error("cfg.AgentRelay = nil, want it folded into the same (hub) caretaker")
	}
}

// findVolumeMount returns the VolumeMount targeting path, failing the test if
// none matches.
func findVolumeMount(t *testing.T, mounts []corev1.VolumeMount, path string) corev1.VolumeMount {
	t.Helper()
	for _, m := range mounts {
		if m.MountPath == path {
			return m
		}
	}
	t.Fatalf("no volume mount targeting %q among %+v", path, mounts)
	return corev1.VolumeMount{}
}

// TestAgentForwardEnabled proves the annotation-backed AgentForwardCapable
// implementation: it reflects the applied spec's AgentForward setting and
// reports deploy.ErrNotFound for an unknown deployment.
func TestAgentForwardEnabled(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "off", Image: "img"}); err != nil {
		t.Fatalf("Apply(off): %v", err)
	}
	if _, err := b.Apply(ctx, api.DeploySpec{Name: "on", Image: "img", AgentForward: true}); err != nil {
		t.Fatalf("Apply(on): %v", err)
	}

	if got, err := b.AgentForwardEnabled(ctx, "off"); err != nil || got {
		t.Errorf("AgentForwardEnabled(off) = %v, %v; want false, nil", got, err)
	}
	if got, err := b.AgentForwardEnabled(ctx, "on"); err != nil || !got {
		t.Errorf("AgentForwardEnabled(on) = %v, %v; want true, nil", got, err)
	}
	if _, err := b.AgentForwardEnabled(ctx, "missing"); err == nil {
		t.Error("AgentForwardEnabled(missing) = nil error, want deploy.ErrNotFound")
	}
}
