package kubernetes

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
	"cornus/pkg/deploy"

	// The anthropic-proxy provider must be registered so the backend resolves its
	// Env() at apply time.
	_ "cornus/pkg/creddelivery/anthropicproxy"
)

// TestCredentialProxyEndpoint: an anthropic-proxy delivery injects ANTHROPIC_BASE_URL
// into the app container and a caretaker credential role (it rides the endpoint kind).
func TestCredentialProxyEndpoint(t *testing.T) {
	spec := api.DeploySpec{Name: "agent", Image: "img:v1"}
	creds := []deploy.AttachCredential{{
		Name:     "claude",
		Session:  "s",
		RelayURL: "ws://r",
		// An upstream override (a compatible gateway / test mock) must thread
		// through to the caretaker's resolved delivery.
		Deliver: []api.CredentialDelivery{{Kind: "endpoint", Provider: "anthropic-proxy", Upstream: "http://gw.internal:8080"}},
	}}
	pod := applyCreds(t, spec, creds)

	env := appEnv(pod)
	if env["ANTHROPIC_BASE_URL"] == "" {
		t.Fatalf("app env missing ANTHROPIC_BASE_URL: %v", env)
	}
	cfg := decodeCaretakerConfig(t, caretakerCtr(t, pod).Env)
	if len(cfg.Credentials) != 1 || cfg.Credentials[0].Deliver[0].Provider != "anthropic-proxy" {
		t.Fatalf("credential role = %+v", cfg.Credentials)
	}
	if got := cfg.Credentials[0].Deliver[0].Upstream; got != "http://gw.internal:8080" {
		t.Fatalf("caretaker delivery upstream = %q, want the override threaded through", got)
	}
}

// TestCredentialEnvDelivery: an env delivery injects a secretKeyRef env on the app
// container, creates an owned Secret with the server-resolved value, and adds NO
// caretaker role (env is static, materialized at deploy time).
func TestCredentialEnvDelivery(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	spec := api.DeploySpec{Name: "agent", Image: "img:v1"}
	// The server splits env deliveries out of Deliver into resolved EnvVars, so an
	// env-only credential reaches the backend with an empty Deliver.
	creds := []deploy.AttachCredential{{
		Name:    "openai",
		Session: "s",
		EnvVars: []deploy.CredentialEnvVar{{Var: "OPENAI_API_KEY", Value: "sk-openai-secret"}},
	}}
	if _, err := b.ApplyWithAttachments(context.Background(), spec, nil, creds, nil); err != nil {
		t.Fatalf("ApplyWithAttachments: %v", err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(context.Background(), "agent", metav1.GetOptions{})
	pod := dep.Spec.Template.Spec

	// secretKeyRef env on the app container.
	var ref *corev1.SecretKeySelector
	for _, e := range pod.Containers[0].Env {
		if e.Name == "OPENAI_API_KEY" && e.ValueFrom != nil {
			ref = e.ValueFrom.SecretKeyRef
		}
	}
	if ref == nil {
		t.Fatalf("app container missing OPENAI_API_KEY secretKeyRef: %+v", pod.Containers[0].Env)
	}
	if ref.Name != credentialSecretName("agent") || ref.Key != "OPENAI_API_KEY" {
		t.Fatalf("secretKeyRef = %s/%s", ref.Name, ref.Key)
	}
	// The Secret exists with the resolved value and is owned by the Deployment.
	sec, err := cs.CoreV1().Secrets("default").Get(context.Background(), credentialSecretName("agent"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get credential secret: %v", err)
	}
	if sec.StringData["OPENAI_API_KEY"] != "sk-openai-secret" {
		t.Fatalf("secret value = %q", sec.StringData["OPENAI_API_KEY"])
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].UID != dep.UID {
		t.Fatalf("secret not owned by the deployment: %+v", sec.OwnerReferences)
	}
	// No caretaker role and no sidecar for an env-only source.
	for _, c := range pod.InitContainers {
		if c.Name == "cornus-caretaker" {
			t.Fatal("env-only credential should not inject a caretaker sidecar")
		}
	}
}
