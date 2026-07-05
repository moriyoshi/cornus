package e2e

import (
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"go.starlark.net/starlark"
	"google.golang.org/protobuf/proto"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// otlpCollector is an in-process OTLP/HTTP trace receiver the harness runs on the
// loopback of the harness host. Unlike trace_sink (which only records the
// injected request headers), this decodes the exported spans themselves, so a
// scenario can point BOTH a served cornus and a client cornus at it (via the
// standard OTEL_EXPORTER_OTLP_ENDPOINT env) and then PROVE the full distributed
// trace: that a client span and a REAL server span share one trace id and the
// server span is a child of the client's.
//
// It also decodes the LOGS signal (/v1/logs), which is what makes it usable as
// the upstream for cornus's own re-export: the built-in store's zero-touch
// recorders produce log records and metric datapoints, so a scenario proving
// "cornus forwarded what it received" has to be able to read them back.
type otlpCollector struct {
	ln      net.Listener
	srv     *http.Server
	mu      sync.Mutex
	spans   []collectedSpan
	logs    []collectedLog
	metrics []collectedMetric
}

// collectedMetric is the harness-facing projection of one exported datapoint.
//
// Datapoint attributes are carried as a flat map rather than being dropped like
// the log projection drops record attributes: for metrics they ARE the identity
// of the series (which replica, which interface, which direction), so a scenario
// that cannot see them cannot tell three replicas from one replica sampled three
// times — the exact defect this feed exists to avoid.
type collectedMetric struct {
	name    string
	unit    string
	service string
	value   float64
	attrs   map[string]string
}

// collectedLog is the harness-facing projection of one exported log record.
type collectedLog struct {
	body     string
	service  string // service.name from the owning ResourceLogs resource
	severity string
}

// collectedSpan is the harness-facing projection of one exported span.
type collectedSpan struct {
	traceID  string
	spanID   string
	parentID string
	name     string
	service  string // service.name from the owning ResourceSpans resource
}

func (c *otlpCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := readOTLPBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case "/v1/traces":
		var req coltrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err == nil {
			c.ingest(&req)
		}
	case "/v1/logs":
		var req collogs.ExportLogsServiceRequest
		if err := proto.Unmarshal(body, &req); err == nil {
			c.ingestLogs(&req)
		}
	case "/v1/metrics":
		var req colmetrics.ExportMetricsServiceRequest
		if err := proto.Unmarshal(body, &req); err == nil {
			c.ingestMetrics(&req)
		}
	}
	// OTLP/HTTP success: a 200 with an (empty) protobuf ExportTraceServiceResponse.
	// Empty body is a valid response and keeps the exporter from retry-spamming.
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

// readOTLPBody reads the request body, transparently gunzipping it when the
// exporter used gzip content-encoding.
func readOTLPBody(r *http.Request) ([]byte, error) {
	var rd io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		rd = gz
	}
	return io.ReadAll(rd)
}

func (c *otlpCollector) ingest(req *coltrace.ExportTraceServiceRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rs := range req.GetResourceSpans() {
		service := ""
		if rs.GetResource() != nil {
			service = attrString(rs.GetResource().GetAttributes(), "service.name")
		}
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				c.spans = append(c.spans, collectedSpan{
					traceID:  hex.EncodeToString(sp.GetTraceId()),
					spanID:   hex.EncodeToString(sp.GetSpanId()),
					parentID: hex.EncodeToString(sp.GetParentSpanId()),
					name:     sp.GetName(),
					service:  service,
				})
			}
		}
	}
}

// ingestLogs records every log record in an export request.
func (c *otlpCollector) ingestLogs(req *collogs.ExportLogsServiceRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rl := range req.GetResourceLogs() {
		service := ""
		if rl.GetResource() != nil {
			service = attrString(rl.GetResource().GetAttributes(), "service.name")
		}
		for _, sl := range rl.GetScopeLogs() {
			for _, rec := range sl.GetLogRecords() {
				c.logs = append(c.logs, collectedLog{
					body:     rec.GetBody().GetStringValue(),
					service:  service,
					severity: rec.GetSeverityText(),
				})
			}
		}
	}
}

