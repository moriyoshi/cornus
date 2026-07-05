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
	// No per-command --no-color: cornus already has a GLOBAL --no-color (see
	// main.go), and kong makes a root flag available on every subcommand, so
	// `cornus compose logs --no-color` parses and takes effect exactly as docker
	// compose's per-command flag does. Redeclaring it here is a duplicate-flag
	// error at parse-tree construction, not an override.
	//
	// Index selects ONE replica of a scaled service, 1-based like docker
	// compose's --index. 0 means unset (stream the first instance, as before).
	Index int `kong:"name='index',default='0',help='Stream only this replica of each selected service (1-based), for a service scaled to several instances. Mutually exclusive with --all-replicas.'"`

	// Source selection. The store is a superset of the runtime for anything it
	// has recorded, but it is not always present, so the source is explicit
	// rather than magic.
	From     string `kong:"name='from',default='auto',enum='auto,runtime,store',help='Where to read logs from: auto (the live runtime, falling back to the recorded store when the workload is gone), runtime (only the live container output), or store (only the observability store, which survives the container).'"`
	Match    string `kong:"name='match',help='Only show lines matching this text. Implies --from=store: a live container stream cannot be searched.'"`
	Severity string `kong:"name='severity',help='Only show records at or above this level (debug, info, warn, error, fatal). Implies --from=store.'"`
	// AllReplicas fans a scaled service's every instance into the output. Off by
	// default because a single-replica service — the common case — would otherwise
	// pay a status lookup per service to learn it has exactly one.
	AllReplicas bool `kong:"name='all-replicas',help='Stream every instance of a scaled service, not just the first. Each line is tagged with its replica ordinal.'"`
}

// logSource names where a `compose logs` invocation reads from.
type logSource string

const (
	sourceAuto    logSource = "auto"
	sourceRuntime logSource = "runtime"
	sourceStore   logSource = "store"
)

// storeOnly reports whether the invocation asked for something only the
// observability store can do. Searching and level-filtering are not options a
// live container stream has — the runtime hands over bytes, not records — so
// these flags select the source rather than conflicting with it.
func (c *LogsCmd) storeOnly() bool {
	return c.Match != "" || c.Severity != ""
}

// Run streams logs for the selected services. Every service streams
// concurrently (as Docker Compose does), so with --follow all services are
// tailed together until Ctrl-C.
func (c *LogsCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	if err := c.validateIndex(); err != nil {
		return err
	}
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
	if c.Index > 0 {
		// Backends address replicas by ordinal (`<name>-<i>`) and LogOptions.Instance
		// already selects one, so --index is a straight translation from docker's
		// 1-based index to the 0-based ordinal. Checked against the live instance
		// count first: an out-of-range index is otherwise an ErrNotFound from the
		// backend, which reads like the SERVICE is missing rather than the replica.
		for _, n := range names {
			if got := rt.replicaCount(ctx, rt.plans[n].Resource); c.Index > got {
				return fmt.Errorf("--index %d: service %q has %d replica(s), so the valid indexes are 1..%d", c.Index, n, got, got)
			}
		}
		opts.Instance = c.Index - 1
	}
	return c.run(ctx, rt, names, opts, d)
}

// validateIndex rejects an --index that cannot mean anything, before any
// connection is made. Docker's --index is 1-based, so 0 is "unset" and a
// negative value is a typo; and asking for one replica while also asking for
// all of them is a contradiction the CLI should name rather than resolve.
func (c *LogsCmd) validateIndex() error {
	if c.Index < 0 {
		return fmt.Errorf("--index is 1-based and must be at least 1 (got %d)", c.Index)
	}
	if c.Index > 0 && c.AllReplicas {
		return fmt.Errorf("--index selects one replica and --all-replicas streams every replica; pass only one")
	}
	if c.Index > 0 && c.storeOnly() {
		return fmt.Errorf("--index selects a live replica, but --match/--severity read the recorded store, which is not per-replica; drop --index or the filter")
	}
	return nil
}

