package dockerproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/attachsession"
	"cornus/pkg/deploy"
	"cornus/pkg/portfwd"
)

// startReadyTimeout bounds how long /containers/{id}/start waits for the
// deployment to report ready.
const startReadyTimeout = 180 * time.Second

// handleContainerCreate buffers a create: it translates the request to a
// DeploySpec and stores a record, but does not contact the server until start.
func (p *Proxy) handleContainerCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		dockerError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dockerError(w, http.StatusBadRequest, "invalid create body: "+err.Error())
		return
	}
	if req.Image == "" {
		dockerError(w, http.StatusBadRequest, "Image is required")
		return
	}
	id := newID()
	name := r.URL.Query().Get("name")
	dep := deploymentName(name, id)
	rec := &containerRecord{
		id:         id,
		name:       "/" + strings.TrimPrefix(name, "/"),
		deployment: dep,
		created:    nowRFC3339(),
		req:        req,
		spec:       toDeploySpec(dep, req),
		state:      "created",
		startedC:   make(chan struct{}),
	}
	if name == "" {
		rec.name = "/" + dep
	}
	for net := range req.NetworkingConfig.EndpointsConfig {
		rec.networks = append(rec.networks, net)
	}
	p.reg.put(rec)
	writeJSON(w, http.StatusCreated, createResponse{ID: id})
}

// handleContainerItem routes /containers/{id} and /containers/{id}/{action}.
func (p *Proxy) handleContainerItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/containers/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" {
		dockerError(w, http.StatusBadRequest, "missing container id")
		return
	}
	rec := p.reg.get(id)
	if rec == nil {
		dockerError(w, http.StatusNotFound, "no such container: "+id)
		return
	}

	switch {
	case action == "json" && r.Method == http.MethodGet:
		p.inspect(w, rec)
	case action == "start" && r.Method == http.MethodPost:
		p.start(w, r, rec)
	case (action == "stop" || action == "kill") && r.Method == http.MethodPost:
		p.stopContainer(w, rec)
	case action == "wait" && r.Method == http.MethodPost:
		p.wait(w, r, rec)
	case action == "logs" && r.Method == http.MethodGet:
		p.logs(w, r, rec)
	case action == "stats" && r.Method == http.MethodGet:
		p.stats(w, r, rec)
	case action == "archive" && r.Method == http.MethodGet:
		p.archiveGet(w, r, rec)
	case action == "archive" && r.Method == http.MethodPut:
		p.archivePut(w, r, rec)
	case action == "archive" && r.Method == http.MethodHead:
		p.archiveStat(w, r, rec)
	case action == "exec" && r.Method == http.MethodPost:
		p.execCreate(w, r, rec)
	case action == "attach" && r.Method == http.MethodPost:
		p.attachContainer(w, r, rec)
	case action == "resize" && r.Method == http.MethodPost:
		p.containerResize(w, r, rec)
	case action == "" && r.Method == http.MethodDelete:
		p.remove(w, rec)
	default:
		dockerError(w, http.StatusNotFound, "unsupported container operation: "+action)
	}
}

