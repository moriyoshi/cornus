package svcforward

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"cornus/pkg/kubeclient"
)

// DiscoverOptions selects the cluster and namespace to search for a cornus
// install. It mirrors the kube-facing fields of clientconfig.PortForward.
type DiscoverOptions struct {
	// KubeContext selects a kubeconfig context; empty uses the current context.
	KubeContext string
	// Namespace to search; empty uses the kubeconfig context's namespace.
	Namespace string
}

// DiscoverResult is the client-facing cornus Service found in a namespace.
type DiscoverResult struct {
	// Service is the resolved Service name.
	Service string
	// RemotePort is the resolved Service port to forward to.
	RemotePort int
	// Managed labels which install scheme matched: "helm" or "manifest".
	Managed string
}

// discoverSelectors are the label selectors that identify a cornus Service,
// tried in order. The Helm chart labels its Service app.kubernetes.io/name=cornus
// (deploy/helm/cornus/templates/_helpers.tpl); the raw manifest uses app=cornus
// (deploy/k8s/cornus.yaml).
var discoverSelectors = []struct {
	selector string
	managed  string
}{
	{"app.kubernetes.io/name=cornus", "helm"},
	{"app=cornus", "manifest"},
}

// Discover loads the kubeconfig and finds the client-facing cornus Service in a
// namespace, returning its name and port so a connection profile can be filled in
// without the user knowing the install's naming. It errors when zero or more than
// one client-facing Service matches (see discover).
func Discover(ctx context.Context, o DiscoverOptions) (DiscoverResult, error) {
	clientset, _, ns, err := kubeclient.Load(o.KubeContext, o.Namespace)
	if err != nil {
		return DiscoverResult{}, fmt.Errorf("svcforward: %w", err)
	}
	return discover(ctx, clientset, ns)
}

// discover is the cluster-facing half of Discover, split out so it can be tested
// with a fake clientset. It tries each label scheme in turn, excludes the headless
// hub Service (which carries the same labels but is inter-replica traffic), and
// requires exactly one remaining candidate.
func discover(ctx context.Context, clientset kubernetes.Interface, ns string) (DiscoverResult, error) {
	for _, s := range discoverSelectors {
		list, err := clientset.CoreV1().Services(ns).List(ctx, metav1.ListOptions{LabelSelector: s.selector})
		if err != nil {
			return DiscoverResult{}, fmt.Errorf("svcforward: list services in namespace %q: %w", ns, err)
		}
		var candidates []corev1.Service
		for _, svc := range list.Items {
			if svc.Spec.ClusterIP == corev1.ClusterIPNone {
				continue // headless hub Service, not client traffic
			}
			candidates = append(candidates, svc)
		}
		switch {
		case len(candidates) == 0:
			continue // try the next label scheme
		case len(candidates) > 1:
			return DiscoverResult{}, fmt.Errorf("svcforward: multiple cornus services in namespace %q:\n%s\nre-run with --pf-service to pick one", ns, formatCandidates(candidates))
		}
		svc := candidates[0]
		port, ok := servicePort(svc.Spec.Ports)
		if !ok {
			return DiscoverResult{}, fmt.Errorf("svcforward: service %s/%s exposes %d ports and none is named %q or 5000; set --pf-remote-port", ns, svc.Name, len(svc.Spec.Ports), "http")
		}
		return DiscoverResult{Service: svc.Name, RemotePort: port, Managed: s.managed}, nil
	}
	return DiscoverResult{}, fmt.Errorf("svcforward: no cornus service found in namespace %q (pass --pf-service to set one manually)", ns)
}

// servicePort picks the client-facing port of a cornus Service: the port named
// "http" if present, else port 5000, else the sole port. It reports false when the
// Service has multiple ports and none is identifiable, so the caller can ask for an
// explicit --pf-remote-port.
func servicePort(ports []corev1.ServicePort) (int, bool) {
	for _, p := range ports {
		if p.Name == "http" {
			return int(p.Port), true
		}
	}
	for _, p := range ports {
		if p.Port == 5000 {
			return int(p.Port), true
		}
	}
	if len(ports) == 1 {
		return int(ports[0].Port), true
	}
	return 0, false
}

// formatCandidates renders the "  - name (port)" candidate list used in the
// ambiguous-match error.
func formatCandidates(svcs []corev1.Service) string {
	var b strings.Builder
	for i, svc := range svcs {
		if i > 0 {
			b.WriteByte('\n')
		}
		if port, ok := servicePort(svc.Spec.Ports); ok {
			fmt.Fprintf(&b, "  - %s (port %d)", svc.Name, port)
		} else {
			fmt.Fprintf(&b, "  - %s", svc.Name)
		}
	}
	return b.String()
}
