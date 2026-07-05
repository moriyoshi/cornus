package composecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/api"
	"cornus/pkg/kubelogs"
	"cornus/pkg/logging"
)

// kubeLogOpener opens a direct-from-cluster log stream for a deployment resource,
// using the developer's kubeconfig credentials. It is the seam the log commands use
// to prefer a cluster-native path over the server proxy (and that tests fake).
type kubeLogOpener interface {
	// Open opens the resource's pod log stream. Every setup failure (kubeconfig,
	// pod lookup, RBAC, stream open) surfaces here before any bytes are produced,
	// so the caller can fall back to the server proxy without risking duplicated
	// output. The returned stream is the raw combined container log.
	Open(ctx context.Context, resource string, opts api.LogOptions) (io.ReadCloser, error)
}

// kubeLogSource is the production kubeLogOpener: it streams pod logs with the
// developer's kubeconfig for a cluster profile (see pkg/kubelogs).
type kubeLogSource struct {
	kubeContext string
	namespace   string
}

func (k *kubeLogSource) Open(ctx context.Context, resource string, opts api.LogOptions) (io.ReadCloser, error) {
	// The kube pods/log API has no upper time bound, so --until cannot be honored
	// on this direct-pod path either; warn rather than silently drop it.
	if opts.Until != "" {
		logging.FromContext(ctx).WarnContext(ctx, "logs --until is not supported on the kubernetes backend (pods/log has no upper time bound); ignoring", "service", resource)
	}
	return kubelogs.Open(ctx, kubelogs.Options{
		KubeContext: k.kubeContext,
		Namespace:   k.namespace,
		Resource:    resource,
		Follow:      opts.Follow,
		Tail:        opts.Tail,
		Timestamps:  opts.Timestamps,
		Since:       opts.Since,
	})
}

// LogsCmd shows service logs, mirroring `docker compose logs`.
type LogsCmd struct {
	Services []string `kong:"arg,optional,help='Services to show logs for (default: all).'"`
	// No short 'f': the parent `compose` group already owns -f for --file (a
	// global flag inherited by every subcommand), so -f here would be ambiguous.
	Follow      bool   `kong:"name='follow',help='Follow log output.'"`
	Tail        string `kong:"name='tail',short='n',default='all',help='Number of lines to show from the end of the logs, per service (\"all\" for everything).'"`
	Timestamps  bool   `kong:"name='timestamps',short='t',help='Show timestamps.'"`
	Since       string `kong:"name='since',help='Show logs since a timestamp (RFC3339) or relative duration (e.g. 42m).'"`
	Until       string `kong:"name='until',help='Show logs before a timestamp (RFC3339) or relative duration (e.g. 42m). Not supported on the kubernetes backend (ignored with a warning).'"`
	NoLogPrefix bool   `kong:"name='no-log-prefix',help='Do not prefix each log line with its service name.'"`
}

// Run streams logs for the selected services. Every service streams
// concurrently (as Docker Compose does), so with --follow all services are
// tailed together until Ctrl-C.
func (c *LogsCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	rt, err := cli.load(r, d)
	if err != nil {
		return err
	}
	defer rt.cleanup()
	ctx, stop := signalContext()
	defer stop()

	names, err := rt.selectServices(c.Services)
	if err != nil {
		return err
	}
	opts := api.LogOptions{
		Follow:     c.Follow,
		Tail:       c.Tail,
		Timestamps: c.Timestamps,
		Since:      c.Since,
		Until:      c.Until,
	}
	return rt.streamLogs(ctx, names, opts, !c.NoLogPrefix, d.Out(), d.Err())
}

