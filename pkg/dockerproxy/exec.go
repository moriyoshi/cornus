package dockerproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cornus/pkg/api"
)

// execRecord is the proxy's memory of one created exec: which deployment it runs
// against and whether it was created with a TTY (needed at exec-start time).
type execRecord struct {
	deployment string
	tty        bool
}

// execRegistry maps a backend exec id to its record.
type execRegistry struct {
	mu   sync.Mutex
	byID map[string]execRecord
}

func newExecRegistry() *execRegistry { return &execRegistry{byID: map[string]execRecord{}} }

func (e *execRegistry) put(id string, rec execRecord) {
	e.mu.Lock()
	e.byID[id] = rec
	e.mu.Unlock()
}

func (e *execRegistry) get(id string) (execRecord, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec, ok := e.byID[id]
	return rec, ok
}

// del removes an exec record once its exec has finished, so the registry does
// not accumulate one entry per `docker exec` forever (health-check / probe /
// CI loops drive the same container thousands of times). It is idempotent.
func (e *execRegistry) del(id string) {
	e.mu.Lock()
	delete(e.byID, id)
	e.mu.Unlock()
}

// execConfigRequest is Docker's POST /containers/{id}/exec request body (subset).
type execConfigRequest struct {
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
	Cmd          []string `json:"Cmd"`
	Env          []string `json:"Env"`
	WorkingDir   string   `json:"WorkingDir"`
	User         string   `json:"User"`
	Privileged   bool     `json:"Privileged"`
}

// execStartRequest is Docker's POST /exec/{id}/start request body.
type execStartRequest struct {
	Detach bool `json:"Detach"`
	Tty    bool `json:"Tty"`
}

// execCreate serves POST /containers/{id}/exec: it parses docker's exec config,
// creates the exec against the container's cornus deployment, records it, and
// returns {"Id": execID}.
func (p *Proxy) execCreate(w http.ResponseWriter, r *http.Request, rec *containerRecord) {
	var req execConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dockerError(w, http.StatusBadRequest, "invalid exec config: "+err.Error())
		return
	}
	cfg := api.ExecConfig{
		Cmd:          req.Cmd,
		Tty:          req.Tty,
		AttachStdin:  req.AttachStdin,
		AttachStdout: req.AttachStdout,
		AttachStderr: req.AttachStderr,
		Env:          req.Env,
		WorkingDir:   req.WorkingDir,
		User:         req.User,
		Privileged:   req.Privileged,
	}
	execID, err := p.attacher.ExecCreate(r.Context(), rec.deployment, cfg)
	if err != nil {
		dockerError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.execs.put(execID, execRecord{deployment: rec.deployment, tty: req.Tty})
	writeJSON(w, http.StatusCreated, map[string]string{"Id": execID})
}

// handleExecItem routes /exec/{id}/start and /exec/{id}/json.
func (p *Proxy) handleExecItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/exec/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" {
		dockerError(w, http.StatusBadRequest, "missing exec id")
		return
	}
	switch {
	case action == "start" && r.Method == http.MethodPost:
		p.execStart(w, r, id)
	case action == "json" && r.Method == http.MethodGet:
		p.execInspect(w, r, id)
	case action == "resize" && r.Method == http.MethodPost:
		p.execResize(w, r, id)
	default:
		dockerError(w, http.StatusNotFound, "unsupported exec operation: "+action)
	}
}