// ingestMetrics records every numeric datapoint in an export request.
//
// Only gauges and sums are projected: those are the shapes cornus's own
// collectors emit, and a histogram has no single "value" to assert on anyway.
func (c *otlpCollector) ingestMetrics(req *colmetrics.ExportMetricsServiceRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rm := range req.GetResourceMetrics() {
		service := ""
		if rm.GetResource() != nil {
			service = attrString(rm.GetResource().GetAttributes(), "service.name")
		}
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				var pts []*metricspb.NumberDataPoint
				switch {
				case m.GetGauge() != nil:
					pts = m.GetGauge().GetDataPoints()
				case m.GetSum() != nil:
					pts = m.GetSum().GetDataPoints()
				}
				for _, p := range pts {
					c.metrics = append(c.metrics, collectedMetric{
						name:    m.GetName(),
						unit:    m.GetUnit(),
						service: service,
						// OTLP carries a datapoint as either an int or a double;
						// the harness wants one comparable number, and a float
						// holds every counter cornus emits without loss.
						value: numberValue(p),
						attrs: attrMap(p.GetAttributes()),
					})
				}
			}
		}
	}
}

func numberValue(p *metricspb.NumberDataPoint) float64 {
	if p.GetAsInt() != 0 {
		return float64(p.GetAsInt())
	}
	return p.GetAsDouble()
}

// attrMap flattens datapoint attributes to strings, rendering non-string values
// through their natural spelling so a scenario can compare them literally.
func attrMap(attrs []*commonpb.KeyValue) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		v := kv.GetValue()
		if s := v.GetStringValue(); s != "" {
			out[kv.GetKey()] = s
			continue
		}
		out[kv.GetKey()] = fmt.Sprint(v.GetIntValue())
	}
	return out
}

func (c *otlpCollector) snapshotMetrics() []collectedMetric {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]collectedMetric(nil), c.metrics...)
}

// attrString returns the string value of the named resource attribute, or "".
func attrString(attrs []*commonpb.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv.GetKey() == key {
			return kv.GetValue().GetStringValue()
		}
	}
	return ""
}

func (c *otlpCollector) snapshot() []collectedSpan {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]collectedSpan(nil), c.spans...)
}

func (c *otlpCollector) snapshotLogs() []collectedLog {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]collectedLog(nil), c.logs...)
}

// bOTLPCollector starts an OTLP/HTTP trace receiver on the loopback and returns
// its "127.0.0.1:PORT" address for a scenario to pass as OTEL_EXPORTER_OTLP_ENDPOINT
// (as "http://<addr>").
func (h *Harness) bOTLPCollector(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("otlp_collector", args, kwargs); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("otlp_collector: listen: %w", err)
	}
	c := &otlpCollector{ln: ln}
	c.srv = &http.Server{Handler: c}
	go func() { _ = c.srv.Serve(ln) }()
	if h.otlpCollectors == nil {
		h.otlpCollectors = map[string]*otlpCollector{}
	}
	addr := ln.Addr().String()
	h.otlpCollectors[addr] = c
	h.logf("• otlp_collector listening on http://%s", addr)
	return starlark.String(addr), nil
}