// streamLogs streams the given services' logs concurrently, demultiplexing each
// service's Docker raw-stream into stdout/stderr. When prefix is set, every line
// is tagged with its (width-padded) service name, matching `docker compose logs`.
// A shared mutex keeps concurrent writers from interleaving a partial line on
// the shared output. Returns the first non-cancellation error across services;
// context cancellation (Ctrl-C on a --follow) is treated as a clean stop.
func (r *runtime) streamLogs(ctx context.Context, names []string, opts api.LogOptions, prefix bool, stdout, stderr io.Writer) error {
	// All per-service writers share one LineGroup mutex, so concurrent services
	// never interleave a partial line on the shared output — and json mode wraps
	// each line as an NDJSON log object.
	group := r.driver().LineGroup()
	width := 0
	if prefix {
		for _, n := range names {
			if len(n) > width {
				width = len(n)
			}
		}
	}

	var wg sync.WaitGroup
	var closers []io.WriteCloser
	errs := make([]error, len(names))
	for i, name := range names {
		p := ""
		if prefix {
			p = fmt.Sprintf("%-*s | ", width, name)
		}
		outW := group.Writer(stdout, p)
		errW := group.Writer(stderr, p)
		closers = append(closers, outW, errW)
		wg.Add(1)
		go func(i int, resource string, outW, errW io.Writer) {
			defer wg.Done()
			errs[i] = r.streamServiceLogs(ctx, resource, opts, outW, errW)
		}(i, r.plans[name].Resource, outW, errW)
	}
	wg.Wait()

	// Close flushes any trailing partial line (a final log entry without a newline).
	for _, c := range closers {
		c.Close()
	}

	for _, e := range errs {
		if e != nil && !errors.Is(e, context.Canceled) {
			return e
		}
	}
	return nil
}

// streamServiceLogs streams one deployment's logs into the stdout/stderr writers.
// In a cluster profile it first tries the direct-from-cluster path (r.kubeLogs),
// which reads pod logs with the developer's kubeconfig credentials; the server's
// own ServiceAccount usually cannot. Only if that path fails to start (kubeconfig,
// pod lookup, RBAC, stream open — all before any bytes) does it fall back to the
// server proxy, which becomes the last resort. Non-cluster profiles (r.kubeLogs
// nil) use the proxy directly.
func (r *runtime) streamServiceLogs(ctx context.Context, resource string, opts api.LogOptions, outW, errW io.Writer) error {
	if r.kubeLogs != nil {
		// Open surfaces every setup failure before any bytes flow. On success the
		// copy result is returned as-is: once output has (possibly) been written we
		// must NOT retry through the proxy, or the log would be duplicated. Only an
		// Open failure — with no bytes written — falls back.
		rc, err := r.kubeLogs.Open(ctx, resource, opts)
		if err == nil {
			defer rc.Close()
			_, copyErr := io.Copy(outW, rc)
			return copyErr
		}
		if ctx.Err() != nil {
			return err // caller cancelled during setup (e.g. Ctrl-C); a clean stop
		}
		// Setup failed before any output; fall through to the server proxy. Warn
		// (not Debug) so the user knows their kubeconfig read was attempted and
		// why it failed — otherwise a later server-side RBAC error reads as
		// puzzling ("but I have cluster access"). If the proxy also fails, report
		// both attempts rather than the lone server error.
		r.driver().Warn("direct pod-log read for %s with your kubeconfig failed; falling back to the cornus server: %v", resource, err)
		if proxyErr := r.streamProxyServiceLogs(ctx, resource, opts, outW, errW); proxyErr != nil {
			return fmt.Errorf("direct pod-log read failed (%v); server fallback also failed: %w", err, proxyErr)
		}
		return nil
	}
	return r.streamProxyServiceLogs(ctx, resource, opts, outW, errW)
}

// streamProxyServiceLogs pulls one deployment's Docker raw-stream via the client and
// demultiplexes it into the stdout/stderr writers. The client copies the raw
// (stdcopy-framed) body into a pipe whose read end StdCopy splits; closing the
// read end on a StdCopy error unblocks the client goroutine.
func (r *runtime) streamProxyServiceLogs(ctx context.Context, resource string, opts api.LogOptions, outW, errW io.Writer) error {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(r.client.Logs(ctx, resource, opts, pw))
	}()
	_, err := stdcopy.StdCopy(outW, errW, pr)
	pr.CloseWithError(err)
	return err
}
