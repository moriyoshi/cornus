package kubernetes

import (
	"context"
	"fmt"
	"net"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"cornus/pkg/deploy"
	"cornus/pkg/ingressroute"
	"cornus/pkg/kubefwd"
	"cornus/pkg/svcforward"
)

// DialIngress opens a connection to the cluster's real ingress controller — the
// front door an ingress tunnel hands its traffic to on Kubernetes, so the
// controller itself does the Host/path routing rather than anything of ours.
// https selects the controller's HTTPS port (TLS and SNI pass straight through to
// it) over its plain HTTP port.
//
// Two paths, in preference order:
//
//   - In-cluster: a plain TCP dial to the controller Service's cluster DNS name.
//     A pod may dial any ClusterIP, so this needs NO RBAC at all — which matters,
//     because the Helm chart grants pods/portforward through a NAMESPACE-scoped
//     Role and the controller lives in another namespace (ingress-nginx), so the
//     server's ServiceAccount could not port-forward to it even if we wanted to.
//   - Out-of-cluster (a server driving the cluster with a kubeconfig): resolve the
//     Service to a ready backing pod and open a port-forward stream to it, the
//     same composition pkg/ingressnative uses client-side — but over the backend's
//     OWN credentials, which a kubeconfig-driven server has in full.
//
// It returns an error when no controller can be resolved, so the caller can fall
// back to the server-side mux.
func (b *Backend) DialIngress(ctx context.Context, https bool) (net.Conn, error) {
	c := b.ingressController(ctx)
	if c == nil || c.Service == "" {
		return nil, fmt.Errorf("kubernetes: no ingress controller found (set CORNUS_INGRESS_CONTROLLER as <namespace>/<service>[:http/https])")
	}
	port := c.HTTPPort
	if https {
		port = c.HTTPSPort
	}
	if port == 0 {
		if https {
			port = 443
		} else {
			port = 80
		}
	}

	if inCluster() {
		addr := net.JoinHostPort(c.Service+"."+c.Namespace+".svc", strconv.Itoa(port))
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("kubernetes: dialing ingress controller %s: %w", addr, err)
		}
		return conn, nil
	}

	if b.restConfig == nil {
		return nil, fmt.Errorf("kubernetes: reaching the ingress controller needs a real cluster connection (rest.Config); not available on this backend")
	}
	pod, targetPort, err := svcforward.ResolveEndpoint(ctx, b.clientset, c.Namespace, c.Service, port)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: resolving ingress controller %s/%s: %w", c.Namespace, c.Service, err)
	}
	return kubefwd.DialPod(ctx, b.clientset, b.restConfig, c.Namespace, pod, targetPort)
}

// IngressHosts reports the hostnames the cluster Ingress for name actually
// serves, read back from the live object rather than re-derived from the spec —
// so what a tunnel advertises is what the controller will really route. It
// returns nil (no error) when the deployment has no Ingress.
func (b *Backend) IngressHosts(ctx context.Context, name string) ([]string, error) {
	ing, err := b.clientset.NetworkingV1().Ingresses(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var hosts []string
	seen := map[string]bool{}
	for _, rule := range ing.Spec.Rules {
		h := ingressroute.CanonicalHost(rule.Host)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		hosts = append(hosts, h)
	}
	return hosts, nil
}

// inCluster reports whether this process is running inside the cluster it talks
// to, which is what makes a plain ClusterIP dial to the controller possible.
func inCluster() bool {
	_, err := rest.InClusterConfig()
	return err == nil
}

// Compile-time check that the backend satisfies the optional capability the
// server dispatches ingress tunnels on.
var _ deploy.IngressGateway = (*Backend)(nil)
