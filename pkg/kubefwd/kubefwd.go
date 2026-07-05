// Package kubefwd opens raw TCP tunnels straight to a cornus workload pod's
// container port using the developer's kubeconfig credentials (the Kubernetes
// pods/portforward SPDY subresource, the same mechanism kubectl port-forward
// uses). It exists for the same reason as pkg/kubelogs: the cornus server proxies
// port-forwards with its own ServiceAccount, whose RBAC often cannot reach
// workload pods, whereas the developer's own credentials generally can. It
// satisfies portfwd.Dialer, so the CLI can prefer it over the server proxy and
// fall back only as a last resort.
//
// Only TCP is supported: the pods/portforward subresource is TCP-only, matching
// the kubernetes deploy backend (which likewise rejects UDP port-forwards).
package kubefwd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"cornus/pkg/kubeclient"
	"cornus/pkg/logging"
	"cornus/pkg/portfwd"
)

// Dialer opens one raw tunnel per call straight to a deployment's pod, resolving
// the pod by the cornus.app label (first Running). It satisfies portfwd.Dialer.
// The kubeconfig is loaded lazily on first use and cached, so a burst of accepted
// connections shares one client. The zero value is not usable; construct with New.
type Dialer struct {
	kubeContext string
	namespace   string

	// load loads the kubeconfig (clientset + REST config + effective namespace);
	// defaults to kubeclient.Load, overridable in tests.
	load func(kubeContext, namespace string) (kubernetes.Interface, *rest.Config, string, error)
	// dialPod opens a raw stream to pod:port; defaults to dialPodStream,
	// overridable in tests (SPDY needs a live API server otherwise).
	dialPod func(ctx context.Context, clientset kubernetes.Interface, restConfig *rest.Config, ns, pod string, port int) (net.Conn, error)

	mu         sync.Mutex
	loaded     bool
	clientset  kubernetes.Interface
	restConfig *rest.Config
	ns         string
	loadErr    error
}

// New returns a Dialer that reaches workload pods in the given kube context and
// namespace. An empty namespace resolves via the kubeconfig context (then
// "default"), matching kubeclient.Load.
func New(kubeContext, namespace string) *Dialer {
	return &Dialer{
		kubeContext: kubeContext,
		namespace:   namespace,
		load:        kubeclient.Load,
		dialPod:     dialPodStream,
	}
}

// PortForward opens a raw TCP tunnel to the deployment's pod container port. UDP
// is rejected (pods/portforward is TCP-only), which lets a Fallback caller defer
// to the server proxy for the (also-unsupported-on-kube) UDP probe rather than
// treating it as a hard failure. Every failure — kubeconfig load, pod lookup
// (RBAC), no pod, stream open — surfaces here before any bytes flow.
func (d *Dialer) PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error) {
	if proto == "udp" {
		return nil, fmt.Errorf("kubefwd: udp port-forward is not supported (pods/portforward is TCP-only)")
	}
	clientset, restConfig, ns, err := d.ensureLoaded()
	if err != nil {
		return nil, err
	}
	pod, err := kubeclient.FirstPod(ctx, clientset, ns, name)
	if err != nil {
		return nil, err
	}
	return d.dialPod(ctx, clientset, restConfig, ns, pod, port)
}

// ensureLoaded loads and caches the kubeconfig on first use. A load failure is
// cached too, so a broken kubeconfig fails fast without re-probing per connection.
func (d *Dialer) ensureLoaded() (kubernetes.Interface, *rest.Config, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.loaded {
		d.clientset, d.restConfig, d.ns, d.loadErr = d.load(d.kubeContext, d.namespace)
		d.loaded = true
	}
	return d.clientset, d.restConfig, d.ns, d.loadErr
}

// DialPod opens a raw TCP tunnel to a specific pod's container port over the
// pods/portforward SPDY subresource with the given kube clients. It is exported so a
// caller that resolves a non-cornus pod itself (e.g. an ingress controller Service's
// backing pod, via svcforward.ResolveEndpoint) can dial it without the cornus.app
// label lookup PortForward does.
func DialPod(ctx context.Context, clientset kubernetes.Interface, restConfig *rest.Config, ns, pod string, port int) (net.Conn, error) {
	return dialPodStream(ctx, clientset, restConfig, ns, pod, port)
}

