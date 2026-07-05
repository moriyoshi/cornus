// Package kubeclient loads a Kubernetes client from the developer's kubeconfig for
// the CLI-side features that reach a cluster directly (port-forwarding to an
// in-cluster cornus Service, and minting a ServiceAccount token to authenticate to
// it). It centralizes the kubeconfig loading and namespace resolution both share.
package kubeclient

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"cornus/pkg/deploy"
)

// Load builds a Kubernetes clientset and REST config from the default kubeconfig
// (KUBECONFIG / ~/.kube/config), honoring an optional context override. It returns
// the effective namespace: the explicit namespace argument when non-empty, else the
// selected context's namespace, else "default". It does not contact the cluster.
func Load(kubeContext, namespace string) (kubernetes.Interface, *rest.Config, string, error) {
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	)
	restConfig, err := loader.ClientConfig()
	if err != nil {
		return nil, nil, "", fmt.Errorf("kubeclient: load kubeconfig: %w", err)
	}
	ns := namespace
	if ns == "" {
		if n, _, err := loader.Namespace(); err == nil {
			ns = n
		}
	}
	if ns == "" {
		ns = "default"
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, "", fmt.Errorf("kubeclient: build client: %w", err)
	}
	return clientset, restConfig, ns, nil
}

// FirstPod returns the name of the pod backing a cornus deployment, selected by
// the cornus.app label and preferring a Running pod (falling back to the first
// found). It is the shared CLI-side pod resolver for the features that reach a
// workload pod directly (log streaming, port-forwarding), mirroring the server
// kubernetes backend's own firstPod. A deployment with no pods yields an error
// wrapping deploy.ErrNotFound.
func FirstPod(ctx context.Context, clientset kubernetes.Interface, ns, resource string) (string, error) {
	pods, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: deploy.LabelApp + "=" + resource,
	})
	if err != nil {
		return "", fmt.Errorf("kubeclient: list pods for %q: %w", resource, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("kubeclient: no pods for deployment %q in namespace %q: %w", resource, ns, deploy.ErrNotFound)
	}
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			return pods.Items[i].Name, nil
		}
	}
	return pods.Items[0].Name, nil
}
