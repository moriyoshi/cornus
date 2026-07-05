package webbff

// The /.cornus/web/* handlers: the JSON and WebSocket surface the SPA talks to.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/coder/websocket"

	"cornus/pkg/api"
	"cornus/pkg/compose"
)

// loadProject discovers and parses the compose project, mirroring composecli's
// file discovery (compose.yaml first). A missing project is not an error — the
// UI simply has no project views.
func (s *Server) loadProject() error {
	files := s.cfg.Files
	if len(files) == 0 {
		files = DiscoverComposeFiles()
	}
	s.buildAllowList(files)
	if len(files) == 0 {
		return nil
	}

	project, err := compose.LoadWithOptions(compose.LoadOptions{EnvFiles: s.cfg.EnvFiles}, files...)
	if err != nil {
		return err
	}
	baseDir, err := filepath.Abs(filepath.Dir(files[0]))
	if err != nil {
		return err
	}
	name := s.cfg.ProjectName
	if name == "" {
		name = project.ResolveName(baseDir)
	}
	order, err := project.Order()
	if err != nil {
		return err
	}
	plans, err := project.Plan(name)
	if err != nil {
		return err
	}
	for _, plan := range plans {
		plan.ResolveMounts(baseDir)
	}

	s.project = project
	s.projectName = name
	s.plans = plans
	s.order = order
	s.baseDir = baseDir
	s.composeFiles = files
	s.buildLocalRoots()
	return nil
}

// buildAllowList records the only paths the file endpoints may read or write:
// the compose file(s), explicit env files, and the client config.
func (s *Server) buildAllowList(composeFiles []string) {
	s.editable = map[string]webFile{}
	add := func(path, label, kind string) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return
		}
		abs = filepath.Clean(abs)
		s.editable[abs] = webFile{Path: abs, Label: label, Kind: kind}
	}
	for _, f := range composeFiles {
		add(f, filepath.Base(f), "compose")
	}
	for _, f := range s.cfg.EnvFiles {
		add(f, filepath.Base(f), "env")
	}
	if cfg, err := s.resolver.AbsConfigPath(); err == nil {
		add(cfg, "client config", "clientconfig")
	}
}

// routes registers the /.cornus/web/* BFF surface on mux.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.cornus/web/config", s.handleConfig)
	mux.HandleFunc("GET /.cornus/web/workloads", s.handleWorkloads)
	mux.HandleFunc("GET /.cornus/web/workloads/{name}", s.handleWorkloadDetail)
	mux.HandleFunc("POST /.cornus/web/workloads/{name}/{action}", s.handleWorkloadAction)
	mux.HandleFunc("DELETE /.cornus/web/workloads/{name}", s.handleWorkloadDelete)
	mux.HandleFunc("POST /.cornus/web/workloads/{name}/tunnel", s.handleTunnelStart)
	mux.HandleFunc("DELETE /.cornus/web/workloads/{name}/tunnel", s.handleTunnelStop)
	mux.HandleFunc("DELETE /.cornus/web/volumes/{name}", s.handleVolumeDelete)
	mux.HandleFunc("GET /.cornus/web/projects", s.handleProjects)
	mux.HandleFunc("GET /.cornus/web/projects/{name}/graph", s.handleGraph)
	mux.HandleFunc("POST /.cornus/web/projects/{name}/apply", s.handleApply)
	mux.HandleFunc("GET /.cornus/web/mounts", s.handleMounts)
	mux.HandleFunc("GET /.cornus/web/tunnels", s.handleTunnels)
	mux.HandleFunc("GET /.cornus/web/files", s.handleFiles)
	mux.HandleFunc("GET /.cornus/web/files/content", s.handleFileRead)
	mux.HandleFunc("PUT /.cornus/web/files/content", s.handleFileWrite)
	mux.HandleFunc("GET /.cornus/web/fs", s.handleFsList)
	mux.HandleFunc("GET /.cornus/web/fs/roots", s.handleFsRoots)
	mux.HandleFunc("GET /.cornus/web/fs/stat", s.handleFsStat)
	mux.HandleFunc("GET /.cornus/web/fs/content", s.handleFsRead)
	mux.HandleFunc("PUT /.cornus/web/fs/content", s.handleFsWrite)
	mux.HandleFunc("POST /.cornus/web/fs/upload", s.handleFsUpload)
	mux.HandleFunc("POST /.cornus/web/fs/mkdir", s.handleFsMkdir)
	mux.HandleFunc("POST /.cornus/web/fs/rename", s.handleFsRename)
	mux.HandleFunc("POST /.cornus/web/fs/copy", s.handleFsCopy)
	mux.HandleFunc("DELETE /.cornus/web/fs", s.handleFsDelete)
	mux.HandleFunc("GET /.cornus/web/workloads/{name}/logs", s.handleLogsWS)
	mux.HandleFunc("GET /.cornus/web/workloads/{name}/stats", s.handleStatsWS)
	mux.HandleFunc("GET /.cornus/web/workloads/{name}/exec", s.handleExecWS)
	mux.HandleFunc("GET /.cornus/web/terminals", s.handleTermList)
	mux.HandleFunc("POST /.cornus/web/terminals", s.handleTermCreate)
	mux.HandleFunc("DELETE /.cornus/web/terminals/{id}", s.handleTermKill)
	mux.HandleFunc("GET /.cornus/web/terminals/{id}/attach", s.handleTermAttach)
}

