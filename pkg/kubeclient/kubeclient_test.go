package kubeclient

import (
	"os"
	"path/filepath"
	"testing"
)

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: c1
  cluster:
    server: https://example.test:6443
contexts:
- name: ctx1
  context:
    cluster: c1
    user: u1
    namespace: ns-one
- name: ctx2
  context:
    cluster: c1
    user: u1
    namespace: ns-two
current-context: ctx1
users:
- name: u1
  user:
    token: fake-token
`

func writeKubeconfig(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
}

func TestLoadNamespaceResolution(t *testing.T) {
	writeKubeconfig(t)

	// Current context's namespace.
	cs, cfg, ns, err := Load("", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cs == nil || cfg == nil {
		t.Fatal("Load returned nil clientset/config")
	}
	if ns != "ns-one" {
		t.Errorf("namespace = %q, want ns-one", ns)
	}

	// Context override picks that context's namespace.
	if _, _, ns, err := Load("ctx2", ""); err != nil || ns != "ns-two" {
		t.Errorf("Load(ctx2) ns = %q, %v; want ns-two", ns, err)
	}

	// Explicit namespace wins over the context.
	if _, _, ns, err := Load("", "override"); err != nil || ns != "override" {
		t.Errorf("Load(namespace=override) ns = %q, %v; want override", ns, err)
	}
}

func TestLoadUnknownContextErrors(t *testing.T) {
	writeKubeconfig(t)
	if _, _, _, err := Load("does-not-exist", ""); err == nil {
		t.Error("Load(unknown context) = nil error, want error")
	}
}
