package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"unicode/utf8"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
)

// handleDeployCollection serves /.cornus/v1/deploy:
//
//	POST  -> apply a DeploySpec (create or recreate)
//	GET   -> list all managed deployments
//
// stampOriginSubject records the authenticated request identity as the
// deployment's origin Subject, overwriting whatever the client sent (a client
// cannot be trusted to attest its own verified identity). When the client
// supplied no origin at all but the request is authenticated, a minimal origin
// carrying just the subject is created; an unauthenticated request (empty
// subject) with no client origin leaves Origin nil.
func stampOriginSubject(spec *api.DeploySpec, subject string) {
	if spec.Origin == nil {
		if subject == "" {
			return
		}
		spec.Origin = &api.Origin{}
	}
	spec.Origin.Subject = subject
}

func (s *Server) handleDeployCollection(w http.ResponseWriter, r *http.Request) {
	// A POST applies a DeploySpec (the "deploy" action); gate it on the API policy
	// before touching the backend. GET (list) is a read and is not gated here.
	if r.Method == http.MethodPost && !s.apiPolicy.Allow(Identity(r), "deploy") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to deploy"})
		return
	}
	backend, err := s.getBackend()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "deploy backend unavailable: " + err.Error()})
		return
	}

	switch r.Method {
	case http.MethodPost:
		var spec api.DeploySpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid spec: " + err.Error()})
			return
		}
		stampOriginSubject(&spec, Identity(r))
		// Serialise same-name applies so concurrent POSTs of one deployment do
		// not race on the backend's delete-then-create; different names run
		// concurrently.
		lock := s.acquireDeployLock(spec.Name)
		defer s.releaseDeployLock(spec.Name, lock)
		var status api.DeployStatus
		err := s.traceDeploy(r.Context(), "apply", spec.Name, func(ctx context.Context) error {
			// A stateless (--detach) deploy with a relay egress mode has no client
			// session, so it must egress through the GATEWAY terminus (this server).
			// Inject the egress caretaker with a sessionless AttachEgress; a caretaker
			// gateway request is then gated by CORNUS_EGRESS_GATEWAY at relay time.
			if needsEgressRelay(spec.Egress) {
				st, e := s.applyDetachedEgress(ctx, backend, spec)
				status = st
				return e
			}
			var e error
			status, e = backend.Apply(ctx, spec)
			return e
		})
		if err != nil {
			// Log server-side too, not just return to the client: a stateless deploy's
			// apply failure (a raw `forbidden` from a missing RBAC grant, a wedged
			// dependent) was previously only visible in the CLIENT's output, so an
			// operator watching the server saw nothing. The permission preflight
			// (preflightBackend) warns proactively; this catches the reactive case.
			logging.FromContext(r.Context()).WarnContext(r.Context(), "deploy: apply failed",
				"deployment", spec.Name, "error", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, status)

	case http.MethodGet:
		var list []api.DeployStatus
		err := s.traceDeploy(r.Context(), "list", "", func(ctx context.Context) error {
			var e error
			list, e = backend.List(ctx)
			return e
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, list)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleDeployItem serves /.cornus/v1/deploy/{name} and /.cornus/v1/deploy/{name}/{action}:
//
//	GET    /.cornus/v1/deploy/{name}           -> status of one deployment
//	DELETE /.cornus/v1/deploy/{name}           -> remove one deployment
//	POST   /.cornus/v1/deploy/{name}/start     -> start instances
//	POST   /.cornus/v1/deploy/{name}/stop      -> stop instances
//	POST   /.cornus/v1/deploy/{name}/restart   -> restart instances
//	GET    /.cornus/v1/deploy/{name}/logs      -> stream instance logs
//	GET    /.cornus/v1/deploy/{name}/stats     -> stream instance stats
//	*      /.cornus/v1/deploy/{name}/archive   -> container archive (docker cp), GET/PUT/HEAD
func (s *Server) handleDeployItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/.cornus/v1/deploy/")
	name, action, hasAction := strings.Cut(rest, "/")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing deployment name"})
		return
	}
	// A DELETE of /.cornus/v1/deploy/{name} (no sub-action) removes a deployment (the
	// "deploy" action); gate it on the API policy before touching the backend.
	if r.Method == http.MethodDelete && !hasAction && !s.apiPolicy.Allow(Identity(r), "deploy") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to deploy"})
		return
	}
	backend, err := s.getBackend()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "deploy backend unavailable: " + err.Error()})
		return
	}

	if hasAction {
		// Operations that mutate a deployment require the "deploy" action under a
		// configured API policy; pure reads (logs, stats, status, and copy-out) do
		// not. Exec and interactive attach are gated on the dedicated "exec"
		// action, which "deploy" implies (apiPolicy.AllowExec) — existing
		// deploy-capable identities keep exec, and an admin can grant exec-ONLY
		// identities. Without these gates, a policy that restricts "deploy" could
		// be bypassed via start/stop/restart, exec, or attach.
		deny := ""
		switch action {
		case "logs", "stats":
			// pure reads: not gated
		case "archive":
			if r.Method == http.MethodPut && !s.apiPolicy.Allow(Identity(r), "deploy") {
				deny = "deploy" // copy-in writes; copy-out reads
			}
		case "exec", "attach", "exec-agent-channel":
			if !s.apiPolicy.AllowExec(Identity(r)) {
				deny = "exec"
			}
		default:
			if !s.apiPolicy.Allow(Identity(r), "deploy") {
				deny = "deploy"
			}
		}
		if deny != "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to " + deny})
			return
		}
		switch action {
		case "logs":
			s.handleDeployLogs(w, r, backend, name)
			return
		case "stats":
			s.handleDeployStats(w, r, backend, name)
			return
		case "archive":
			s.handleDeployArchive(w, r, backend, name)
			return
		case "exec":
			s.handleDeployExecCreate(w, r, backend, name)
			return
		case "exec-agent-channel":
			s.handleDeployExecAgentChannel(w, r, backend, name)
			return
		case "attach":
			s.handleDeployAttachStream(w, r, backend, name)
			return
		case "portforward":
			s.handleDeployPortForward(w, r, backend, name)
			return
		case "tunnel":
			s.handleDeployTunnel(w, r, backend, name)
			return
		}
		if purpose, ok := strings.CutPrefix(action, "tunnel/channel/"); ok {
			s.handleDeployTunnelChannel(w, r, backend, name, purpose)
			return
		}
		s.handleDeployAction(w, r, backend, name, action)
		return
	}

	switch r.Method {
	case http.MethodGet:
		var status api.DeployStatus
		err := s.traceDeploy(r.Context(), "status", name, func(ctx context.Context) error {
			var e error
			status, e = backend.Status(ctx, name)
			return e
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, status)

	case http.MethodDelete:
		// Take the same per-name lock as apply so a delete cannot interleave with
		// a concurrent apply of the same deployment.
		lock := s.acquireDeployLock(name)
		defer s.releaseDeployLock(name, lock)
		// A deleted deployment has nothing to tunnel to; tear its tunnel down too.
		s.tunnels.stop(name)
		if err := s.traceDeploy(r.Context(), "delete", name, func(ctx context.Context) error {
			return backend.Delete(ctx, name)
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleVolumeItem removes a named, project-scoped volume
// (DELETE /.cornus/v1/volume/{name}), backing `compose down --volumes`. It is gated on
// the same "deploy" API-policy action as deleting a deployment. Backends that
// cannot remove volumes (do not implement deploy.VolumeRemover) answer 501 so
// the client can report volume removal as unsupported rather than fail the down.
func (s *Server) handleVolumeItem(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/.cornus/v1/volume/")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing volume name"})
		return
	}
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.apiPolicy.Allow(Identity(r), "deploy") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to deploy"})
		return
	}
	backend, err := s.getBackend()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "deploy backend unavailable: " + err.Error()})
		return
	}
	remover, ok := backend.(deploy.VolumeRemover)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "deploy backend does not support removing volumes"})
		return
	}
	if err := s.traceDeploy(r.Context(), "volume-delete", name, func(ctx context.Context) error {
		return remover.RemoveVolume(ctx, name)
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeployLogs streams a deployment's logs (GET /.cornus/v1/deploy/{name}/logs).
// The response body is the backend's stdcopy-multiplexed frame stream; it is
// flushed per write so a follow reaches the client promptly.
func (s *Server) handleDeployLogs(w http.ResponseWriter, r *http.Request, backend deploy.Backend, name string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	q := r.URL.Query()
	opts := api.LogOptions{
		Follow:     q.Get("follow") == "1" || q.Get("follow") == "true",
		Stdout:     q.Get("stdout") == "1" || q.Get("stdout") == "true",
		Stderr:     q.Get("stderr") == "1" || q.Get("stderr") == "true",
		Timestamps: q.Get("timestamps") == "1" || q.Get("timestamps") == "true",
		Tail:       q.Get("tail"),
		Since:      q.Get("since"),
		Until:      q.Get("until"),
	}
	w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	// The 200 header is written lazily on the backend's first output byte, so a
	// backend error before any output still becomes a real error response
	// instead of an empty 200. Once the body has begun, a mid-stream error can
	// no longer change the status; it is surfaced out of band as the
	// X-Cornus-Stream-Error trailer (declared by lazyFlushWriter before the
	// headers commit) so the client can distinguish truncation from a clean EOF.
	lw := newLazyFlushWriter(w)
	if err := backend.Logs(r.Context(), name, opts, lw); err != nil {
		// Log server-side either way: a pre-output failure (RBAC denied on
		// pods/log, no pod, invalid since) is also reported to the client as a
		// real HTTP error, but the server log is where an operator looks when
		// "compose logs" fails puzzlingly. Mid-stream failures can only ride the
		// trailer, so the log is the primary record.
		logStreamHandlerErr(r, "logs", name, err)
		if !lw.wrote {
			writeJSON(w, streamErrStatus(err), map[string]string{"error": err.Error()})
			return
		}
		lw.setStreamError(err)
	}
}

// handleDeployStats streams a deployment's Docker-format container metrics
// (GET /.cornus/v1/deploy/{name}/stats). ?stream=0 yields a single stats object; the
// body is flushed per write so a live stream reaches the client promptly.
func (s *Server) handleDeployStats(w http.ResponseWriter, r *http.Request, backend deploy.Backend, name string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	q := r.URL.Query()
	// Default to streaming (docker's default); only ?stream=0/false disables it.
	stream := q.Get("stream") != "0" && q.Get("stream") != "false"
	w.Header().Set("Content-Type", "application/json")
	// As in handleDeployLogs: the 200 header is deferred until the backend's
	// first output byte so a pre-output backend error (e.g. the kubernetes
	// backend's "stats not supported") becomes a real error response. After the
	// body has begun, a mid-stream error cannot change the status; it rides the
	// X-Cornus-Stream-Error trailer instead.
	lw := newLazyFlushWriter(w)
	if err := backend.Stats(r.Context(), name, api.StatsOptions{Stream: stream}, lw); err != nil {
		logStreamHandlerErr(r, "stats", name, err)
		if !lw.wrote {
			writeJSON(w, streamErrStatus(err), map[string]string{"error": err.Error()})
			return
		}
		lw.setStreamError(err)
	}
}

// lazyFlushWriter defers WriteHeader(http.StatusOK) — and thus the committed
// response status — until the first Write, then flushes after every write so a
// follow stream reaches the client promptly. It lets a streaming handler turn a
// backend error that happens before any output into a proper error response
// instead of an empty 200.
//
// On the first Write, just before the headers commit, it also declares the
// X-Cornus-Stream-Error response TRAILER: once the 200 is out a mid-stream
// backend error can no longer change the status, so setStreamError carries it
// out of band in the trailer instead of dropping it. The per-write Flush keeps
// the response chunked (never Content-Length-buffered), which is what makes
// HTTP/1.1 trailers deliverable. Error responses on the pre-first-byte path
// never declare the trailer (the declaration lives inside Write).
type lazyFlushWriter struct {
	w     http.ResponseWriter
	f     http.Flusher // nil when w cannot flush
	wrote bool         // true once the 200 header (and first bytes) are out
}

func newLazyFlushWriter(w http.ResponseWriter) *lazyFlushWriter {
	lw := &lazyFlushWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		lw.f = f
	}
	return lw
}

func (lw *lazyFlushWriter) Write(p []byte) (int, error) {
	if !lw.wrote {
		lw.wrote = true
		// Declare the mid-stream error trailer while headers can still change;
		// net/http only transmits trailers that were announced (or use the
		// TrailerPrefix escape hatch) before WriteHeader.
		lw.w.Header().Set("Trailer", api.StreamErrorTrailer)
		lw.w.WriteHeader(http.StatusOK)
	}
	n, err := lw.w.Write(p)
	if lw.f != nil {
		lw.f.Flush()
	}
	return n, err
}

// setStreamError records a mid-stream backend error as the declared
// X-Cornus-Stream-Error trailer value (mutating w.Header() after the body has
// begun is net/http's mechanism for setting a pre-declared trailer). It is a
// no-op before the first Write — on that path the handler still owns the
// status and must send a real error response instead.
func (lw *lazyFlushWriter) setStreamError(err error) {
	if lw.wrote {
		lw.w.Header().Set(api.StreamErrorTrailer, sanitizeStreamError(err))
	}
}

// maxStreamErrorLen caps the trailer value so a pathological backend error
// cannot bloat the response tail.
const maxStreamErrorLen = 1024

// sanitizeStreamError flattens a backend error into a single-line,
// length-capped HTTP field value: CR/LF and other control bytes become spaces
// (a header/trailer value must not contain them), and the result is trimmed
// and truncated. A message that sanitizes to nothing yields a fixed
// placeholder so the trailer still signals the failure.
func sanitizeStreamError(err error) string {
	msg := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, err.Error())
	msg = strings.TrimSpace(msg)
	if len(msg) > maxStreamErrorLen {
		cut := maxStreamErrorLen
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut-- // do not split a multi-byte rune at the cap
		}
		msg = strings.TrimSpace(msg[:cut])
	}
	if msg == "" {
		return "stream error"
	}
	return msg
}

