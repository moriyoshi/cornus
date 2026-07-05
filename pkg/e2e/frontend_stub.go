package e2e

import (
	"fmt"
	"net"
	"net/http"

	"go.starlark.net/starlark"
)

// frontendStubBody is the sentinel HTML a frontend_stub serves for every path.
// A scenario asserts a detached-mode `cornus web --frontend <stub>` proxies GET /
// through to it (the body appears) while the BFF stays served at the same origin.
const frontendStubBody = "<!doctype html><title>frontend-stub</title>FRONTEND-STUB"

// frontendStub is an in-process HTTP server on the harness loopback that stands
// in for a separately-run frontend dev server (Vite). It answers any path/method
// with frontendStubBody. It exists so the harness can exercise `cornus web`'s
// detached-frontend reverse-proxy path end to end WITHOUT shipping node/Vite into
// CI — the same in-harness recording/serving-endpoint pattern trace_sink and
// egress_proxy use. The real Vite dev server is the developer's manual
// `cornus web --frontend http://localhost:5173`.
type frontendStub struct {
	ln  net.Listener
	srv *http.Server
}

func (s *frontendStub) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(frontendStubBody))
}

// bFrontendStub starts a stub frontend dev server on the loopback and returns its
// "127.0.0.1:PORT" address, for a scenario to pass as web(frontend=...).
func (h *Harness) bFrontendStub(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("frontend_stub", args, kwargs); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("frontend_stub: listen: %w", err)
	}
	s := &frontendStub{ln: ln}
	s.srv = &http.Server{Handler: s}
	go func() { _ = s.srv.Serve(ln) }()
	if h.frontendStubs == nil {
		h.frontendStubs = map[string]*frontendStub{}
	}
	addr := ln.Addr().String()
	h.frontendStubs[addr] = s
	h.logf("• frontend_stub listening on http://%s", addr)
	return starlark.String(addr), nil
}

// stopFrontendStubs closes every frontend_stub listener on scenario teardown.
func (h *Harness) stopFrontendStubs() {
	for _, s := range h.frontendStubs {
		_ = s.ln.Close()
	}
	h.frontendStubs = nil
}
