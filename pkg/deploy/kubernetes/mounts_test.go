package kubernetes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
)

// TestApplyWithMountsInjectsSidecar verifies that a client-local mount becomes a
// privileged native-sidecar CARETAKER + shared emptyDir with the right
// propagation, that the app container mounts it read-only via HostToContainer,
// and that the attach target is NOT turned into a hostPath volume.
func TestApplyWithMountsInjectsSidecar(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:   "web",
		Image:  "localhost:5000/web:v1",
		Mounts: []api.Mount{{Source: "/client/conf", Target: "/etc/app", ReadOnly: true}},
	}
	attach := []deploy.AttachMount{{
		Target:     "/etc/app",
		ReadOnly:   true,
		Session:    "sess123",
		Name:       "m0",
		RelayURL:   "ws://cornus.default.svc:5000",
		AgentImage: "cornus:latest",
	}}

	if _, err := b.ApplyWithMounts(ctx, spec, attach); err != nil {
		t.Fatalf("ApplyWithMounts: %v", err)
	}

	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec

	// No hostPath volume for the attach target.
	for _, v := range pod.Volumes {
		if v.HostPath != nil {
			t.Fatalf("unexpected hostPath volume %q for an attach mount", v.Name)
		}
	}
	// A shared emptyDir propagation medium exists.
	var vol *corev1.Volume
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == "cornus-mount-0" {
			vol = &pod.Volumes[i]
		}
	}
	if vol == nil || vol.EmptyDir == nil {
		t.Fatalf("expected emptyDir volume cornus-mount-0, volumes=%+v", pod.Volumes)
	}

	// Native sidecar init container, privileged, Bidirectional, with a startupProbe.
	if len(pod.InitContainers) != 1 {
		t.Fatalf("init containers = %d, want 1", len(pod.InitContainers))
	}
	sc := pod.InitContainers[0]
	if sc.Name != "cornus-caretaker" {
		t.Errorf("sidecar name = %q, want cornus-caretaker", sc.Name)
	}
	if sc.RestartPolicy == nil || *sc.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Errorf("sidecar RestartPolicy = %v, want Always (native sidecar)", sc.RestartPolicy)
	}
	if sc.SecurityContext == nil || sc.SecurityContext.Privileged == nil || !*sc.SecurityContext.Privileged {
		t.Errorf("sidecar not privileged")
	}
	if sc.Image != "cornus:latest" {
		t.Errorf("sidecar image = %q", sc.Image)
	}
	if len(sc.VolumeMounts) != 1 || sc.VolumeMounts[0].MountPropagation == nil ||
		*sc.VolumeMounts[0].MountPropagation != corev1.MountPropagationBidirectional {
		t.Errorf("sidecar mount propagation = %+v, want Bidirectional", sc.VolumeMounts)
	}
	// Command must pin the `cornus` entrypoint explicitly: the sidecar image is a
	// cornus image (the server's own discovered image, or here — with no self Pod
	// or override — the app-image fallback), whose ENTRYPOINT is not necessarily
	// `cornus`, so Args alone ([caretaker]) would run that image's own entrypoint
	// with a stray `caretaker` argument and never mount.
	if strings.Join(sc.Command, " ") != "cornus" {
		t.Errorf("sidecar command = %v, want [cornus]", sc.Command)
	}
	if sc.Args == nil || sc.Args[0] != "caretaker" {
		t.Errorf("sidecar args = %v, want [caretaker]", sc.Args)
	}
	// The startup probe gates the app until all roles are live.
	if sc.StartupProbe == nil || sc.StartupProbe.Exec == nil ||
		strings.Join(sc.StartupProbe.Exec.Command, " ") != "cornus caretaker-check" {
		t.Errorf("sidecar startup probe = %+v, want `cornus caretaker-check`", sc.StartupProbe)
	}
	// The relay coordinates ride the config env var (parseable back to a MountRole).
	var cfgEnv string
	for _, e := range sc.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			cfgEnv = e.Value
		}
	}
	var cfg caretaker.Config
	if err := json.Unmarshal([]byte(cfgEnv), &cfg); err != nil {
		t.Fatalf("caretaker config env not valid JSON (%q): %v", cfgEnv, err)
	}
	if len(cfg.Mounts) != 1 {
		t.Fatalf("caretaker config mounts = %d, want 1", len(cfg.Mounts))
	}
	r := cfg.Mounts[0]
	if r.Server != "ws://cornus.default.svc:5000" || r.Session != "sess123" || r.Name != "m0" || !r.ReadOnly {
		t.Errorf("caretaker mount role = %+v, want the relay coords + read-only", r)
	}
	if r.Target != sc.VolumeMounts[0].MountPath {
		t.Errorf("mount role target %q != the sidecar's scratch mountPath %q", r.Target, sc.VolumeMounts[0].MountPath)
	}

	// App container mounts the shared volume read-only via HostToContainer.
	app := pod.Containers[0]
	var found bool
	for _, vm := range app.VolumeMounts {
		if vm.Name == "cornus-mount-0" {
			found = true
			if vm.MountPath != "/etc/app" || !vm.ReadOnly {
				t.Errorf("app mount = %+v, want /etc/app ro", vm)
			}
			if vm.MountPropagation == nil || *vm.MountPropagation != corev1.MountPropagationHostToContainer {
				t.Errorf("app mount propagation = %v, want HostToContainer", vm.MountPropagation)
			}
		}
	}
	if !found {
		t.Fatalf("app container missing the shared mount: %+v", app.VolumeMounts)
	}
}

