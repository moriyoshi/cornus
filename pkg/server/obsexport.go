package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"cornus/pkg/logging"
)

// Re-export: forwarding what the server received on to a real OTLP backend.
//
// The store answers "what happened here, recently". An organization's telemetry
// backend answers "what happened across everything, for as long as we keep it".
// Those are different questions, and making the user pick one was the wrong
// trade — so the server can do both: ingest into the local store AND forward the
// same bytes upstream.
//
// What that buys, beyond keeping both answers:
//
//   - Workloads stop needing credentials for the upstream. They export to cornus,
//     which already authenticates them; the backend token lives in one place
//     instead of in every deploy spec.
//   - Workloads stop needing a route to the upstream. Combined with telemetry
//     over the caretaker mux, a workload with no egress at all can still land
//     spans in a SaaS backend.
//   - The upstream is configured once, on the server, rather than per workload.
//
// # Never in the ingest path
//
// The forwarder is asynchronous and bounded, and that is the whole design
// constraint. A slow or wedged upstream must not slow ingest, must not grow the
// heap without limit, and must not turn "cornus can't reach Honeycomb" into
// "cornus stops recording". So the queue is fixed-size, a full queue DROPS,
// and drops are counted rather than silently absorbed — an operator can see the
// forwarder falling behind in `cornus observe status`.
//
// This is deliberately the opposite trade from the OTLP receiver's own
// backpressure, which answers 429 and asks the sender to retry. There the sender
// still holds the data; here cornus has already accepted it, and blocking to
// preserve a copy would penalize the workload for the backend's outage.

const (
	// obsExportQueue bounds outstanding export bodies. Sized to absorb a burst
	// or a brief upstream stall, not an outage.
	obsExportQueue = 256
	// obsExportTimeout bounds one upstream request.
	obsExportTimeout = 30 * time.Second
)

// obsExportConfig is the resolved upstream. A zero Endpoint means re-export is
// off, which is the default.
type obsExportConfig struct {
	Endpoint string
	Headers  map[string]string
	Insecure bool
}

// Active reports whether re-export is configured.
func (c obsExportConfig) Active() bool { return strings.TrimSpace(c.Endpoint) != "" }

// obsExporter forwards received OTLP payloads to an upstream backend.
type obsExporter struct {
	cfg    obsExportConfig
	client *http.Client
	queue  chan obsExportItem

	sent    atomic.Int64
	dropped atomic.Int64
	failed  atomic.Int64
}

type obsExportItem struct {
	signal string // "logs" | "traces" | "metrics"
	body   []byte
}

// newObsExporter builds a forwarder, or returns nil when re-export is off. A nil
// *obsExporter is a working no-op: every method tolerates it, so callers never
// branch on whether the feature is configured.
func newObsExporter(ctx context.Context, cfg obsExportConfig) *obsExporter {
	if !cfg.Active() {
		return nil
	}
	e := &obsExporter{
		cfg:    cfg,
		client: &http.Client{Timeout: obsExportTimeout},
		queue:  make(chan obsExportItem, obsExportQueue),
	}
	if cfg.Insecure {
		e.client.Transport = insecureTransport()
	}
	logging.FromContext(ctx).InfoContext(ctx, "observability re-export enabled",
		"endpoint", cfg.Endpoint, "headers", len(cfg.Headers))
	return e
}

// Enqueue hands a received payload to the forwarder. It never blocks: a full
// queue drops the payload and bumps the counter, because the alternative is
// letting a wedged backend become the server's problem.
//
// The body is retained as-is rather than copied. Callers pass the buffer they
// read off the wire and do not reuse it, which is what makes that safe.
func (e *obsExporter) Enqueue(signal string, body []byte) {
	if e == nil || len(body) == 0 {
		return
	}
	select {
	case e.queue <- obsExportItem{signal: signal, body: body}:
	default:
		e.dropped.Add(1)
	}
}

// Serve drains the queue until ctx is cancelled. It satisfies supervisor.Service.
//
// One worker, deliberately: OTLP export requests to a single backend are
// order-insensitive but not free, and a pool would mostly buy the ability to
// overwhelm the upstream faster. The queue, not concurrency, is what absorbs
// bursts.
func (e *obsExporter) Serve(ctx context.Context) error {
	if e == nil {
		<-ctx.Done()
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case item := <-e.queue:
			e.send(ctx, item)
		}
	}
}

// send performs one upstream export, with a single retry.
//
// One retry, not many: the queue behind this is bounded, so a long retry ladder
// converts a transient upstream blip into dropped payloads further back. Retry
// once for the errors that are plausibly transient, then give up and count it.
func (e *obsExporter) send(ctx context.Context, item obsExportItem) {
	url := strings.TrimSuffix(e.cfg.Endpoint, "/") + "/v1/" + item.signal
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		retryable, err := e.post(ctx, url, item.body)
		if err == nil {
			e.sent.Add(1)
			return
		}
		if !retryable || ctx.Err() != nil {
			break
		}
	}
	e.failed.Add(1)
}

// post issues one export request, reporting whether a failure is worth retrying.
func (e *obsExporter) post(ctx context.Context, url string, body []byte) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, err // a malformed endpoint will not fix itself
	}
	req.Header.Set("Content-Type", protobufContentType)
	for k, v := range e.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return true, err // connection-level: plausibly transient
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return false, nil
	}
	// 429 and 5xx are the backend asking for (or deserving) another try; a 4xx
	// means this payload will never be accepted, so retrying wastes the queue.
	retryable = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	return retryable, fmt.Errorf("upstream returned %s", resp.Status)
}

// Stats reports the forwarder's counters for `observe status`.
func (e *obsExporter) Stats() (sent, dropped, failed int64) {
	if e == nil {
		return 0, 0, 0
	}
	return e.sent.Load(), e.dropped.Load(), e.failed.Load()
}

// insecureTransport disables verification for an upstream with a private or
// self-signed certificate. It is only ever reached through an explicit
// `--obs-export-insecure`, so there is no silent downgrade.
func insecureTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	return t
}
