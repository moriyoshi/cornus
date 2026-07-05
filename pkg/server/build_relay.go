package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"cornus/pkg/build/builderctr"
	"cornus/pkg/wire"
)

// builderAttachPath is the build-attach endpoint on the upstream builder.
const builderAttachPath = "/.cornus/v1/build/attach"

// builderAttachURL normalizes a configured builder base into its attach URL, or
// returns "" when no builder is configured (this server builds in-process). It
// accepts the same forms as the CLI's --builder: a bare "host:port", an http(s)
// URL, or a ws(s) URL, with or without the attach path already appended.
func builderAttachURL(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "ws://"), strings.HasPrefix(base, "wss://"):
	default:
		base = "ws://" + base
	}
	if !strings.Contains(base, builderAttachPath) {
		base = strings.TrimRight(base, "/") + builderAttachPath
	}
	return base
}

// resolveBuilder returns the upstream builder base URL this server should
// delegate builds to, or "" to build in-process.
//
// Resolution order:
//  1. An explicit --builder-url always wins.
//  2. Otherwise, when the in-process engine cannot run (no mount(2) — see
//     builderctr.CanMount) and auto-builder is enabled, start/adopt a managed
//     builder container.
//
// The auto path only engages where builds would otherwise be impossible, so it
// cannot change the behavior of a host that already builds successfully. It is
// resolved lazily, on the first build, so a server that never builds never
// starts a container — and the result (including failure) is cached, so a
// broken Docker setup is not retried on every request.
func (s *Server) resolveBuilder(ctx context.Context) (string, error) {
	if u := builderAttachURL(s.cfg.BuilderURL); u != "" {
		return u, nil
	}
	if !s.cfg.BuilderAuto || builderctr.CanMount() {
		return "", nil
	}

	s.builderMu.Lock()
	defer s.builderMu.Unlock()
	if s.builderDone {
		return s.builderURL, s.builderErr
	}
	s.builderDone = true

	// "using" rather than "starting": Ensure adopts an already-running builder
	// (the common case after a server restart) as readily as it starts one.
	slog.Info("build engine cannot mount(2) as this user; using a containerized builder",
		"container", builderctr.DefaultName)
	// The builder must export the way THIS server would have. Build sessions are
	// relayed verbatim, so the builder decides how to deliver the result; if our
	// registry re-exports the local Docker daemon it is read-only, and a builder
	// left to its own devices would push at it and get a 405.
	if s.registrySource == registrySourceContainerd {
		s.builderErr = fmt.Errorf("this server cannot build (mount(2) is not permitted for uid %d) and "+
			"its registry re-exports the host containerd store, which a containerized builder cannot write to; "+
			"run cornus privileged, set --storage, or point --builder-url at a builder you manage", os.Geteuid())
		return "", s.builderErr
	}
	base, err := builderctr.Ensure(ctx, builderctr.Options{
		Image:        s.cfg.BuilderImage,
		BaseImage:    s.cfg.BuilderBaseImage,
		DockerExport: s.registrySource == registrySourceDockerDaemon,
	})
	if err != nil {
		s.builderErr = fmt.Errorf("this server cannot build (mount(2) is not permitted for uid %d) and starting a containerized builder failed: %w; run cornus privileged, or point --builder-url at a builder", os.Geteuid(), err)
		slog.Error("containerized builder unavailable", "err", err)
		return "", s.builderErr
	}
	s.builderURL = builderAttachURL(base)
	slog.Info("delegating builds to containerized builder", "url", base)
	return s.builderURL, nil
}

// builderHTTPBase normalizes a configured builder base into an http(s) origin
// (no path), or "" when no builder is configured. It is the POST /.cornus/v1/build
// counterpart of builderAttachURL, which yields the ws(s) attach URL.
func builderHTTPBase(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	base = strings.TrimSuffix(base, builderAttachPath)
	switch {
	case strings.HasPrefix(base, "ws://"):
		base = "http://" + strings.TrimPrefix(base, "ws://")
	case strings.HasPrefix(base, "wss://"):
		base = "https://" + strings.TrimPrefix(base, "wss://")
	case strings.HasPrefix(base, "http://"), strings.HasPrefix(base, "https://"):
	default:
		base = "http://" + base
	}
	return strings.TrimRight(base, "/")
}

// relayBuildPost forwards POST /.cornus/v1/build (the tar-upload build path) to the
// upstream builder and streams its progress response back verbatim.
//
// Unlike the attach path this cannot be a raw splice: it is a plain HTTP request,
// so the context tar body and the query string (target, build args) are forwarded
// as-is and the streaming plain-text response is copied back with a flush per
// chunk, preserving the caller's live progress output. The caller's bearer token
// is passed through so an authenticated builder still authorizes the build.
func (s *Server) relayBuildPost(w http.ResponseWriter, r *http.Request, base string) {
	out := base + "/.cornus/v1/build"
	if r.URL.RawQuery != "" {
		out += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, out, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	// No client timeout: a build legitimately runs for minutes; the request
	// context still cancels it when the caller disconnects.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "builder unavailable: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	_, _ = io.Copy(&flushWriter{w: w, f: flusher}, resp.Body)
}

// relayBuildAttach splices the caller's build-attach WebSocket to an upstream
// builder, byte for byte.
//
// The whole buildwire protocol — the yamux session, the control stream carrying
// the BuildSpec and progress events, and the 9P stream exporting the caller's
// context, named contexts, and secrets — rides over that one WebSocket. Relaying
// the RAW connection (before wire.Accept's yamux handshake) therefore forwards
// all of them transparently: the builder terminates 9P against the caller's own
// export, so the caller's files never land on this host, and this server needs no
// 9P proxy of its own.
//
// Authorization is enforced by handleBuildAttach before the upgrade, so a denied
// identity never reaches the builder.
func (s *Server) relayBuildAttach(w http.ResponseWriter, r *http.Request, upstream string) {
	down, err := wire.AcceptConn(w, r)
	if err != nil {
		// Already hijacked on most failures; nothing useful to write back.
		return
	}
	defer down.Close()

	up, err := wire.DialConn(r.Context(), upstream)
	if err != nil {
		// The connection is hijacked, so this cannot be reported as HTTP; the
		// caller surfaces it as a control-stream failure.
		return
	}
	defer up.Close()

	wire.Pipe(down, up)
}