// runningSummary formats "N/M running" for a deployment status (the same
// summary `cornus compose ps` prints).
func runningSummary(st api.DeployStatus) (string, bool) {
	running := 0
	for _, in := range st.Instances {
		if in.Running {
			running++
		}
	}
	return fmt.Sprintf("%d/%d running", running, len(st.Instances)), running > 0
}

// ---- config ----

type webConfigResponse struct {
	Endpoint     string          `json:"endpoint"`
	ConfigPath   string          `json:"configPath,omitempty"`
	Context      string          `json:"context,omitempty"`
	Contexts     []string        `json:"contexts,omitempty"`
	Server       *api.ServerInfo `json:"server,omitempty"`
	ServerError  string          `json:"serverError,omitempty"`
	Project      string          `json:"project,omitempty"`
	ComposeFiles []string        `json:"composeFiles,omitempty"`
	AgentSocket  string          `json:"agentSocket"`
	AgentLive    bool            `json:"agentLive"`
	Version      string          `json:"version"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	resp := webConfigResponse{
		Endpoint:     s.endpoint,
		Context:      s.cfg.Context,
		Project:      s.projectName,
		ComposeFiles: s.composeFiles,
		AgentSocket:  s.agent.Socket(),
		AgentLive:    s.agent.Status() != nil,
		Version:      s.cfg.Version,
	}
	if p, err := s.resolver.AbsConfigPath(); err == nil {
		resp.ConfigPath = p
	}
	if f, err := s.resolver.LoadConfig(); err == nil && f != nil {
		if resp.Context == "" {
			resp.Context = f.CurrentContext
		}
		for name := range f.Contexts {
			resp.Contexts = append(resp.Contexts, name)
		}
		sort.Strings(resp.Contexts)
	}
	if info, err := s.client.Info(r.Context()); err == nil {
		resp.Server = &info
	} else {
		resp.ServerError = err.Error()
	}
	writeJSON(w, resp)
}

// ---- workloads ----

type webWorkload struct {
	Name      string               `json:"name"`
	Service   string               `json:"service,omitempty"`
	Project   string               `json:"project,omitempty"`
	Image     string               `json:"image,omitempty"`
	Backend   string               `json:"backend,omitempty"`
	Summary   string               `json:"summary"`
	Created   bool                 `json:"created"`
	Running   bool                 `json:"running"`
	Instances []api.InstanceStatus `json:"instances,omitempty"`
	// Origin is the deployment's recorded lineage (project, client host/user/dir/
	// git, and the authenticated subject), present only on deployed rows the
	// server reports it for.
	Origin *api.Origin `json:"origin,omitempty"`
}

// serviceByResource maps a deployment resource name ("<project>-<service>")
// back to its compose service name.
func (s *Server) serviceByResource() map[string]string {
	m := map[string]string{}
	for svc, plan := range s.plans {
		m[plan.Resource] = svc
	}
	return m
}

func (s *Server) handleWorkloads(w http.ResponseWriter, r *http.Request) {
	out, err := s.Workloads(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

type webWorkloadDetail struct {
	Name    string            `json:"name"`
	Service string            `json:"service,omitempty"`
	Project string            `json:"project,omitempty"`
	Status  *api.DeployStatus `json:"status,omitempty"`
	Spec    *api.DeploySpec   `json:"spec,omitempty"`
	Tunnel  *api.TunnelStatus `json:"tunnel,omitempty"`
}

func (s *Server) handleWorkloadDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := s.WorkloadDetail(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, detail)
}

func (s *Server) handleWorkloadAction(w http.ResponseWriter, r *http.Request) {
	if err := s.WorkloadAction(r.Context(), r.PathValue("name"), r.PathValue("action")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) handleWorkloadDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.WorkloadDelete(r.Context(), r.PathValue("name")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) handleVolumeDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.VolumeDelete(r.Context(), r.PathValue("name")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

// ---- tunnels ----

func (s *Server) handleTunnelStart(w http.ResponseWriter, r *http.Request) {
	var req api.TunnelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid tunnel request: "+err.Error(), http.StatusBadRequest)
		return
	}
	st, err := s.TunnelStart(r.Context(), r.PathValue("name"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, st)
}

func (s *Server) handleTunnelStop(w http.ResponseWriter, r *http.Request) {
	if err := s.TunnelStop(r.Context(), r.PathValue("name")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

type webTunnel struct {
	Workload string `json:"workload"`
	api.TunnelStatus
}

type webTunnelsResponse struct {
	Tunnels  []webTunnel         `json:"tunnels"`
	Forwards map[string][]string `json:"forwards,omitempty"` // live local port-forwards per service (client agent)
	Banners  []string            `json:"banners,omitempty"`  // conduit banners (SOCKS5 proxy lines)
}

func (s *Server) handleTunnels(w http.ResponseWriter, r *http.Request) {
	resp, err := s.Tunnels(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, resp)
}

// ---- projects ----

type webProject struct {
	Name     string   `json:"name"`
	Services []string `json:"services,omitempty"` // dependency order; only known for the loaded project
	Running  []string `json:"running,omitempty"`  // services with a live client-agent session
	Loaded   bool     `json:"loaded"`             // true for the project this `cornus web` loaded
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Projects(r.Context()))
}

// ---- dependency graph ----

type graphNode struct {
	Service  string `json:"service"`
	Resource string `json:"resource"`
	Image    string `json:"image,omitempty"`
	Summary  string `json:"summary"`
	Running  bool   `json:"running"`
	Created  bool   `json:"created"`
}

type graphEdge struct {
	From      string `json:"from"` // the dependent service
	To        string `json:"to"`   // its dependency
	Condition string `json:"condition,omitempty"`
	Required  bool   `json:"required"`
}

type webGraph struct {
	Project string      `json:"project"`
	Nodes   []graphNode `json:"nodes"`
	Edges   []graphEdge `json:"edges"`
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	g, err := s.Graph(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, g)
}

// handleApply re-deploys the loaded project after an edit by re-execing this
// binary as `cornus compose up -d`: the exact `up` semantics (reconcile,
// background agent handoff for mounts/conduit) without duplicating composecli's
// CLI-coupled reconcile engine. Output streams back as text/plain.
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// Validate up front: once output streams as 200, a later 404 cannot be sent.
	if s.project == nil || name != s.projectName {
		http.Error(w, "unknown project (only the loaded compose project can be applied)", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fw := &flushWriter{w: w}
	if err := s.Apply(r.Context(), name, fw); err != nil {
		fmt.Fprintf(fw, "\napply failed: %v\n", err)
	}
}

// flushWriter flushes after every write so `compose up` progress streams to
// the browser as it happens.
type flushWriter struct{ w http.ResponseWriter }

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

// ---- mounts ----

// webMount is one mount of one workload, with a status DERIVED client-side —
// there is no MountStatus in the API. The mapping (kept deliberately honest):
//   - "live":     the client agent holds a session for the service (client-local
//     9P mounts are only alive while that session is).
//   - "running":  the workload has a running instance but no agent session
//     (server-side bind/volume: realized by the backend, nothing to relay).
//   - "inactive": the workload is not running (or not created).
type webMount struct {
	Project  string `json:"project,omitempty"`
	Service  string `json:"service,omitempty"`
	Workload string `json:"workload"`
	Kind     string `json:"kind"`             // "bind" or "volume"
	Source   string `json:"source,omitempty"` // bind source path, or volume name ("" = anonymous)
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly,omitempty"`
	Status   string `json:"status"` // live | running | inactive
}