func (p *Proxy) start(w http.ResponseWriter, _ *http.Request, rec *containerRecord) {
	if rec.session() != nil {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	sess := attachsession.Open(p.attacher, rec.spec)
	ctx, cancel := context.WithTimeout(context.Background(), startReadyTimeout)
	defer cancel()
	if err := sess.WaitReady(ctx); err != nil {
		sess.Stop()
		dockerError(w, http.StatusInternalServerError, "start "+rec.deployment+": "+err.Error())
		return
	}
	// Expose the container's published ports. When the agent supplied a shared
	// conduit (WithConduit) the ports go through it — bound local listeners in
	// port-forward mode, or reachable by name through the shared SOCKS5 proxy;
	// withdrawn by cancelling a per-container context. Otherwise the proxy binds
	// its own per-container listeners (docker -p semantics: the port appears on the
	// DOCKER_HOST machine). Either way the exposure lives until setExited.
	var cleanup func()
	switch {
	case p.conduitAdd != nil && len(rec.spec.Ports) > 0:
		ectx, ecancel := context.WithCancel(context.Background())
		if err := p.conduitAdd(ectx, rec.deployment, rec.spec.Ports); err != nil {
			ecancel()
		} else {
			cleanup = ecancel
		}
	case p.forwardPorts && len(rec.spec.Ports) > 0:
		fwd, _ := portfwd.Start(context.Background(), p.attacher, rec.deployment, rec.spec.Ports)
		if fwd != nil && len(fwd.Forwards()) > 0 {
			cleanup = fwd.Close
		} else if fwd != nil {
			fwd.Close() // every mapping skipped
		}
	}
	if !rec.setRunning(sess, cleanup) {
		// A concurrent /start won the race and installed its own session first.
		// Discard ours so we neither leak this deploy-attach nor double-close
		// startedC (which would panic the server).
		sess.Stop()
		if cleanup != nil {
			cleanup()
		}
		w.WriteHeader(http.StatusNotModified)
		return
	}
	// Reconcile state when the workload exits on its own (e.g. `docker run -d`
	// with no wait): nothing else observes sess.done, so without this the record
	// would stay "running" forever and its port exposure (portfwd listeners /
	// conduit registration) would never be withdrawn. setExited is idempotent, so
	// a concurrent stop/remove/wait that beats us here just makes this a no-op —
	// and only the winner publishes "die".
	go func() {
		<-sess.Done()
		if rec.setExited(sess) {
			p.hub.publish(containerEvent("die", rec))
		}
	}()
	p.hub.publish(containerEvent("start", rec))
	w.WriteHeader(http.StatusNoContent)
}

// logs streams a container's logs (GET /containers/{id}/logs). It parses
// docker's log query params, resolves the container to its cornus deployment,
// and streams the backend's stdcopy-multiplexed frames straight through with the
// docker raw-stream Content-Type. The backend guarantees framing (dockerhost
// passes Docker's already-framed non-TTY bytes through; the kube backend wraps
// its raw stream in stdcopy stdout framing), so the proxy does not re-frame.
func (p *Proxy) logs(w http.ResponseWriter, r *http.Request, rec *containerRecord) {
	q := r.URL.Query()
	opts := api.LogOptions{
		Follow:     q.Get("follow") == "1" || q.Get("follow") == "true",
		Stdout:     q.Get("stdout") == "1" || q.Get("stdout") == "true",
		Stderr:     q.Get("stderr") == "1" || q.Get("stderr") == "true",
		Timestamps: q.Get("timestamps") == "1" || q.Get("timestamps") == "true",
		Tail:       q.Get("tail"),
		Since:      q.Get("since"),
	}
	w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	// The 200 header is written lazily on the backend's first output byte, so a
	// backend error before any output still becomes a docker-shaped error
	// response instead of an empty 200. Once the body has begun, a mid-stream
	// error cannot change the status and is dropped as before.
	lw := newLazyFlushWriter(w)
	if err := p.attacher.Logs(r.Context(), rec.deployment, opts, lw); err != nil && !lw.wrote {
		dockerError(w, streamErrStatus(err), err.Error())
	}
}

// stats streams a container's metrics (GET /containers/{id}/stats). It parses
// docker's ?stream flag, resolves the container to its cornus deployment, and
// passes the backend's Docker-format stats JSON straight through (the docker CLI
// parses Docker's own format), flushing per write.
func (p *Proxy) stats(w http.ResponseWriter, r *http.Request, rec *containerRecord) {
	q := r.URL.Query()
	// Docker streams by default; only ?stream=0/false requests a single object.
	stream := q.Get("stream") != "0" && q.Get("stream") != "false"
	w.Header().Set("Content-Type", "application/json")
	// As in logs: the 200 header is deferred until the backend's first output
	// byte so a pre-output backend error (e.g. the kubernetes backend's "stats
	// not supported", relayed by the cornus server) becomes a docker-shaped
	// error response. After the body has begun, the status cannot change.
	lw := newLazyFlushWriter(w)
	if err := p.attacher.Stats(r.Context(), rec.deployment, api.StatsOptions{Stream: stream}, lw); err != nil && !lw.wrote {
		dockerError(w, streamErrStatus(err), err.Error())
	}
}

// archiveStat serves docker cp's HEAD /containers/{id}/archive: it returns the
// path stat in the X-Docker-Container-Path-Stat header (no body). The error is
// classified by streamErrStatus (missing deployment/path -> 404, docker's
// status for a missing cp path; an unsupported backend -> 501) rather than a
// blanket 404.
func (p *Proxy) archiveStat(w http.ResponseWriter, r *http.Request, rec *containerRecord) {
	st, err := p.attacher.StatPath(r.Context(), rec.deployment, r.URL.Query().Get("path"))
	if err != nil {
		dockerError(w, streamErrStatus(err), err.Error())
		return
	}
	if enc, err := api.EncodePathStat(st); err == nil {
		w.Header().Set(api.PathStatHeader, enc)
	}
	w.WriteHeader(http.StatusOK)
}

// archiveGet serves docker cp's GET /containers/{id}/archive: it sets the path
// stat header (resolved first, so it precedes the body) then streams a tar of
// the path. The tar bytes are Docker's own archive format, passed through.
func (p *Proxy) archiveGet(w http.ResponseWriter, r *http.Request, rec *containerRecord) {
	path := r.URL.Query().Get("path")
	st, err := p.attacher.StatPath(r.Context(), rec.deployment, path)
	if err != nil {
		dockerError(w, streamErrStatus(err), err.Error())
		return
	}
	if enc, err := api.EncodePathStat(st); err == nil {
		w.Header().Set(api.PathStatHeader, enc)
	}
	w.Header().Set("Content-Type", "application/x-tar")
	// As in logs/stats: the 200 header is deferred until the first tar byte so
	// a CopyFrom error before any output (e.g. the path vanished between stat
	// and copy) becomes a docker-shaped error response instead of an empty
	// 200. After the body has begun, a mid-stream error cannot change the
	// status and is dropped.
	lw := newLazyFlushWriter(w)
	if _, err := p.attacher.CopyFrom(r.Context(), rec.deployment, path, lw); err != nil && !lw.wrote {
		w.Header().Del(api.PathStatHeader)
		dockerError(w, streamErrStatus(err), err.Error())
	}
}

// archivePut serves docker cp's PUT /containers/{id}/archive: it extracts the
// request-body tar into the path, translating docker's noOverwriteDirNonDir /
// copyUIDGID query params.
func (p *Proxy) archivePut(w http.ResponseWriter, r *http.Request, rec *containerRecord) {
	q := r.URL.Query()
	opts := api.CopyToOptions{
		NoOverwriteDirNonDir: q.Get("noOverwriteDirNonDir") == "1" || q.Get("noOverwriteDirNonDir") == "true",
		CopyUIDGID:           q.Get("copyUIDGID") == "1" || q.Get("copyUIDGID") == "true",
	}
	// PUT responds only after the extraction completes, so errors already
	// surface; classify them like GET/HEAD (missing deployment/path -> 404)
	// instead of a blanket 500.
	if err := p.attacher.CopyTo(r.Context(), rec.deployment, q.Get("path"), r.Body, opts); err != nil {
		dockerError(w, streamErrStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

// lazyFlushWriter defers WriteHeader(http.StatusOK) — and thus the committed
// response status — until the first Write, then flushes after every write so a
// follow stream reaches the docker client promptly. It lets a streaming handler
// turn a backend error that happens before any output into a proper error
// response instead of an empty 200. (Duplicated from pkg/server: the proxy
// cannot cleanly import the server package for a ~30-line helper.)
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
		lw.w.WriteHeader(http.StatusOK)
	}
	n, err := lw.w.Write(p)
	if lw.f != nil {
		lw.f.Flush()
	}
	return n, err
}

// Flush implements http.Flusher. Before the first Write it is a no-op so it
// cannot commit the 200 status prematurely.
func (lw *lazyFlushWriter) Flush() {
	if lw.wrote && lw.f != nil {
		lw.f.Flush()
	}
}

// streamErrStatus maps a streaming-backend error (Logs/Stats and the archive
// endpoints) to an HTTP status. When the attacher is an in-process
// deploy.Backend the deploy.ErrNotFound sentinel survives and errors.Is
// classifies it directly — likewise fs.ErrNotExist for a missing archive PATH
// (the containerdhost tarcopy backend returns raw stat errors; docker uses 404
// for a missing cp path). The usual attacher is a cornus server client whose
// errors only carry the server's status text plus the backend message, so
// message matching remains the fallback (a relayed "404 Not Found: ..." matches
// "not found"). Anything unrecognized is a 500. (Duplicated from pkg/server;
// see lazyFlushWriter.)
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

// containerResize serves POST /containers/{id}/resize?h=<rows>&w=<cols>, which
// the docker CLI sends for a `docker run -it` / `docker attach` primary-TTY
// window change. cornus's attach has no per-container primary-TTY resize
// primitive (there is no backend ContainerResize), so this is a deliberate
// NO-OP: it parses and accepts the request and returns 200 so the docker CLI
// does not error, but the new dimensions are NOT propagated to the workload.
// Only `docker exec -it` resize is wired end to end (see execResize). We still
// validate h/w so a malformed request is rejected consistently with execResize.
func (p *Proxy) containerResize(w http.ResponseWriter, r *http.Request, _ *containerRecord) {
	q := r.URL.Query()
	if _, err := strconv.ParseUint(q.Get("h"), 10, 32); err != nil {
		dockerError(w, http.StatusBadRequest, "invalid height: "+q.Get("h"))
		return
	}
	if _, err := strconv.ParseUint(q.Get("w"), 10, 32); err != nil {
		dockerError(w, http.StatusBadRequest, "invalid width: "+q.Get("w"))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (p *Proxy) stopContainer(w http.ResponseWriter, rec *containerRecord) {
	if sess := rec.session(); sess != nil {
		sess.Stop()
		if rec.setExited(sess) {
			p.hub.publish(containerEvent("die", rec))
		}
		p.hub.publish(containerEvent("stop", rec))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Proxy) remove(w http.ResponseWriter, rec *containerRecord) {
	if sess := rec.session(); sess != nil {
		sess.Stop()
		if rec.setExited(sess) {
			p.hub.publish(containerEvent("die", rec))
		}
	}
	p.reg.del(rec.id)
	p.hub.publish(containerEvent("destroy", rec))
	w.WriteHeader(http.StatusNoContent)
}

// wait serves POST /containers/{id}/wait. Docker's `condition` query parameter
// changes when the response body may be sent: the default ("not-running")
// answers immediately for a container that is not running, but "next-exit" —
// which `docker run` (foreground) sends BEFORE start — must hold the body until
// the container next exits. Answering it immediately makes docker run report
// exit the moment start returns (the devcontainer CLI then treats the
// keepalive container as dead).
//
// The response HEADER must flush immediately regardless: the docker client's
// ContainerWait blocks on it before the CLI issues start, so holding the header
// back deadlocks `docker run` (wait waits for start, start waits for wait).
// dockerd behaves the same way — 200 now, JSON body at exit.
func (p *Proxy) wait(w http.ResponseWriter, r *http.Request, rec *containerRecord) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	sess := rec.session()
	if cond := r.URL.Query().Get("condition"); cond == "next-exit" || cond == "removed" {
		if sess == nil {
			select {
			case <-rec.started():
				sess = rec.session()
			case <-r.Context().Done():
				return
			}
		}
	}
	if sess != nil {
		select {
		case <-sess.Done():
			rec.setExited(sess)
		case <-r.Context().Done():
			return
		}
	}
	// KNOWN LIMITATION: StatusCode is hardcoded to 0, so a workload that exits
	// non-zero is reported to `docker wait` / `docker run` as success. The real
	// exit code is not available here: neither deploywire.Event nor
	// api.InstanceStatus carries it, and the deploy-attach session (session.done)
	// only signals that the attach ended. Propagating the true code needs a
	// cross-package change (thread an exit code through the DeployAttach events
	// and record it on the session) before it can be encoded here.
	_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": 0})
}

// handleContainerList serves docker ps (GET /containers/json).
func (p *Proxy) handleContainerList(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1" || r.URL.Query().Get("all") == "true"
	labels := parseLabelFilters(r.URL.Query().Get("filters"))
	out := make([]containerSummary, 0)
	for _, rec := range p.reg.list(all, labels) {
		state := rec.stateNow()
		out = append(out, containerSummary{
			ID:     rec.id,
			Names:  []string{rec.name},
			Image:  rec.spec.Image,
			State:  state,
			Status: state,
			Labels: rec.req.Labels,
			Mounts: mountsOf(rec),
			// The summary shape has no Ports inside NetworkSettings, only the
			// Networks map (dockerd's types.SummaryNetworkSettings).
			NetworkSettings: map[string]any{"Networks": p.networkEndpoints(rec)},
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (p *Proxy) inspect(w http.ResponseWriter, rec *containerRecord) {
	state := rec.stateNow()
	writeJSON(w, http.StatusOK, containerJSON{
		ID:      rec.id,
		Name:    rec.name,
		Created: rec.created,
		Image:   rec.spec.Image,
		State: stateJSON{
			Status:    state,
			Running:   state == "running",
			StartedAt: rec.created,
		},
		Config: configJSON{
			Image:      rec.spec.Image,
			Cmd:        rec.spec.Command,
			Entrypoint: rec.spec.Entrypoint,
			Env:        rec.req.Env,
			Labels:     rec.req.Labels,
		},
		Mounts:          mountsOf(rec),
		NetworkSettings: p.networkSettings(rec),
		HostConfig:      map[string]any{"NetworkMode": "default"},
	})
}

// networkSettings builds a non-nil NetworkSettings whose Networks map has an
// entry per attached network (compose dereferences these after create).
func (p *Proxy) networkSettings(rec *containerRecord) map[string]any {
	return map[string]any{"Networks": p.networkEndpoints(rec), "Ports": map[string]any{}}
}

// networkEndpoints renders the Networks map shared by container inspect and
// the container LIST summary: one endpoint per attached network, keyed by
// network name, with the network's real id when the network store knows it.
func (p *Proxy) networkEndpoints(rec *containerRecord) map[string]any {
	nets := map[string]any{}
	for _, name := range rec.networks {
		id := name
		if n := p.networks.get(name); n != nil {
			id = n.ID
		}
		nets[name] = map[string]any{
			"NetworkID":  id,
			"EndpointID": rec.id,
			"Gateway":    "",
			"IPAddress":  "",
			"Aliases":    []string{},
		}
	}
	return nets
}

func mountsOf(rec *containerRecord) []mountJSON {
	out := make([]mountJSON, 0, len(rec.spec.Mounts))
	for _, m := range rec.spec.Mounts {
		out = append(out, mountJSON{Type: "bind", Source: m.Source, Destination: m.Target, RW: !m.ReadOnly})
	}
	return out
}

// parseLabelFilters extracts label filters from Docker's `filters` query param.
// Clients at API >= 1.22 (every modern docker CLI) send the map form
// {"label":{"k=v":true}}; older clients send the legacy list form
// {"label":["k","k2=v"]}. Both must be honored — dropping the filter would make
// `docker ps --filter label=...` match every container.
func parseLabelFilters(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" {
		return out
	}
	add := func(l string) {
		k, v, _ := strings.Cut(l, "=")
		out[k] = v
	}
	var m map[string]map[string]bool
	if err := json.Unmarshal([]byte(raw), &m); err == nil {
		for l, on := range m["label"] {
			if on {
				add(l)
			}
		}
		return out
	}
	var f map[string][]string
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return out
	}
	for _, l := range f["label"] {
		add(l)
	}
	return out
}

func nowRFC3339() string {
	// The proxy has no reason to fabricate a precise timestamp; a stable epoch
	// keeps inspect output well-formed without pretending to be authoritative.
	return "1970-01-01T00:00:00Z"
}