// bOTLPSpans returns ALL spans the otlp_collector at addr has received, each as a
// dict {trace_id, span_id, parent_span_id, name, service}. It polls until at
// least min spans have arrived (default 0) or timeout (default "15s") elapses.
// The optional service filter scopes the min count to spans from that
// service.name — so a scenario can wait specifically for the server ("cornus")
// span, which arrives asynchronously on the server's batch schedule and would
// otherwise be raced by the client spans (which flush eagerly on CLI exit). The
// returned list is always unfiltered so the caller can correlate across services.
func (h *Harness) bOTLPSpans(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var addr, service string
	minSpans := 0
	timeout := "15s"
	if err := starlark.UnpackArgs("otlp_spans", args, kwargs, "addr", &addr, "min?", &minSpans, "timeout?", &timeout, "service?", &service); err != nil {
		return nil, err
	}
	c := h.otlpCollectors[addr]
	if c == nil {
		return nil, fmt.Errorf("otlp_spans: no otlp_collector at %q", addr)
	}
	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, fmt.Errorf("otlp_spans: timeout: %w", err)
	}
	deadline := time.Now().Add(dur)
	var spans []collectedSpan
	for {
		spans = c.snapshot()
		have := len(spans)
		if service != "" {
			have = 0
			for _, s := range spans {
				if s.service == service {
					have++
				}
			}
		}
		if have >= minSpans || time.Now().After(deadline) {
			break
		}
		select {
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	elems := make([]starlark.Value, len(spans))
	for i, s := range spans {
		elems[i] = anyDict(map[string]any{
			"trace_id":       s.traceID,
			"span_id":        s.spanID,
			"parent_span_id": s.parentID,
			"name":           s.name,
			"service":        s.service,
		})
	}
	return starlark.NewList(elems), nil
}

// bOTLPLogs returns ALL log records the otlp_collector at addr has received, each
// as a dict {body, service, severity}. Like otlp_spans it polls until at least
// min records have arrived (default 0) or timeout (default "15s") elapses,
// optionally scoping the min count to one service.
//
// It is the read side of cornus's re-export: a scenario asserts that what a
// workload printed reached not only the local store but the upstream backend.
func (h *Harness) bOTLPLogs(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var addr, service string
	minLogs := 0
	timeout := "15s"
	if err := starlark.UnpackArgs("otlp_logs", args, kwargs, "addr", &addr, "min?", &minLogs, "timeout?", &timeout, "service?", &service); err != nil {
		return nil, err
	}
	c := h.otlpCollectors[addr]
	if c == nil {
		return nil, fmt.Errorf("otlp_logs: no otlp_collector at %q", addr)
	}
	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, fmt.Errorf("otlp_logs: timeout: %w", err)
	}
	deadline := time.Now().Add(dur)
	var logs []collectedLog
	for {
		logs = c.snapshotLogs()
		have := len(logs)
		if service != "" {
			have = 0
			for _, l := range logs {
				if l.service == service {
					have++
				}
			}
		}
		if have >= minLogs || time.Now().After(deadline) {
			break
		}
		select {
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	elems := make([]starlark.Value, len(logs))
	for i, l := range logs {
		elems[i] = anyDict(map[string]any{
			"body":     l.body,
			"service":  l.service,
			"severity": l.severity,
		})
	}
	return starlark.NewList(elems), nil
}

// bOTLPMetrics returns ALL metric datapoints the otlp_collector at addr has
// received, each as a dict {name, unit, service, value, attributes}. Like
// otlp_logs it polls until at least min datapoints have arrived (default 0) or
// timeout (default "30s") elapses, optionally scoping the min count to one
// metric name.
//
// The default timeout is longer than otlp_logs': a log line is forwarded as soon
// as the workload prints it, while a metric datapoint has to wait for the next
// sampling tick and then for the re-export batch.
func (h *Harness) bOTLPMetrics(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var addr, name string
	minPoints := 0
	timeout := "30s"
	if err := starlark.UnpackArgs("otlp_metrics", args, kwargs, "addr", &addr, "min?", &minPoints, "timeout?", &timeout, "name?", &name); err != nil {
		return nil, err
	}
	c := h.otlpCollectors[addr]
	if c == nil {
		return nil, fmt.Errorf("otlp_metrics: no otlp_collector at %q", addr)
	}
	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, fmt.Errorf("otlp_metrics: timeout: %w", err)
	}
	deadline := time.Now().Add(dur)
	var pts []collectedMetric
	for {
		pts = c.snapshotMetrics()
		have := len(pts)
		if name != "" {
			have = 0
			for _, p := range pts {
				if p.name == name {
					have++
				}
			}
		}
		if have >= minPoints || time.Now().After(deadline) {
			break
		}
		select {
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	elems := make([]starlark.Value, len(pts))
	for i, p := range pts {
		attrs := map[string]any{}
		for k, v := range p.attrs {
			attrs[k] = v
		}
		elems[i] = anyDict(map[string]any{
			"name":       p.name,
			"unit":       p.unit,
			"service":    p.service,
			"value":      p.value,
			"attributes": anyDict(attrs),
		})
	}
	return starlark.NewList(elems), nil
}

// stopOTLPCollectors closes every otlp_collector listener (unblocking its Serve
// loop) on scenario teardown.
func (h *Harness) stopOTLPCollectors() {
	for _, c := range h.otlpCollectors {
		_ = c.ln.Close()
	}
	h.otlpCollectors = nil
}
