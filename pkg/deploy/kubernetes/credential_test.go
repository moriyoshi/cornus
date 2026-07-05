package kubernetes

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"

	// Register the delivery providers the backend resolves at apply time.
	_ "cornus/pkg/creddelivery/awsimds"
	_ "cornus/pkg/creddelivery/generic"
)

func applyCreds(t *testing.T, spec api.DeploySpec, creds []deploy.AttachCredential) corev1.PodSpec {
	t.Helper()
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	if _, err := b.ApplyWithAttachments(context.Background(), spec, nil, creds, nil); err != nil {
		t.Fatalf("ApplyWithAttachments: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(context.Background(), spec.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	return dep.Spec.Template.Spec
}

func caretakerCtr(t *testing.T, pod corev1.PodSpec) corev1.Container {
	t.Helper()
	for _, c := range pod.InitContainers {
		if c.Name == "cornus-caretaker" {
			return c
		}
	}
	t.Fatal("cornus-caretaker sidecar not injected")
	return corev1.Container{}
}

func appEnv(pod corev1.PodSpec) map[string]string {
	m := map[string]string{}
	for _, e := range pod.Containers[0].Env {
		m[e.Name] = e.Value
	}
	return m
}

// TestCredentialGenericEndpoint: a generic-endpoint credential injects a caretaker
// credential role, a loopback endpoint delivery, the app CORNUS_CREDENTIALS_URL
// env, and — with no mounts and no well-known bind — needs no elevated security
// context.
func TestCredentialGenericEndpoint(t *testing.T) {
	spec := api.DeploySpec{Name: "web", Image: "img:v1"}
	creds := []deploy.AttachCredential{{
		Name:     "db",
		Session:  "sess1",
		RelayURL: "ws://cornus.default.svc:5000",
		Deliver:  []api.CredentialDelivery{{Kind: "endpoint", Provider: "generic"}},
	}}
	pod := applyCreds(t, spec, creds)

	ctr := caretakerCtr(t, pod)
	cfg := decodeCaretakerConfig(t, ctr.Env)
	if len(cfg.Credentials) != 1 || cfg.Credentials[0].Name != "db" {
		t.Fatalf("credential roles = %+v", cfg.Credentials)
	}
	d := cfg.Credentials[0].Deliver
	if len(d) != 1 || d[0].Kind != "endpoint" || d[0].Addr == "" {
		t.Fatalf("delivery = %+v", d)
	}
	if env := appEnv(pod); env["CORNUS_CREDENTIALS_URL"] == "" {
		t.Fatalf("app env missing CORNUS_CREDENTIALS_URL: %v", env)
	}
	// No mounts, no well-known -> no privileged/NET_ADMIN.
	if ctr.SecurityContext != nil && ctr.SecurityContext.Privileged != nil && *ctr.SecurityContext.Privileged {
		t.Error("credential-only caretaker should not be privileged")
	}
}

// TestCredentialWellKnownGrantsNetAdmin: an aws-imds well-known delivery binds
// 169.254.169.254 and needs NET_ADMIN.
func TestCredentialWellKnownGrantsNetAdmin(t *testing.T) {
	spec := api.DeploySpec{Name: "web", Image: "img:v1"}
	creds := []deploy.AttachCredential{{
		Name:     "aws",
		Session:  "s",
		RelayURL: "ws://r",
		Deliver:  []api.CredentialDelivery{{Kind: "endpoint", Provider: "aws-imds", WellKnown: true}},
	}}
	pod := applyCreds(t, spec, creds)
	ctr := caretakerCtr(t, pod)

	cfg := decodeCaretakerConfig(t, ctr.Env)
	d := cfg.Credentials[0].Deliver[0]
	if !d.WellKnown || d.Addr != "169.254.169.254:80" {
		t.Fatalf("well-known delivery = %+v", d)
	}
	found := false
	if ctr.SecurityContext != nil && ctr.SecurityContext.Capabilities != nil {
		for _, c := range ctr.SecurityContext.Capabilities.Add {
			if c == "NET_ADMIN" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected NET_ADMIN capability, security context = %+v", ctr.SecurityContext)
	}
}

// TestCredentialsAreServerBound: a credentials-only caretaker dials the server
// relay, so it must receive the scoped token like the mount/hub roles.
func TestCredentialsAreServerBound(t *testing.T) {
	b := &Backend{caretakerToken: "sekret"}
	cfg := caretaker.Config{Credentials: []caretaker.CredentialRole{{Server: "ws://r", Name: "db"}}}
	if got := decodeCaretakerConfig(t, b.caretakerConfigEnv(cfg, "web")); got.Token != "sekret" {
		t.Fatalf("credentials token = %q, want it stamped (server-bound)", got.Token)
	}
}

// TestCredentialFileDelivery: a file delivery gets a shared emptyDir mounted in
// both the app (at the file's directory) and the caretaker (at a scratch path),
// and the caretaker role's path points into the scratch dir.
func TestCredentialFileDelivery(t *testing.T) {
	spec := api.DeploySpec{Name: "web", Image: "img:v1"}
	creds := []deploy.AttachCredential{{
		Name:     "db",
		Session:  "s",
		RelayURL: "ws://r",
		Deliver:  []api.CredentialDelivery{{Kind: "file", Path: "/var/run/secrets/db.json", Format: "json"}},
	}}
	pod := applyCreds(t, spec, creds)
	ctr := caretakerCtr(t, pod)

	// App container mounts the emptyDir at the file's directory.
	var appDir bool
	for _, vm := range pod.Containers[0].VolumeMounts {
		if vm.MountPath == "/var/run/secrets" {
			appDir = true
		}
	}
	if !appDir {
		t.Fatalf("app container missing volume mount at /var/run/secrets: %+v", pod.Containers[0].VolumeMounts)
	}
	// Caretaker role writes the file into a scratch path under the shared volume.
	cfg := decodeCaretakerConfig(t, ctr.Env)
	d := cfg.Credentials[0].Deliver[0]
	if d.Kind != "file" || d.Format != "json" || d.Path == "/var/run/secrets/db.json" {
		t.Fatalf("file delivery not remapped to scratch: %+v", d)
	}
}
