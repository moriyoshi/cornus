package dockerproxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
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
	if err := writeRawStreamHandshake(conn, upgrade); err != nil {
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
	if err := writeRawStreamHandshake(conn, upgrade); err != nil {
		return
	}

	// `docker run` (foreground) attaches BEFORE start, when the deployment does
	// not exist yet. Real dockerd accepts the attach and starts streaming once
	// the container runs; mirror that by holding the hijacked connection until
	// the deploy-attach session goes live, then opening the backend tunnel.
	if rec.session() == nil {
		select {
		case <-rec.started():
		case <-r.Context().Done():
			return
		case <-time.After(startReadyTimeout):
			return
		}
	}

	stream, err := p.attacher.Attach(r.Context(), rec.deployment, cfg)
	if err != nil {
		return
	}
	bridge(&bufConn{Conn: conn, r: brw.Reader}, stream)
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

// writeRawStreamHandshake writes the daemon-style response that switches the
// hijacked connection to Docker's raw bidirectional stream. When the CLI asked
// to upgrade (Connection: Upgrade), the daemon replies 101 UPGRADED with the
// Upgrade headers; otherwise it replies 200 OK. Either way the body that follows
// is the raw stream.
func writeRawStreamHandshake(conn net.Conn, upgrade bool) error {
	var resp string
	if upgrade {
		resp = "HTTP/1.1 101 UPGRADED\r\n" +
			"Content-Type: application/vnd.docker.raw-stream\r\n" +
			"Connection: Upgrade\r\n" +
			"Upgrade: tcp\r\n" +
			"\r\n"
	} else {
		resp = "HTTP/1.1 200 OK\r\n" +
			"Content-Type: application/vnd.docker.raw-stream\r\n" +
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