// Flush implements http.Flusher. Before the first Write it is a no-op so it
// cannot commit the 200 status prematurely.
func (lw *lazyFlushWriter) Flush() {
	if lw.wrote && lw.f != nil {
		lw.f.Flush()
	}
}

// streamErrStatus maps a deploy-backend error (Logs/Stats, the lifecycle
// actions, and the archive endpoints) to an HTTP status. A missing deployment
// is classified primarily via errors.Is on the deploy.ErrNotFound sentinel
// every backend wraps; a missing archive PATH likewise via fs.ErrNotExist (the
// containerdhost tarcopy backend returns raw stat errors — docker uses 404 for
// a missing cp path). Message matching remains as a fallback for error shapes
// that lose the sentinel (e.g. text relayed from a remote docker engine, whose
// "404 Not Found: ..." status text it matches). Anything unrecognized is a 500.
func streamErrStatus(err error) int {
	if errors.Is(err, deploy.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return http.StatusNotFound
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no instances") || strings.Contains(msg, "no such") || strings.Contains(msg, "not found"):
		return http.StatusNotFound
	case strings.Contains(msg, "not supported") || strings.Contains(msg, "unsupported"):
		return http.StatusNotImplemented
	case strings.Contains(msg, "invalid since"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// handleDeployArchive serves the container archive endpoint used by docker cp
// (/.cornus/v1/deploy/{name}/archive), splitting on method:
//
//	HEAD -> path stat only, in the X-Docker-Container-Path-Stat header
//	GET  -> that header plus a tar of the path as the body
//	PUT  -> extract the request-body tar into the path
func (s *Server) handleDeployArchive(w http.ResponseWriter, r *http.Request, backend deploy.Backend, name string) {
	q := r.URL.Query()
	path := q.Get("path")
	switch r.Method {
	case http.MethodHead:
		st, err := backend.StatPath(r.Context(), name, path)
		if err != nil {
			// A missing deployment or path is the caller's error: streamErrStatus
			// maps deploy.ErrNotFound / fs.ErrNotExist (and their message shapes)
			// to a 404 — docker's status for a missing cp path — and an
			// unsupported backend to a 501, not a blanket 500.
			writeJSON(w, streamErrStatus(err), map[string]string{"error": err.Error()})
			return
		}
		if enc, err := api.EncodePathStat(st); err == nil {
			w.Header().Set(api.PathStatHeader, enc)
		}
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		// The stat header must precede the tar body, so resolve it first. Its
		// error mapping matches HEAD above.
		st, err := backend.StatPath(r.Context(), name, path)
		if err != nil {
			writeJSON(w, streamErrStatus(err), map[string]string{"error": err.Error()})
			return
		}
		if enc, err := api.EncodePathStat(st); err == nil {
			w.Header().Set(api.PathStatHeader, enc)
		}
		w.Header().Set("Content-Type", "application/x-tar")
		// As in Logs/Stats: the 200 header is deferred until the backend's first
		// tar byte, so a CopyFrom error before any output (e.g. the path vanished
		// between stat and copy, or the deployment stopped) becomes a real error
		// response instead of an empty 200. After the body has begun, a
		// mid-stream error can no longer change the status; it rides the
		// X-Cornus-Stream-Error trailer so the client knows the tar is truncated.
		lw := newLazyFlushWriter(w)
		if _, err := backend.CopyFrom(r.Context(), name, path, lw); err != nil {
			if !lw.wrote {
				w.Header().Del(api.PathStatHeader)
				writeJSON(w, streamErrStatus(err), map[string]string{"error": err.Error()})
				return
			}
			lw.setStreamError(err)
		}

	case http.MethodPut:
		opts := api.CopyToOptions{
			NoOverwriteDirNonDir: q.Get("noOverwriteDirNonDir") == "1" || q.Get("noOverwriteDirNonDir") == "true",
			CopyUIDGID:           q.Get("copyUIDGID") == "1" || q.Get("copyUIDGID") == "true",
		}
		// PUT responds only after the extraction completes, so errors already
		// surface; classify them like GET/HEAD (missing deployment/path -> 404)
		// instead of a blanket 500.
		if err := backend.CopyTo(r.Context(), name, path, r.Body, opts); err != nil {
			writeJSON(w, streamErrStatus(err), map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleDeployAction runs a lifecycle action (start/stop/restart) on a deployment.
func (s *Server) handleDeployAction(w http.ResponseWriter, r *http.Request, backend deploy.Backend, name, action string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var err error
	switch action {
	case "start":
		err = s.traceDeploy(r.Context(), "start", name, func(ctx context.Context) error { return backend.Start(ctx, name) })
	case "stop":
		err = s.traceDeploy(r.Context(), "stop", name, func(ctx context.Context) error { return backend.Stop(ctx, name) })
	case "restart":
		err = s.traceDeploy(r.Context(), "restart", name, func(ctx context.Context) error { return backend.Restart(ctx, name) })
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown action: " + action})
		return
	}
	if err != nil {
		// Backends wrap deploy.ErrNotFound for a missing deployment, which
		// streamErrStatus maps to a 404 (a stop/start/restart of an unknown name
		// is the caller's error, not a server fault).
		writeJSON(w, streamErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