// execResize serves POST /exec/{id}/resize?h=<rows>&w=<cols>: the docker CLI
// sends this on the exec's initial size and on every SIGWINCH while a
// `docker exec -it` window is resized. It parses the dimensions and forwards
// them to the backend so the exec's TTY tracks the terminal. Docker replies 200
// with an empty body on success (500 on error).
func (p *Proxy) execResize(w http.ResponseWriter, r *http.Request, id string) {
	q := r.URL.Query()
	h, err := strconv.ParseUint(q.Get("h"), 10, 32)
	if err != nil {
		dockerError(w, http.StatusBadRequest, "invalid height: "+q.Get("h"))
		return
	}
	width, err := strconv.ParseUint(q.Get("w"), 10, 32)
	if err != nil {
		dockerError(w, http.StatusBadRequest, "invalid width: "+q.Get("w"))
		return
	}
	if err := p.attacher.ExecResize(r.Context(), id, uint(h), uint(width)); err != nil {
		dockerError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

// execStart serves POST /exec/{id}/start: it hijacks the docker CLI connection,
// replies with the daemon's raw-stream upgrade handshake, opens the backend exec
// tunnel, and bridges bytes both ways until either side closes.
func (p *Proxy) execStart(w http.ResponseWriter, r *http.Request, id string) {
	var req execStartRequest
	// The body is small JSON ({"Detach":..,"Tty":..}); ignore decode errors so an
	// empty body still starts the exec.
	_ = json.NewDecoder(r.Body).Decode(&req)
	tty := req.Tty
	if rec, ok := p.execs.get(id); ok {
		tty = rec.tty
	}
	// The exec runs to completion within this handler (bridge blocks until the
	// exec's stream closes), so its record is no longer needed once we return.
	// Reclaim it here to bound execRegistry under repeated `docker exec`.
	defer p.execs.del(id)

	upgrade := r.Header.Get("Upgrade") != ""
	conn, brw, err := hijackConn(w)
	if err != nil {
		dockerError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer conn.Close()
	if err := writeStreamHandshake(conn, upgrade, streamContentType(r, tty)); err != nil {
		return
	}

	stream, err := p.attacher.ExecStart(r.Context(), id, api.ExecStartConfig{Tty: tty, Detach: req.Detach})
	if err != nil {
		// Handshake already sent; the CLI treats a closed stream as exec end.
		return
	}
	bridge(&bufConn{Conn: conn, r: brw.Reader}, stream)
}

// execInspect serves GET /exec/{id}/json (docker exec inspect), rendering the
// backend exec state in docker's shape (at minimum ID/Running/ExitCode).
func (p *Proxy) execInspect(w http.ResponseWriter, r *http.Request, id string) {
	st, err := p.attacher.ExecInspect(r.Context(), id)
	if err != nil {
		dockerError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ID":       id,
		"Running":  st.Running,
		"ExitCode": st.ExitCode,
		"Pid":      st.Pid,
	})
}

// attachContainer serves POST /containers/{id}/attach: it hijacks the docker CLI
// connection, replies with the raw-stream upgrade handshake, opens the backend
// attach tunnel, and bridges bytes both ways until either side closes.
func (p *Proxy) attachContainer(w http.ResponseWriter, r *http.Request, rec *containerRecord) {
	q := r.URL.Query()
	isSet := func(k string) bool { return q.Get(k) == "1" || q.Get(k) == "true" }
	cfg := api.AttachConfig{
		Stream: isSet("stream"),
		Stdin:  isSet("stdin"),
		Stdout: isSet("stdout"),
		Stderr: isSet("stderr"),
		Logs:   isSet("logs"),
	}

	upgrade := r.Header.Get("Upgrade") != ""
	conn, brw, err := hijackConn(w)
	if err != nil {
		dockerError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer conn.Close()
	if err := writeStreamHandshake(conn, upgrade, streamContentType(r, rec.spec.TTY)); err != nil {
		return
	}

	// `docker run` (foreground) attaches BEFORE start, when the deployment does
	// not exist yet. Real dockerd accepts the attach and starts streaming once
	// the container runs; mirror that by holding the hijacked connection until
	// the deploy-attach session goes live, then opening the backend tunnel.
	lateAttach := false
	if rec.session() == nil {
		select {
		case <-rec.started():
			lateAttach = true
		case <-r.Context().Done():
			return
		case <-time.After(startReadyTimeout):
			return
		}
	}
	if lateAttach {
		// Everything below follows from one asymmetry: real dockerd registers the
		// attach BEFORE the container's process starts, so `logs=0` cannot lose
		// anything. Cornus deploys and waits for readiness first, so by the time
		// the tunnel opens the workload has already been running for a moment, and
		// dockerd carries only what is written after the attach is registered.
		// Everything in that window is discarded — for an `echo` workload that is
		// ALL of the output, which is why a foreground `docker run` printed nothing
		// and why the devcontainer CLI waited ten minutes to see a string it had
		// already missed. Replaying from the container's first byte closes the
		// window; the stream then continues live, so nothing is doubled.
		//
		// Only on THIS branch. An attach to an already-established session (plain
		// `docker attach`) missed nothing, and replaying there would re-print
		// history the caller never asked for.
		cfg.Logs = true
	}

	stream, err := p.attacher.Attach(r.Context(), rec.deployment, cfg)
	if err != nil {
		// The caller just sees the hijacked connection close, which is survivable
		// — but silence here is precisely what left an investigation with nothing
		// to go on while attach was delivering no output at all.
		slog.Warn("docker proxy: attach tunnel failed", "deployment", rec.deployment, "error", err)
		return
	}
	remote := newIdleTracker(stream)
	if lateAttach {
		defer p.endWhenWorkloadDrains(rec, remote)()
	}
	bridge(&bufConn{Conn: conn, r: brw.Reader}, remote)
}

// Grace period after the workload exits during which the attach tunnel must stay
// quiet before its output is called complete. It has to outlast only the bytes
// already in flight between the container's last write and this end of the
// tunnel — not anything the workload does — so it is short.
const attachDrainQuiet = 300 * time.Millisecond

// attachDrainCap bounds the drain however the tunnel behaves, so a backend that
// keeps the stream busy cannot hold a finished `docker run` open forever.
const attachDrainCap = 10 * time.Second

// idleTracker wraps the backend tunnel and records when it last produced bytes,
// which is what lets the drain below distinguish "the workload is gone and its
// output has arrived" from "the workload is gone and output is still in flight".
type idleTracker struct {
	net.Conn
	mu   sync.Mutex
	last time.Time
}

func newIdleTracker(c net.Conn) *idleTracker {
	return &idleTracker{Conn: c, last: time.Now()}
}

func (t *idleTracker) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if n > 0 {
		t.mu.Lock()
		t.last = time.Now()
		t.mu.Unlock()
	}
	return n, err
}

// CloseWrite forwards the stdin half-close that bridge performs. Without it the
// wrapper would HIDE the underlying connection's CloseWrite — net.Conn does not
// declare it, so embedding never promotes it — and a foreground `docker run -i`
// would silently stop signalling end-of-stdin to the workload.
func (t *idleTracker) CloseWrite() error {
	if cw, ok := t.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// idleFor reports how long the tunnel has been quiet.
func (t *idleTracker) idleFor(now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return now.Sub(t.last)
}

// endWhenWorkloadDrains closes the attach tunnel once the workload has exited AND
// its output has stopped arriving, returning a func that stops the watcher.
//
// It exists because dockerd does NOT EOF an attach opened against a container
// that has ALREADY exited: a stream opened post-exit stays open indefinitely,
// while one opened while the container was running EOFs about 20ms after it
// stops. A short-lived workload therefore leaves bridge blocked forever in its
// output copy — the hang half of a foreground `docker run`.
//
// The quiet period is the whole point, and is what a previous attempt got wrong.
// Closing the instant the workload is known to be gone truncates: the output is
// still crossing the tunnel at that moment, and cutting it there converted a
// visible hang into a silent empty success. "The workload has exited" says
// nothing more will be produced; "the tunnel has been quiet" says nothing more is
// still on the way. Both are needed.
//
// The exit signal is deliberately awaitExit and NOT session.Done(): the server
// holds the deploy-attach session OPEN after a workload exits on its own (so
// `docker logs` still answers for a completed one-shot), so Done() never fires
// for precisely the short-lived containers this exists to unblock. awaitExit
// settles from a backend status poll as well, which is the same signal /wait
// resolves on — so the stream and the exit code can never disagree.
func (p *Proxy) endWhenWorkloadDrains(rec *containerRecord, t *idleTracker) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.awaitExit(ctx, rec, rec.session())
		if ctx.Err() != nil {
			return // the bridge finished on its own; nothing to close
		}
		deadline := time.Now().Add(attachDrainCap)
		tick := time.NewTicker(attachDrainQuiet / 3)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				if t.idleFor(now) >= attachDrainQuiet || now.After(deadline) {
					_ = t.Close()
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

// hijackConn takes over the underlying TCP connection from w. After this the
// proxy owns the connection: it must write the raw HTTP response itself and
// Close the connection when done.
func hijackConn(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("connection does not support hijacking")
	}
	return hj.Hijack()
}

// writeStreamHandshake writes the daemon-style response that switches the
// hijacked connection to Docker's bidirectional stream. When the CLI asked to
// upgrade (Connection: Upgrade), the daemon replies 101 UPGRADED with the
// Upgrade headers; otherwise it replies 200 OK. contentType announces whether
// the body that follows is raw or stdcopy-multiplexed (see streamContentType);
// this response is hand-written rather than sent through net/http because the
// connection is already hijacked, so nothing else will set the header.
func writeStreamHandshake(conn net.Conn, upgrade bool, contentType string) error {
	var resp string
	if upgrade {
		resp = "HTTP/1.1 101 UPGRADED\r\n" +
			"Content-Type: " + contentType + "\r\n" +
			"Connection: Upgrade\r\n" +
			"Upgrade: tcp\r\n" +
			"\r\n"
	} else {
		resp = "HTTP/1.1 200 OK\r\n" +
			"Content-Type: " + contentType + "\r\n" +
			"\r\n"
	}
	_, err := io.WriteString(conn, resp)
	return err
}

// bufConn is a net.Conn whose reads come from a buffered reader (so bytes the
// http server buffered past the request headers are not lost) while writes and
// Close go to the underlying connection.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// bridge copies bytes bidirectionally between the docker CLI connection and the
// backend tunnel (which carries the Docker-origin stream). The directions are
// NOT symmetric:
//
//   - Output (docker -> client) is authoritative: bridge returns only when this
//     copy finishes (Docker closed its side because the process exited and its
//     output is drained), then both conns are closed.
//   - Input (client -> docker) carries stdin. A non-interactive `docker exec`
//     sends no stdin, so this copy hits EOF immediately; tearing the tunnel down
//     then would truncate the output before the process's stdout arrives. So on
//     stdin EOF we only best-effort half-close the backend write side (CloseWrite
//     is a no-op over the websocket net.Conn) and leave the output flowing.
func bridge(client, docker io.ReadWriteCloser) {
	outDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(client, docker) // output: docker -> client (authoritative)
		close(outDone)
	}()
	go func() {
		_, _ = io.Copy(docker, client) // input: client stdin -> docker
		if cw, ok := docker.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	<-outDone
	docker.Close()
	client.Close() // unblocks the input copy if it is still reading stdin
}
