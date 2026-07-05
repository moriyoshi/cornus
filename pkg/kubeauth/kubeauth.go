// Package kubeauth mints a short-lived, audience-scoped Kubernetes ServiceAccount
// token using the developer's own kube credentials, so the cornus CLI can present
// a cluster-issued JWT to an in-cluster cornus server instead of a separately
// provisioned bearer token. The server validates it against the cluster's OIDC
// JWKS (the existing CORNUS_JWT_JWKS_URL / _AUDIENCE / _ISSUER path) — no
// server-side code change is required.
package kubeauth

import (
	"context"
	"fmt"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"cornus/pkg/kubeclient"
)

// defaultExpirationSeconds is the token lifetime requested when none is set. The
// API server clamps to its own bounds; an hour is a reasonable CLI session length.
const defaultExpirationSeconds int64 = 3600

// Options describes the ServiceAccount token to mint.
type Options struct {
	// KubeContext selects a kubeconfig context; empty uses the current context.
	KubeContext string
	// Namespace of the ServiceAccount; empty uses the kubeconfig context's namespace.
	Namespace string
	// ServiceAccount is the name of the ServiceAccount to mint a token for.
	ServiceAccount string
	// Audience is the token audience; it must match the server's CORNUS_JWT_AUDIENCE.
	Audience string
	// ExpirationSeconds is the requested token lifetime; 0 uses the default.
	ExpirationSeconds int64
}

// Token mints the token via the TokenRequest API. The caller's kube identity must
// hold RBAC to create tokens for the ServiceAccount (create on the
// serviceaccounts/token subresource).
func Token(ctx context.Context, o Options) (string, error) {
	if o.ServiceAccount == "" {
		return "", fmt.Errorf("kubeauth: service account is required")
	}
	if o.Audience == "" {
		return "", fmt.Errorf("kubeauth: audience is required (must match the server CORNUS_JWT_AUDIENCE)")
	}
	clientset, _, ns, err := kubeclient.Load(o.KubeContext, o.Namespace)
	if err != nil {
		return "", err
	}
	return mint(ctx, clientset, ns, o)
}

// mint issues the TokenRequest against clientset; split out so tests can drive it
// with a fake clientset.
func mint(ctx context.Context, clientset kubernetes.Interface, ns string, o Options) (string, error) {
	exp := o.ExpirationSeconds
	if exp == 0 {
		exp = defaultExpirationSeconds
	}
	tr := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			Audiences:         []string{o.Audience},
			ExpirationSeconds: &exp,
		},
	}
	resp, err := clientset.CoreV1().ServiceAccounts(ns).CreateToken(ctx, o.ServiceAccount, tr, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("kubeauth: mint token for serviceaccount %s/%s: %w", ns, o.ServiceAccount, err)
	}
	if resp.Status.Token == "" {
		return "", fmt.Errorf("kubeauth: TokenRequest returned an empty token for %s/%s", ns, o.ServiceAccount)
	}
	return resp.Status.Token, nil
}
