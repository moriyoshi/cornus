package kubernetes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/deploy"
)

func TestApplyRegistryPullCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	b := NewWithClient(client, "test", WithRegistryCredentials(func(_ context.Context, ref string) (deploy.RegistryCredential, bool, error) {
		if !strings.HasPrefix(ref, "registry.cornus.test:5000/") {
			return deploy.RegistryCredential{}, false, nil
		}
		return deploy.RegistryCredential{Username: "cornus-internal", Password: "pull-token"}, true, nil
	}))

	pod := corev1.PodSpec{}
	if err := b.applyRegistryPullCredential(ctx, "registry.cornus.test:5000/team/app:latest", &pod); err != nil {
		t.Fatal(err)
	}
	if len(pod.ImagePullSecrets) != 1 || pod.ImagePullSecrets[0].Name != registryPullSecretName {
		t.Fatalf("ImagePullSecrets = %#v", pod.ImagePullSecrets)
	}
	secret, err := client.CoreV1().Secrets("test").Get(ctx, registryPullSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson {
		t.Fatalf("type = %q", secret.Type)
	}
	if len(secret.OwnerReferences) != 0 {
		t.Fatalf("owner references = %#v", secret.OwnerReferences)
	}
	if secret.Labels[deploy.LabelManaged] != "true" {
		t.Fatalf("labels = %#v", secret.Labels)
	}
	var config dockerConfigJSON
	if err := json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &config); err != nil {
		t.Fatal(err)
	}
	auth, ok := config.Auths["registry.cornus.test:5000"]
	if !ok {
		t.Fatalf("auths = %#v", config.Auths)
	}
	if auth.Username != "cornus-internal" || auth.Password != "pull-token" {
		t.Fatalf("auth = %#v", auth)
	}
	wantEncoded := base64.StdEncoding.EncodeToString([]byte("cornus-internal:pull-token"))
	if auth.Auth != wantEncoded {
		t.Fatalf("encoded auth = %q, want %q", auth.Auth, wantEncoded)
	}

	externalPod := corev1.PodSpec{}
	if err := b.applyRegistryPullCredential(ctx, "docker.io/library/alpine:latest", &externalPod); err != nil {
		t.Fatal(err)
	}
	if len(externalPod.ImagePullSecrets) != 0 {
		t.Fatalf("external ImagePullSecrets = %#v", externalPod.ImagePullSecrets)
	}
}

func TestRefreshRegistryCredentials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old, err := json.Marshal(dockerConfigJSON{Auths: map[string]dockerConfigAuth{
		"registry.cornus.test:5000": newDockerConfigAuth(deploy.RegistryCredential{Username: "old", Password: "old"}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: registryPullSecretName, Namespace: "test", Labels: map[string]string{deploy.LabelManaged: "true"}},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: old},
	})
	b := NewWithClient(client, "test", WithRegistryCredentials(func(_ context.Context, ref string) (deploy.RegistryCredential, bool, error) {
		if ref != "registry.cornus.test:5000/cornus-refresh" {
			t.Fatalf("refresh ref = %q", ref)
		}
		return deploy.RegistryCredential{Username: "cornus-internal", Password: "fresh"}, true, nil
	}))
	if err := b.RefreshRegistryCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	secret, err := client.CoreV1().Secrets("test").Get(ctx, registryPullSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var config dockerConfigJSON
	if err := json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &config); err != nil {
		t.Fatal(err)
	}
	if got := config.Auths["registry.cornus.test:5000"].Password; got != "fresh" {
		t.Fatalf("password = %q", got)
	}
}

func TestRegistryPullSecretOwnershipGuard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: registryPullSecretName, Namespace: "test",
	}})
	b := NewWithClient(client, "test", WithRegistryCredentials(func(context.Context, string) (deploy.RegistryCredential, bool, error) {
		return deploy.RegistryCredential{Username: "u", Password: "p"}, true, nil
	}))
	err := b.applyRegistryPullCredential(ctx, "registry.cornus.test:5000/app:latest", &corev1.PodSpec{})
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite unowned Secret") {
		t.Fatalf("error = %v", err)
	}
}

func TestRefreshRegistryCredentialsMissingSecret(t *testing.T) {
	t.Parallel()
	b := NewWithClient(fake.NewSimpleClientset(), "test", WithRegistryCredentials(func(context.Context, string) (deploy.RegistryCredential, bool, error) {
		return deploy.RegistryCredential{Username: "u", Password: "p"}, true, nil
	}))
	if err := b.RefreshRegistryCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
}
