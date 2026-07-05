// Package ingressnative reaches a cluster's real HTTP(S) ingress controller Service
// through the SOCKS5 conduit: it opens a raw TCP tunnel to a ready pod behind the
// controller Service (with the developer's kubeconfig), so a browser doing remote DNS
// (socks5h) delivers its TLS ClientHello (SNI) and HTTP Host header straight to the
// real controller, which does the actual Host/path routing and terminates TLS with
// the cluster's own certificate. Unlike pkg/kubefwd it targets an arbitrary Service
// (resolved via its Endpoints), not a cornus.app-labelled deployment pod.
package ingressnative

import (
	"context"
	"fmt"
	"net"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"cornus/pkg/kubeclient"
	"cornus/pkg/kubefwd"
	"cornus/pkg/svcforward"
)

// Controller identifies the ingress controller Service a native passthrough tunnels
// to. HTTPPort / HTTPSPort are the Service ports (defaulting to 80 / 443).
type Controller struct {
	KubeContext string
	Namespace   string
	Service     string
	HTTPPort    int
	HTTPSPort   int
}

// Dialer opens tunnels to the controller Service's pods via the developer's
// kubeconfig, loading and caching the kube clients lazily. Safe for concurrent use.
// The behaviour-carrying dependencies are seams so tests can stub the cluster.
type Dialer struct {
	c Controller

	load    func(kubeContext, namespace string) (kubernetes.Interface, *rest.Config, string, error)
	resolve func(ctx context.Context, cs kubernetes.Interface, ns, service string, remotePort int) (string, int, error)
	dial    func(ctx context.Context, cs kubernetes.Interface, rc *rest.Config, ns, pod string, port int) (net.Conn, error)

	mu      sync.Mutex
	loaded  bool
	cs      kubernetes.Interface
	rc      *rest.Config
	ns      string
	loadErr error
}

// New returns a Dialer for the controller Service c, with the production kube seams
// installed. HTTPPort / HTTPSPort default to 80 / 443 when zero.
func New(c Controller) *Dialer {
	if c.HTTPPort == 0 {
		c.HTTPPort = 80
	}
	if c.HTTPSPort == 0 {
		c.HTTPSPort = 443
	}
	return &Dialer{
		c:       c,
		load:    kubeclient.Load,
		resolve: svcforward.ResolveEndpoint,
		dial:    kubefwd.DialPod,
	}
}

// HTTPPort and HTTPSPort report the resolved controller Service ports.
func (d *Dialer) HTTPPort() int  { return d.c.HTTPPort }
func (d *Dialer) HTTPSPort() int { return d.c.HTTPSPort }

func (d *Dialer) ensureLoaded() (kubernetes.Interface, *rest.Config, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.loaded {
		// The controller Service's own namespace (e.g. ingress-nginx) governs the
		// lookup, not the kubeconfig context default.
		d.cs, d.rc, d.ns, d.loadErr = d.load(d.c.KubeContext, d.c.Namespace)
		d.loaded = true
	}
	return d.cs, d.rc, d.ns, d.loadErr
}

// DialService opens a raw TCP tunnel to a ready pod behind the controller Service on
// the given Service port. It is called per accepted SOCKS5 CONNECT so the browser's
// bytes reach the real controller unmodified.
func (d *Dialer) DialService(ctx context.Context, servicePort int) (net.Conn, error) {
	if d.c.Service == "" {
		return nil, fmt.Errorf("ingressnative: no controller service configured")
	}
	cs, rc, ns, err := d.ensureLoaded()
	if err != nil {
		return nil, err
	}
	pod, targetPort, err := d.resolve(ctx, cs, ns, d.c.Service, servicePort)
	if err != nil {
		return nil, err
	}
	return d.dial(ctx, cs, rc, ns, pod, targetPort)
}