// dialPodStream opens a SPDY connection to the pod's portforward subresource and
// creates the error+data stream pair for one forwarded connection, returning the
// data stream wrapped as a net.Conn. The SPDY connection is owned by the returned
// conn and closed with it. This mirrors client-go's own
// portforward.PortForwarder.handleConnection stream setup.
func dialPodStream(ctx context.Context, clientset kubernetes.Interface, restConfig *rest.Config, ns, pod string, port int) (net.Conn, error) {
	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(ns).
		Name(pod).
		SubResource("portforward")
	roundTripper, upgrader, err := spdy.RoundTripperFor(restConfig)
	if err != nil {
		return nil, fmt.Errorf("kubefwd: spdy round tripper: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, "POST", req.URL())
	streamConn, _, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return nil, fmt.Errorf("kubefwd: dial %s/%s portforward: %w", ns, pod, err)
	}

	// The error stream carries a forwarding setup error (e.g. connection refused
	// on the target port) out of band; we only read it. The data stream carries
	// the bidirectional byte stream. Both are tagged with the target port and a
	// per-connection requestID (0: one fresh SPDY connection per call).
	headers := http.Header{}
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	headers.Set(corev1.PortHeader, strconv.Itoa(port))
	headers.Set(corev1.PortForwardRequestIDHeader, "0")
	errorStream, err := streamConn.CreateStream(headers)
	if err != nil {
		streamConn.Close()
		return nil, fmt.Errorf("kubefwd: create error stream: %w", err)
	}
	// We never write to the error stream; half-close its write side.
	_ = errorStream.Close()

	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	dataStream, err := streamConn.CreateStream(headers)
	if err != nil {
		streamConn.Close()
		return nil, fmt.Errorf("kubefwd: create data stream: %w", err)
	}

	pc := &podConn{stream: dataStream, streamConn: streamConn}
	log := logging.FromContext(ctx)
	// Sever the tunnel if the pod reports a forwarding error mid-connection.
	go func() {
		msg, _ := io.ReadAll(errorStream)
		if len(msg) > 0 {
			log.DebugContext(ctx, "pod reported a forwarding error", slog.Group("kubefwd", "pod", pod, "port", port), "msg", string(msg))
			pc.Close()
		}
	}()
	return pc, nil
}

// podConn adapts an httpstream data stream (io.ReadWriteCloser) to net.Conn so it
// can be spliced by wire.Pipe. Only Read/Write/Close carry data; the address and
// deadline methods are inert stubs (wire.Pipe never calls them). Close tears down
// both the data stream and its owning SPDY connection, exactly once.
type podConn struct {
	stream     httpstream.Stream
	streamConn httpstream.Connection
	closeOnce  sync.Once
}

func (c *podConn) Read(b []byte) (int, error)  { return c.stream.Read(b) }
func (c *podConn) Write(b []byte) (int, error) { return c.stream.Write(b) }

func (c *podConn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.stream.Close()
		_ = c.streamConn.Close()
	})
	return nil
}

func (c *podConn) LocalAddr() net.Addr                { return podAddr{} }
func (c *podConn) RemoteAddr() net.Addr               { return podAddr{} }
func (c *podConn) SetDeadline(_ time.Time) error      { return nil }
func (c *podConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *podConn) SetWriteDeadline(_ time.Time) error { return nil }

type podAddr struct{}

func (podAddr) Network() string { return "kubefwd" }
func (podAddr) String() string  { return "pod" }

// Fallback is a portfwd.Dialer that tries Primary (e.g. a direct-to-pod Dialer)
// and, when it fails to open a tunnel, defers to Secondary (e.g. the server
// proxy). The fallback fires only on a Primary error with no bytes yet exchanged
// — PortForward's contract is that it returns before any traffic flows — so no
// data is ever duplicated. A ctx cancellation is returned as-is (not a reason to
// fall back).
type Fallback struct {
	Primary   portfwd.Dialer
	Secondary portfwd.Dialer
}

func (f Fallback) PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error) {
	conn, err := f.Primary.PortForward(ctx, name, port, proto)
	if err == nil {
		return conn, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	logging.FromContext(ctx).DebugContext(ctx, "direct pod port-forward unavailable; falling back to server proxy",
		"name", name, "port", port, "proto", proto, "error", err)
	// Report both attempts if the proxy also fails, so the caller (which logs the
	// dial error per connection) does not surface only the server-side error and
	// hide that the direct kubeconfig path was tried first.
	conn, proxyErr := f.Secondary.PortForward(ctx, name, port, proto)
	if proxyErr != nil {
		return nil, fmt.Errorf("direct pod port-forward failed (%v); server proxy fallback also failed: %w", err, proxyErr)
	}
	return conn, nil
}
