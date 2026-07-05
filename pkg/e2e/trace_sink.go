package e2e

import (
	"fmt"
	"net"
	"net/http"
	"sync"

	"go.starlark.net/starlark"
)

// traceSink is an in-process HTTP server the harness runs on the loopback of the
// harness host. It records the W3C `traceparent` header of every request it
// receives and answers 204 for any path/method. It exists so a scenario can point
// the real cornus CLI at it (as its --server) and then PROVE, end to end through
// the actual binary, that the client injects trace context into its server
// requests when telemetry is enabled — and injects nothing when it is off (the
// opt-in, zero-cost gate). This is the client-side half of the client <> server
// <> caretaker distributed trace.
//
// It deliberately does NOT forward to a real cornus server: answering 204 itself
// keeps a driving client call (e.g. `cornus deploy --delete`) independent of any
// deploy backend, so the scenario runs on every target — the same in-harness
// recording-endpoint pattern egress_proxy uses to observe the client's egress.
type traceSink struct {
	ln  net.Listener
	srv *http.Server
	mu  sync.Mutex
	// reqs holds one cloned header set per received request, in order, so a
	// scenario can inspect any propagation header (traceparent, baggage, ...).
	reqs []http.Header
}

func (s *traceSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, r.Header.Clone())
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// values returns the named header's value for each received request, in order
// ("" when a request carried no such header).
func (s *traceSink) values(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.reqs))
	for i, h := range s.reqs {
		out[i] = h.Get(name)
	}
	return out
}

// bTraceSink starts a trace-recording HTTP sink on the loopback and returns its
// "127.0.0.1:PORT" address for a scenario to pass as a --server URL.
func (h *Harness) bTraceSink(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("trace_sink", args, kwargs); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("trace_sink: listen: %w", err)
	}
	s := &traceSink{ln: ln}
	s.srv = &http.Server{Handler: s}
	go func() { _ = s.srv.Serve(ln) }()
	if h.traceSinks == nil {
		h.traceSinks = map[string]*traceSink{}
	}
	addr := ln.Addr().String()
	h.traceSinks[addr] = s
	h.logf("• trace_sink listening on http://%s", addr)
	return starlark.String(addr), nil
}

// bTraceSinkHeaders returns the list of values of the named header (default
// "traceparent") the trace_sink at addr recorded — one entry per request it
// received ("" when a request carried none) — so a scenario can assert
// telemetry-on injects a valid W3C traceparent / baggage and telemetry-off
// injects nothing.
func (h *Harness) bTraceSinkHeaders(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var addr string
	name := "traceparent"
	if err := starlark.UnpackArgs("trace_sink_headers", args, kwargs, "addr", &addr, "name?", &name); err != nil {
		return nil, err
	}
	s := h.traceSinks[addr]
	if s == nil {
		return nil, fmt.Errorf("trace_sink_headers: no trace_sink at %q", addr)
	}
	var elems []starlark.Value
	for _, v := range s.values(name) {
		elems = append(elems, starlark.String(v))
	}
	return starlark.NewList(elems), nil
}

// stopTraceSinks closes every trace_sink listener (unblocking its Serve loop) on
// scenario teardown.
func (h *Harness) stopTraceSinks() {
	for _, s := range h.traceSinks {
		_ = s.ln.Close()
	}
	h.traceSinks = nil
}
