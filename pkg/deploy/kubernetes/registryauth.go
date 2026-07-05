package kubernetes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	"cornus/pkg/deploy"
	"cornus/pkg/imageref"
)

const registryPullSecretName = "cornus-registry-pull"

type dockerConfigJSON struct {
	Auths map[string]dockerConfigAuth `json:"auths"`
}

type dockerConfigAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Auth     string `json:"auth"`
}

func newDockerConfigAuth(credential deploy.RegistryCredential) dockerConfigAuth {
	return dockerConfigAuth{
		Username: credential.Username,
		Password: credential.Password,
		Auth:     base64.StdEncoding.EncodeToString([]byte(credential.Username + ":" + credential.Password)),
	}
}

// applyRegistryPullCredential writes a namespace-scoped pull Secret only for a
// reference whose host the server credential resolver recognizes. The Secret
// deliberately has no owner reference: kubelets may need it long after the
// workload that first caused it to be created has gone away.
func (b *Backend) applyRegistryPullCredential(ctx context.Context, ref string, podSpec *corev1.PodSpec) error {
	if b.registryCredentials == nil {
		return nil
	}
	credential, ok, err := b.registryCredentials(ctx, ref)
	if err != nil {
		return fmt.Errorf("kubernetes: resolve registry credential: %w", err)
	}
	if !ok {
		return nil
	}
	host, _ := imageref.SplitHostRepo(ref)
	if host == "" {
		return fmt.Errorf("kubernetes: authenticated image reference %q has no registry host", ref)
	}
	if err := b.mutateRegistryPullSecret(ctx, true, func(config *dockerConfigJSON) error {
		config.Auths[host] = newDockerConfigAuth(credential)
		return nil
	}); err != nil {
		return err
	}
	podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: registryPullSecretName}}
	return nil
}

// RefreshRegistryCredentials refreshes every host already represented in the
// namespace Secret. A missing Secret is a normal pre-deploy state and is a
// no-op. The kubelet reads this object at pull time, so no pod restart is needed.
func (b *Backend) RefreshRegistryCredentials(ctx context.Context) error {
	if b.registryCredentials == nil {
		return nil
	}
	return b.mutateRegistryPullSecret(ctx, false, func(config *dockerConfigJSON) error {
		for host := range config.Auths {
			credential, ok, err := b.registryCredentials(ctx, host+"/cornus-refresh")
			if err != nil {
				return fmt.Errorf("refresh registry credential for %s: %w", host, err)
			}
			if !ok {
				delete(config.Auths, host)
				continue
			}
			config.Auths[host] = newDockerConfigAuth(credential)
		}
		return nil
	})
}

var _ deploy.RegistryCredentialRefresher = (*Backend)(nil)

func (b *Backend) mutateRegistryPullSecret(ctx context.Context, create bool, mutate func(*dockerConfigJSON) error) error {
	secrets := b.clientset.CoreV1().Secrets(b.namespace)
	return retry.OnError(retry.DefaultRetry, func(err error) bool {
		return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
	}, func() error {
		secret, err := secrets.Get(ctx, registryPullSecretName, metav1.GetOptions{})
		existing := err == nil
		if apierrors.IsNotFound(err) {
			if !create {
				return nil
			}
			secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name:      registryPullSecretName,
				Namespace: b.namespace,
				Labels:    map[string]string{deploy.LabelManaged: "true"},
			}}
		} else if err != nil {
			return err
		} else if secret.Labels[deploy.LabelManaged] != "true" {
			return fmt.Errorf("kubernetes: refusing to overwrite unowned Secret %s/%s", b.namespace, registryPullSecretName)
		}

		config := dockerConfigJSON{Auths: map[string]dockerConfigAuth{}}
		if raw := secret.Data[corev1.DockerConfigJsonKey]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &config); err != nil {
				return fmt.Errorf("kubernetes: decode Secret %s/%s: %w", b.namespace, registryPullSecretName, err)
			}
			if config.Auths == nil {
				config.Auths = map[string]dockerConfigAuth{}
			}
		}
		if err := mutate(&config); err != nil {
			return err
		}
		raw, err := json.Marshal(config)
		if err != nil {
			return err
		}
		secret.Type = corev1.SecretTypeDockerConfigJson
		secret.Data = map[string][]byte{corev1.DockerConfigJsonKey: raw}
		secret.OwnerReferences = nil
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels[deploy.LabelManaged] = "true"
		if existing {
			_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{})
		} else {
			_, err = secrets.Create(ctx, secret, metav1.CreateOptions{})
		}
		return err
	})
}