// run dispatches to the selected source.
//
// The dispatch is deliberately boring, because the interesting property is what
// it refuses to do: `--from=auto` never returns FEWER lines than `--from=runtime`
// would have. It reads the runtime first and only consults the store when the
// runtime produced nothing and failed — which is what "the container is gone"
// looks like. A fallback that could fire after partial output would duplicate
// lines, so it is gated on zero bytes written, not on the error alone.
func (c *LogsCmd) run(ctx context.Context, rt *runtime, names []string, opts api.LogOptions, d *cliout.Driver) error {
	prefix := !c.NoLogPrefix
	src := logSource(c.From)

	// A search or level filter is a store request however --from was spelled;
	// an explicit --from=runtime alongside one is a contradiction worth naming
	// rather than silently resolving.
	if c.storeOnly() {
		if src == sourceRuntime {
			return fmt.Errorf("--match/--severity search recorded logs and cannot apply to --from=runtime; drop --from=runtime or the filter")
		}
		src = sourceStore
	}

	// Selecting one replica is a live-runtime operation too: the observability
	// store records a service, not an instance, so it cannot answer "only replica
	// 2". auto resolves to the runtime (which can); an explicit --from=store is a
	// contradiction, named rather than silently answered with every replica's
	// recorded output.
	if c.Index > 0 {
		if src == sourceStore {
			return fmt.Errorf("--index selects a live replica and cannot read from --from=store (recorded logs are per service, not per replica); drop --index or --from=store")
		}
		src = sourceRuntime
	}

	// Following is inherently a live-runtime operation: the store is a record of
	// what already happened, and tailing it would lag by the recorder's flush
	// interval while adding nothing.
	if opts.Follow && src == sourceAuto {
		src = sourceRuntime
	}
	if opts.Follow && src == sourceStore {
		return fmt.Errorf("--follow tails the live container and cannot read from --from=store; drop --follow to read recorded logs")
	}

	switch src {
	case sourceRuntime:
		return rt.streamLogsReplicas(ctx, names, opts, prefix, c.AllReplicas, d.Out(), d.Err())
	case sourceStore:
		return c.storeLogs(ctx, rt, names, opts, prefix, d)
	}

	// auto: the runtime first, the store only if the runtime had nothing to say.
	counted := &countingWriter{w: d.Out()}
	countedErr := &countingWriter{w: d.Err()}
	err := rt.streamLogsReplicas(ctx, names, opts, prefix, c.AllReplicas, counted, countedErr)
	if err == nil || ctx.Err() != nil {
		return err
	}
	if counted.n > 0 || countedErr.n > 0 {
		return err // output already began; falling back now would duplicate it
	}
	if storeErr := c.storeLogs(ctx, rt, names, opts, prefix, d); storeErr != nil {
		// Report the runtime's failure, which is the one the user was actually
		// asking about; the store was a best-effort second try.
		return err
	}
	return nil
}

// countingWriter counts bytes so the auto fallback can tell "the runtime said
// nothing and failed" from "the runtime was partway through and then failed".
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// streamLogs streams the given services' logs concurrently, demultiplexing each
// service's Docker raw-stream into stdout/stderr. When prefix is set, every line
// is tagged with its (width-padded) service name, matching `docker compose logs`.
// A shared mutex keeps concurrent writers from interleaving a partial line on
// the shared output. Returns the first non-cancellation error across services;
// context cancellation (Ctrl-C on a --follow) is treated as a clean stop.
func (r *runtime) streamLogs(ctx context.Context, names []string, opts api.LogOptions, prefix bool, stdout, stderr io.Writer) error {
	return r.streamLogsReplicas(ctx, names, opts, prefix, false, stdout, stderr)
}

// streamLogsReplicas is streamLogs with the replica fan-out switch.
func (r *runtime) streamLogsReplicas(ctx context.Context, names []string, opts api.LogOptions, prefix, allReplicas bool, stdout, stderr io.Writer) error {
	// All per-service writers share one LineGroup mutex, so concurrent services
	// never interleave a partial line on the shared output — and json mode wraps
	// each line as an NDJSON log object.
	group := r.driver().LineGroup()
	width := 0
	if prefix {
		for _, n := range names {
			w := len(n)
			if allReplicas {
				w += 3 // room for the "[N]" ordinal tag
			}
			if w > width {
				width = w
			}
		}
	}

	// One stream per (service, replica). replicaCount is 1 unless the caller asked
	// for every instance, so the single-replica common case behaves exactly as
	// before and costs no extra lookups.
	type target struct {
		service  string
		resource string
		replica  int
		replicas int
	}
	var targets []target
	for _, name := range names {
		resource := r.plans[name].Resource
		if !allReplicas {
			// One stream, at the caller's chosen ordinal: 0 (the first instance) for
			// every caller that does not care, and opts.Instance-1 when `logs --index`
			// picked a replica. replicas is 1 so the prefix stays untagged — the user
			// named the instance, so labelling every line with it adds nothing.
			targets = append(targets, target{service: name, resource: resource, replica: opts.Instance, replicas: 1})
			continue
		}
		n := r.replicaCount(ctx, resource)
		for i := 0; i < n; i++ {
			targets = append(targets, target{service: name, resource: resource, replica: i, replicas: n})
		}
	}

	var wg sync.WaitGroup
	var closers []io.WriteCloser
	errs := make([]error, len(targets))
	for i, t := range targets {
		p := ""
		if prefix {
			label := t.service
			// Only tag the ordinal when there is more than one, so a
			// single-replica service's output is unchanged.
			if t.replicas > 1 {
				label = fmt.Sprintf("%s[%d]", t.service, t.replica)
			}
			p = fmt.Sprintf("%-*s | ", width, label)
		}
		outW := group.Writer(stdout, p)
		errW := group.Writer(stderr, p)
		closers = append(closers, outW, errW)
		o := opts
		o.Instance = t.replica
		wg.Add(1)
		go func(i int, resource string, o api.LogOptions, outW, errW io.Writer) {
			defer wg.Done()
			errs[i] = r.streamServiceLogs(ctx, resource, o, outW, errW)
		}(i, t.resource, o, outW, errW)
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
