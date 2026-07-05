// Package kubelogs streams a cornus deployment's pod logs directly from the
// Kubernetes API using the developer's kubeconfig credentials, bypassing the
// cornus server's log-proxy endpoint. It exists because the server fulfils
// /.cornus/v1/deploy/{name}/logs with its own ServiceAccount, whose RBAC usually cannot
// read workload pod logs; the developer's own credentials (the same ones
// pkg/svcforward and pkg/kubeauth use) generally can. The `cornus compose logs`
// client prefers this path in cluster scenarios and falls back to the server
// proxy only as a last resort.
package kubelogs

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"cornus/pkg/deploy"
	"cornus/pkg/kubeclient"
)

// Options selects the pod to stream and how to stream it. Resource is the cornus
// deployment name (a compose service's plan.Resource), matched against the
// cornus.app pod label. The Log* fields mirror api.LogOptions (docker logs
// semantics). KubeContext / Namespace come from the connection profile; an empty
// Namespace resolves via the kubeconfig context (then "default") in kubeclient.Load.
type Options struct {
	KubeContext string
	Namespace   string
	Resource    string

	Follow     bool
	Tail       string
	Timestamps bool
	Since      string
}

// Open resolves the developer's kubeconfig, finds the deployment's pod, and opens
// its log stream. Every failure it can hit — kubeconfig load, pod list (RBAC),
// no matching pod, and the GetLogs stream open — surfaces here, before any log
// bytes are produced, so a caller can safely fall back to another path on error.
// The returned stream is the raw (unframed) combined container log; the caller
// owns closing it. ctx cancellation stops a follow.
func Open(ctx context.Context, o Options) (io.ReadCloser, error) {
	clientset, _, ns, err := kubeclient.Load(o.KubeContext, o.Namespace)
	if err != nil {
		return nil, err
	}
	return open(ctx, clientset, ns, o)
}

// open is Open's cluster-facing core, split out so tests can inject a fake
// clientset. It mirrors pkg/deploy/kubernetes Backend.Logs: select the pod by the
// cornus.app label (preferring a Running one), build PodLogOptions from the
// options, and open the stream.
//
// Like the server backend, this streams a single pod (the first Running one, else
// the first found); multi-replica log fan-in is not implemented.
func open(ctx context.Context, clientset kubernetes.Interface, ns string, o Options) (io.ReadCloser, error) {
	// deploy.ParseSince is the shared cross-backend since grammar (Unix
	// seconds[.nanos], RFC3339, or a duration relative to now). Garbage is an
	// error here — it must never silently degrade to "all logs".
	since, err := deploy.ParseSince(o.Since, time.Now())
	if err != nil {
		return nil, fmt.Errorf("kubelogs: %w", err)
	}
	pod, err := kubeclient.FirstPod(ctx, clientset, ns, o.Resource)
	if err != nil {
		return nil, err
	}
	podOpts := &corev1.PodLogOptions{
		Follow:     o.Follow,
		Timestamps: o.Timestamps,
	}
	if o.Tail != "" && o.Tail != "all" {
		// Like since above, garbage must be an error rather than silently
		// degrading to "all logs" (a nil TailLines makes GetLogs return the
		// entire combined container history).
		n, err := strconv.ParseInt(o.Tail, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("kubelogs: invalid tail %q", o.Tail)
		}
		podOpts.TailLines = ptr.To(n)
	}
	if !since.IsZero() {
		t := metav1.NewTime(since)
		podOpts.SinceTime = &t
	}
	return clientset.CoreV1().Pods(ns).GetLogs(pod, podOpts).Stream(ctx)
}
