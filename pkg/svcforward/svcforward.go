// Package svcforward opens a Kubernetes port-forward from the local machine to an
// in-cluster Service, so the cornus CLI can reach a cornus server running behind a
// ClusterIP Service without the user first running `kubectl port-forward` by hand.
// It loads the developer's kubeconfig, resolves the Service to a ready backing pod
// and its target port, and forwards a local ephemeral port to it over the SPDY
// pods/portforward subresource — the same mechanism kubectl uses.
package svcforward

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"cornus/pkg/kubeclient"
)

// shutdownGrace bounds how long Start waits for the forwarding goroutine to exit
// after cancellation. pf.ForwardPorts() performs a synchronous SPDY dial that does
// not observe stopCh, so a stuck dial can keep the goroutine alive indefinitely;
// rather than block the caller past its own deadline we give up after this grace
// period and leak the goroutine (which unwinds when the stuck dial eventually
// fails, or with the process).
const shutdownGrace = 5 * time.Second

// Options describes the in-cluster Service to forward to. It mirrors the
// clientconfig.PortForward profile block.
type Options struct {
	// KubeContext selects a kubeconfig context; empty uses the current context.
	KubeContext string
	// Namespace of the Service; empty uses the kubeconfig context's namespace.
	Namespace string
	// Service is the target Service name.
	Service string
	// RemotePort is the Service port to reach. When the Service exposes exactly one
	// port, 0 selects it.
	RemotePort int
}

// Forwarder is a running port-forward. LocalAddr is the 127.0.0.1:port the caller
// dials; Close tears the forward down.
type Forwarder struct {
	// LocalAddr is the local address the forward listens on (e.g. 127.0.0.1:54321).
	LocalAddr string
	stopCh    chan struct{}
	done      chan struct{}
}

// Close stops the forward and waits for its goroutine to exit. It is idempotent.
func (f *Forwarder) Close() {
	select {
	case <-f.stopCh:
		// already closed
	default:
		close(f.stopCh)
	}
	<-f.done
}

// Start loads the kubeconfig, resolves the Service to a ready pod and target port,
// and starts a port-forward on a local ephemeral port. It blocks until the forward
// is ready to accept connections (or ctx is cancelled).
func Start(ctx context.Context, o Options) (*Forwarder, error) {
	if o.Service == "" {
		return nil, fmt.Errorf("svcforward: service name is required")
	}

	clientset, restConfig, ns, err := kubeclient.Load(o.KubeContext, o.Namespace)
	if err != nil {
		return nil, fmt.Errorf("svcforward: %w", err)
	}

	pod, targetPort, err := resolveEndpoint(ctx, clientset, ns, o.Service, o.RemotePort)
	if err != nil {
		return nil, err
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(ns).
		Name(pod).
		SubResource("portforward")
	rt, upgrader, err := spdy.RoundTripperFor(restConfig)
	if err != nil {
		return nil, fmt.Errorf("svcforward: spdy round tripper: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: rt}, "POST", req.URL())

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	// "0:<target>" forwards a local ephemeral port to the pod's target port.
	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"},
		[]string{fmt.Sprintf("0:%d", targetPort)}, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("svcforward: new forwarder: %w", err)
	}

	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		errCh <- pf.ForwardPorts()
	}()

	select {
	case <-readyCh:
	case err := <-errCh:
		<-done
		if err == nil {
			err = fmt.Errorf("svcforward: forwarder exited before becoming ready")
		}
		return nil, fmt.Errorf("svcforward: %w", err)
	case <-ctx.Done():
		close(stopCh)
		waitClosed(done, shutdownGrace)
		return nil, ctx.Err()
	}

	ports, err := pf.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stopCh)
		<-done
		return nil, fmt.Errorf("svcforward: resolve local port: %w", err)
	}
	return &Forwarder{
		LocalAddr: fmt.Sprintf("127.0.0.1:%d", ports[0].Local),
		stopCh:    stopCh,
		done:      done,
	}, nil
}

