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

// tlsSecret builds the caretaker TLS Secret fixture with the given keys present.
func tlsSecret(name string, keys ...string) *corev1.Secret {
	data := map[string][]byte{}
	for _, k := range keys {
		data[k] = []byte("pem")
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Data:       data,
	}
}

// applyHubAndGetPod applies a minimal hub spec and returns the resulting pod
// template spec.
func applyHubAndGetPod(t *testing.T, b *Backend, cs *fake.Clientset) corev1.PodSpec {
	t.Helper()
	ctx := context.Background()
	spec := api.DeploySpec{
		Name:  "web",
		Image: "img",
		Hub: &api.HubSpec{
			Export: []api.HubExport{{Name: "web", Port: 8080}},
			Import: []api.HubImport{{Name: "db", Ports: []int{5432}}},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	return dep.Spec.Template.Spec
}

// findCaretaker returns the (single) caretaker sidecar of a pod spec.
func findCaretaker(t *testing.T, pod corev1.PodSpec) *corev1.Container {
	t.Helper()
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name == "cornus-caretaker" {
			return &pod.InitContainers[i]
		}
	}
	t.Fatal("no cornus-caretaker init container")
	return nil
}

// assertTLSWiring checks the Secret volume, the read-only mount, and that the
// embedded config's TLS paths match want.
func assertTLSWiring(t *testing.T, pod corev1.PodSpec, secretName string, want caretaker.TLSFiles) {
	t.Helper()
	var vol *corev1.Volume
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == caretakerTLSVolume {
			vol = &pod.Volumes[i]
		}
	}
	if vol == nil || vol.Secret == nil || vol.Secret.SecretName != secretName {
		t.Fatalf("tls volume = %+v, want a secret volume for %s", vol, secretName)
	}
	ctr := findCaretaker(t, pod)
	var vm *corev1.VolumeMount
	for i := range ctr.VolumeMounts {
		if ctr.VolumeMounts[i].Name == caretakerTLSVolume {
			vm = &ctr.VolumeMounts[i]
		}
	}
	if vm == nil || !vm.ReadOnly || vm.MountPath != caretakerTLSMountPath {
		t.Fatalf("tls mount = %+v, want read-only at %s", vm, caretakerTLSMountPath)
	}
	cfg := decodeCaretakerConfig(t, ctr.Env)
	if cfg.TLS == nil {
		t.Fatal("config TLS = nil, want the mounted file paths")
	}
	if *cfg.TLS != want {
		t.Fatalf("config TLS = %+v, want %+v", *cfg.TLS, want)
	}
}

// TestCaretakerTLSSecretFull proves the mTLS shape: a Secret carrying all three
// conventional keys yields a read-only mount plus a config whose TLS block
// points at every mounted file.
func TestCaretakerTLSSecretFull(t *testing.T) {
	cs := fake.NewSimpleClientset(tlsSecret("care-tls", "ca.crt", "tls.crt", "tls.key"))
	b := NewWithClient(cs, "default")
	b.caretakerTLSSecret = "care-tls"

	pod := applyHubAndGetPod(t, b, cs)
	assertTLSWiring(t, pod, "care-tls", caretaker.TLSFiles{
		CAFile:   caretakerTLSMountPath + "/ca.crt",
		CertFile: caretakerTLSMountPath + "/tls.crt",
		KeyFile:  caretakerTLSMountPath + "/tls.key",
	})
}

// TestCaretakerTLSSecretCAOnly proves a CA-only Secret (server-auth TLS without
// mTLS) works: the client-pair paths are omitted from the config so the
// caretaker's fail-fast load never looks for the absent files.
func TestCaretakerTLSSecretCAOnly(t *testing.T) {
	cs := fake.NewSimpleClientset(tlsSecret("care-tls", "ca.crt"))
	b := NewWithClient(cs, "default")
	b.caretakerTLSSecret = "care-tls"

	pod := applyHubAndGetPod(t, b, cs)
	assertTLSWiring(t, pod, "care-tls", caretaker.TLSFiles{
		CAFile: caretakerTLSMountPath + "/ca.crt",
	})
}

// TestCaretakerTLSSecretUnreadable proves intended TLS never silently degrades:
// when the Secret cannot be inspected (missing here; no RBAC in production) the
// full conventional layout is assumed so a misconfiguration fails loudly in the
// sidecar instead of dialing without TLS.
func TestCaretakerTLSSecretUnreadable(t *testing.T) {
	cs := fake.NewSimpleClientset() // the secret does not exist
	b := NewWithClient(cs, "default")
	b.caretakerTLSSecret = "care-tls"

	pod := applyHubAndGetPod(t, b, cs)
	assertTLSWiring(t, pod, "care-tls", caretaker.TLSFiles{
		CAFile:   caretakerTLSMountPath + "/ca.crt",
		CertFile: caretakerTLSMountPath + "/tls.crt",
		KeyFile:  caretakerTLSMountPath + "/tls.key",
	})
}

// TestCaretakerTLSUnsetUnchanged is the regression guard for the hard
// requirement: with the knob unset the pod spec must be byte-identical to the
// pre-TLS shape — no volume, no mount, no tls key in the config JSON, and no
// trace of the TLS paths anywhere in the serialized spec.
func TestCaretakerTLSUnsetUnchanged(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default") // caretakerTLSSecret unset

	pod := applyHubAndGetPod(t, b, cs)
	ctr := findCaretaker(t, pod)
	if cfg := decodeCaretakerConfig(t, ctr.Env); cfg.TLS != nil {
		t.Fatalf("config TLS = %+v, want nil when the knob is unset", cfg.TLS)
	}
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" && strings.Contains(e.Value, `"tls"`) {
			t.Fatalf("config JSON carries a tls key with the knob unset: %s", e.Value)
		}
	}
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("marshal pod spec: %v", err)
	}
	for _, needle := range []string{caretakerTLSVolume, caretakerTLSMountPath} {
		if strings.Contains(string(raw), needle) {
			t.Fatalf("pod spec mentions %q with the knob unset:\n%s", needle, raw)
		}
	}
}

// TestCaretakerTLSWithMounts proves the mounts path (deploymentWithMounts — the
// single privileged caretaker) gets the same wiring, on top of its 9p volume
// mounts.
func TestCaretakerTLSWithMounts(t *testing.T) {
	cs := fake.NewSimpleClientset(tlsSecret("care-tls", "ca.crt"))
	b := NewWithClient(cs, "default")
	b.caretakerTLSSecret = "care-tls"
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:   "worker",
		Image:  "img",
		Mounts: []api.Mount{{Source: "/c/a", Target: "/a"}},
	}
	attach := []deploy.AttachMount{{Target: "/a", Session: "s", Name: "ma", RelayURL: "ws://relay"}}
	if _, err := b.ApplyWithMounts(ctx, spec, attach); err != nil {
		t.Fatalf("ApplyWithMounts: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec
	assertTLSWiring(t, pod, "care-tls", caretaker.TLSFiles{
		CAFile: caretakerTLSMountPath + "/ca.crt",
	})
	// The 9p mount wiring is untouched by the TLS mount.
	ctr := findCaretaker(t, pod)
	cfg := decodeCaretakerConfig(t, ctr.Env)
	if len(cfg.Mounts) != 1 || cfg.Mounts[0].Name != "ma" {
		t.Fatalf("caretaker mounts = %+v, want the one mount role ma", cfg.Mounts)
	}
}