func (s *Server) handleMounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Mounts(r.Context()))
}

// ---- files (the CodeMirror editor) ----

type webFile struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // compose | env | clientconfig
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Files())
}

func (s *Server) handleFileRead(w http.ResponseWriter, r *http.Request) {
	data, err := s.FileRead(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

// maxEditableFileSize bounds a PUT body; compose/env/config files are tiny.
const maxEditableFileSize = 10 << 20

func (s *Server) handleFileWrite(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxEditableFileSize+1))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.FileWrite(r.URL.Query().Get("path"), data); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

// ---- streaming (WebSocket) ----

// acceptWS upgrades r to a WebSocket. coder/websocket's default origin check
// (Origin host must equal the request Host) is exactly the same-origin policy
// the loopback-only UI needs, so no OriginPatterns are set.
func acceptWS(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	return websocket.Accept(w, r, nil)
}

// wsWriter adapts a WebSocket to io.Writer: each Write becomes one binary
// message. Binary (not text) because log/stats payloads are not guaranteed
// valid UTF-8 per write boundary.
type wsWriter struct {
	ctx  context.Context
	conn *websocket.Conn
}

func (w wsWriter) Write(p []byte) (int, error) {
	if err := w.conn.Write(w.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Server) handleLogsWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	q := r.URL.Query()
	opts := api.LogOptions{
		Follow:     q.Get("follow") != "false",
		Tail:       q.Get("tail"),
		Timestamps: q.Get("timestamps") == "true",
		Since:      q.Get("since"),
		Stdout:     true,
		Stderr:     true,
	}
	if opts.Tail == "" {
		opts.Tail = "500"
	}
	conn, err := acceptWS(w, r)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	// A follow stream ends when the client goes away (ctx) or the backend closes.
	err = s.client.Logs(ctx, name, opts, wsWriter{ctx: ctx, conn: conn})
	closeWS(conn, err)
}

func (s *Server) handleStatsWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	conn, err := acceptWS(w, r)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	err = s.client.Stats(ctx, name, api.StatsOptions{Stream: true}, wsWriter{ctx: ctx, conn: conn})
	closeWS(conn, err)
}