// waitClosed blocks until done is closed or grace elapses. It returns true if done
// closed within the grace period and false if it timed out, so callers can bound an
// otherwise-unbounded wait on a goroutine that may be stuck.
func waitClosed(done <-chan struct{}, grace time.Duration) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// ResolveEndpoint maps an in-cluster Service (+ optional service port) to a ready
// backing pod and the numeric target port on that pod, via the Service's
// EndpointSlices. It is exported so a client-side forwarder that reaches an arbitrary
// Service without binding a local listener (e.g. the ingress-controller passthrough)
// can resolve the pod to SPDY-dial. remotePort is the Service port; 0 selects the
// sole port.
func ResolveEndpoint(ctx context.Context, clientset kubernetes.Interface, ns, service string, remotePort int) (pod string, targetPort int, err error) {
	return resolveEndpoint(ctx, clientset, ns, service, remotePort)
}

// resolveEndpoint maps a Service + service port to a ready backing pod and the
// numeric target port on that pod, using the Service's EndpointSlices (which already
// reflect pod readiness and resolve named target ports to numbers). remotePort is
// the Service port; 0 selects the sole port when the Service exposes exactly one.
//
// It reads discovery.k8s.io/v1 EndpointSlices rather than the core/v1 Endpoints they
// replaced: Endpoints is deprecated as of Kubernetes 1.33, and every request to it
// makes the apiserver return a deprecation Warning header that client-go logs.
func resolveEndpoint(ctx context.Context, clientset kubernetes.Interface, ns, service string, remotePort int) (pod string, targetPort int, err error) {
	// Map the requested Service port to its port name so we can match the
	// correspondingly-named EndpointSlice port (whose number is the pod target port).
	svc, err := clientset.CoreV1().Services(ns).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("svcforward: get service %s/%s: %w", ns, service, err)
	}
	if len(svc.Spec.Ports) == 0 {
		return "", 0, fmt.Errorf("svcforward: service %s/%s exposes no ports", ns, service)
	}
	var wantName string
	switch {
	case remotePort != 0:
		found := false
		for _, p := range svc.Spec.Ports {
			if int(p.Port) == remotePort {
				wantName = p.Name
				found = true
				break
			}
		}
		if !found {
			return "", 0, fmt.Errorf("svcforward: service %s/%s has no port %d", ns, service, remotePort)
		}
	case len(svc.Spec.Ports) == 1:
		wantName = svc.Spec.Ports[0].Name
	default:
		return "", 0, fmt.Errorf("svcforward: service %s/%s exposes %d ports; set a remote port", ns, service, len(svc.Spec.Ports))
	}

	// A Service's slices are found by the kubernetes.io/service-name label, not by
	// name: the controller shards a Service's endpoints across several slices and
	// names them arbitrarily (one set per address type, plus overflow slices).
	slices, err := clientset.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + service,
	})
	if err != nil {
		return "", 0, fmt.Errorf("svcforward: list endpointslices for %s/%s: %w", ns, service, err)
	}
	for i := range slices.Items {
		slice := &slices.Items[i]
		port, ok := endpointPort(slice.Ports, wantName)
		if !ok {
			continue
		}
		if pod := readyPod(slice.Endpoints); pod != "" {
			return pod, port, nil
		}
	}
	return "", 0, fmt.Errorf("svcforward: service %s/%s has no ready backing pods", ns, service)
}

// endpointPort returns the numeric port for the named endpoint port. When name is
// empty (an unnamed single Service port) it takes the sole endpoint port. A nil
// port number means "all ports" (only meaningful for some protocols) and cannot be
// dialled, so it is skipped.
func endpointPort(ports []discoveryv1.EndpointPort, name string) (int, bool) {
	if name == "" && len(ports) == 1 && ports[0].Port != nil {
		return int(*ports[0].Port), true
	}
	for _, p := range ports {
		if p.Port == nil {
			continue
		}
		pn := "" // a nil port name is the unnamed single port, i.e. name ""
		if p.Name != nil {
			pn = *p.Name
		}
		if pn == name {
			return int(*p.Port), true
		}
	}
	return 0, false
}

// readyPod returns the name of the first ready endpoint backed by a pod, or "" if
// the slice has none. Ready is a *bool whose nil means ready per the API contract;
// the EndpointSlice controller already reports terminating pods as not ready, but a
// slice written by another controller may leave Ready nil while flagging
// Terminating, so both are checked.
func readyPod(endpoints []discoveryv1.Endpoint) string {
	for _, ep := range endpoints {
		if c := ep.Conditions; (c.Ready != nil && !*c.Ready) || (c.Terminating != nil && *c.Terminating) {
			continue
		}
		if ep.TargetRef != nil && ep.TargetRef.Name != "" {
			return ep.TargetRef.Name
		}
	}
	return ""
}
