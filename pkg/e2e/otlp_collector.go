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

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// otlpCollector is an in-process OTLP/HTTP trace receiver the harness runs on the
// loopback of the harness host. Unlike trace_sink (which only records the
// injected request headers), this decodes the exported spans themselves, so a
// scenario can point BOTH a served cornus and a client cornus at it (via the
// standard OTEL_EXPORTER_OTLP_ENDPOINT env) and then PROVE the full distributed
// trace: that a client span and a REAL server span share one trace id and the
// server span is a child of the client's. It answers only the traces signal
// (/v1/traces); metrics/logs should be exporter=none in the scenario.
type otlpCollector struct {
	ln    net.Listener
	srv   *http.Server
	mu    sync.Mutex
	spans []collectedSpan
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
	if r.URL.Path == "/v1/traces" {
		var req coltrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err == nil {
			c.ingest(&req)
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

// stopOTLPCollectors closes every otlp_collector listener (unblocking its Serve
// loop) on scenario teardown.
func (h *Harness) stopOTLPCollectors() {
	for _, c := range h.otlpCollectors {
		_ = c.ln.Close()
	}
	h.otlpCollectors = nil
}