// closeWS closes the socket, carrying a stream error to the browser in the
// close reason (truncated to the 125-byte close-frame budget).
func closeWS(conn *websocket.Conn, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	reason := err.Error()
	if len(reason) > 120 {
		reason = reason[:120]
	}
	_ = conn.Close(websocket.StatusInternalError, reason)
}

// execControl is a text-frame control message from the terminal: currently
// only resize. Binary frames are raw stdin bytes.
type execControl struct {
	Resize *struct {
		H uint `json:"h"`
		W uint `json:"w"`
	} `json:"resize"`
}

// handleExecWS runs an interactive exec inside the workload, bridging the
// browser's xterm.js to the server exec stream: binary WS frames are raw
// stdin/stdout bytes, text frames carry JSON control (resize).
func (s *Server) handleExecWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	q := r.URL.Query()
	cmd := q["cmd"]
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh"}
	}
	ctx := r.Context()

	execID, err := s.client.ExecCreate(ctx, name, api.ExecConfig{
		Cmd: cmd, Tty: true, AttachStdin: true, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	stream, err := s.client.ExecStart(ctx, execID, api.ExecStartConfig{Tty: true})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer stream.Close()

	conn, err := acceptWS(w, r)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	if h, errH := strconv.Atoi(q.Get("h")); errH == nil {
		if wd, errW := strconv.Atoi(q.Get("w")); errW == nil {
			_ = s.client.ExecResize(ctx, execID, uint(h), uint(wd))
		}
	}

	// Browser -> workload: binary frames are stdin, text frames are control.
	go func() {
		defer stream.Close()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if _, err := stream.Write(data); err != nil {
					return
				}
			case websocket.MessageText:
				var ctl execControl
				if json.Unmarshal(data, &ctl) == nil && ctl.Resize != nil {
					_ = s.client.ExecResize(ctx, execID, ctl.Resize.H, ctl.Resize.W)
				}
			}
		}
	}()

	// Workload -> browser.
	buf := make([]byte, 32<<10)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			closeWS(conn, nil)
			return
		}
	}
}