// TestApplyWithMountsSingleCaretaker confirms the reframe's core property: N
// client-local mounts produce exactly ONE caretaker sidecar (not N), carrying
// all N mount roles, each with its own emptyDir + app volumeMount.
func TestApplyWithMountsSingleCaretaker(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "web",
		Image: "img",
		Mounts: []api.Mount{
			{Source: "/c/a", Target: "/a"},
			{Source: "/c/b", Target: "/b", ReadOnly: true},
		},
	}
	attach := []deploy.AttachMount{
		{Target: "/a", Session: "s", Name: "ma", RelayURL: "ws://relay"},
		{Target: "/b", ReadOnly: true, Session: "s", Name: "mb", RelayURL: "ws://relay"},
	}
	if _, err := b.ApplyWithMounts(ctx, spec, attach); err != nil {
		t.Fatalf("ApplyWithMounts: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec

	// Exactly ONE sidecar, mounting BOTH scratch volumes, carrying BOTH roles.
	if len(pod.InitContainers) != 1 {
		t.Fatalf("init containers = %d, want exactly 1 caretaker for 2 mounts", len(pod.InitContainers))
	}
	sc := pod.InitContainers[0]
	if len(sc.VolumeMounts) != 2 {
		t.Errorf("caretaker volumeMounts = %d, want 2 (one scratch per mount)", len(sc.VolumeMounts))
	}
	var cfg caretaker.Config
	for _, e := range sc.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			_ = json.Unmarshal([]byte(e.Value), &cfg)
		}
	}
	if len(cfg.Mounts) != 2 {
		t.Fatalf("caretaker config mounts = %d, want 2", len(cfg.Mounts))
	}
	names := map[string]bool{cfg.Mounts[0].Name: true, cfg.Mounts[1].Name: true}
	if !names["ma"] || !names["mb"] {
		t.Errorf("caretaker roles = %+v, want both ma and mb", cfg.Mounts)
	}
	// Both targets are distinct scratch paths and both app mounts exist.
	if cfg.Mounts[0].Target == cfg.Mounts[1].Target {
		t.Errorf("mount roles share a scratch path: %+v", cfg.Mounts)
	}
	appTargets := map[string]bool{}
	for _, vm := range pod.Containers[0].VolumeMounts {
		appTargets[vm.MountPath] = true
	}
	if !appTargets["/a"] || !appTargets["/b"] {
		t.Errorf("app container missing a mount target: %+v", pod.Containers[0].VolumeMounts)
	}
}

// TestProxyWithMountsSingleCaretaker verifies the proxy+mounts combination: the
// spec carries BOTH a client-local mount and an enforcing proxy, and produces
// ONE privileged caretaker running both roles, exempted from the egress redirect
// by a firewall MARK (not a uid — it must run as root for mounts). A
// mark-exempting net-redirect init is present; there is no second caretaker.
func TestProxyWithMountsSingleCaretaker(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:   "proj-web",
		Image:  "img",
		Mounts: []api.Mount{{Source: "/c/a", Target: "/a"}},
		Proxy:  &api.ProxySpec{Allow: []string{"api", "db"}},
	}
	attach := []deploy.AttachMount{{Target: "/a", Session: "s", Name: "ma", RelayURL: "ws://relay"}}
	if _, err := b.ApplyWithMounts(ctx, spec, attach); err != nil {
		t.Fatalf("ApplyWithMounts: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec

	var redirect, ctr *corev1.Container
	var caretakers int
	for i := range pod.InitContainers {
		switch pod.InitContainers[i].Name {
		case "cornus-net-redirect":
			redirect = &pod.InitContainers[i]
		case "cornus-caretaker":
			ctr = &pod.InitContainers[i]
			caretakers++
		}
	}
	if caretakers != 1 {
		t.Fatalf("caretakers = %d, want exactly 1 for proxy+mounts", caretakers)
	}
	if redirect == nil || ctr == nil {
		t.Fatalf("want both a net-redirect init and one caretaker; got %+v", pod.InitContainers)
	}
	// The redirect exempts by MARK, not uid (the caretaker runs as root here).
	args := strings.Join(redirect.Args, " ")
	if !strings.Contains(args, "--exempt-mark") || strings.Contains(args, "--exempt-uid") {
		t.Errorf("net-redirect args = %q, want --exempt-mark and NOT --exempt-uid", args)
	}
	// The one caretaker is privileged (for mounts) and carries BOTH roles + the mark.
	if ctr.SecurityContext == nil || ctr.SecurityContext.Privileged == nil || !*ctr.SecurityContext.Privileged {
		t.Errorf("caretaker must be privileged for mounts, got %+v", ctr.SecurityContext)
	}
	var cfg caretaker.Config
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			_ = json.Unmarshal([]byte(e.Value), &cfg)
		}
	}
	if len(cfg.Mounts) != 1 || cfg.Mounts[0].Name != "ma" {
		t.Errorf("caretaker mounts = %+v, want the one mount role ma", cfg.Mounts)
	}
	if cfg.Proxy == nil || strings.Join(cfg.Proxy.Allow, ",") != "api,db" {
		t.Errorf("caretaker proxy = %+v, want allow [api db]", cfg.Proxy)
	}
	if cfg.Mark == 0 {
		t.Error("caretaker config must carry a non-zero SO_MARK for proxy+mounts")
	}
}
