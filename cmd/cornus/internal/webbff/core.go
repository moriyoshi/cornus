package webbff

// The reusable operation core: value-returning, context-taking methods that hold
// every client-side data join the BFF performs. Both the /.cornus/web/* HTTP
// handlers (handlers.go) and the /.cornus/mcp tools (mcp.go) are thin adapters
// over these, so the web UI and the MCP surface can never drift — they call the
// same logic.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"cornus/pkg/api"
)

// statusError carries an HTTP status alongside an error so a core operation can
// distinguish "not found" / "bad request" from the default upstream 502 without
// baking net/http status codes into every caller. The HTTP adapters map it with
// writeErr; the MCP adapters just surface the message as a tool error.
type statusError struct {
	code int
	err  error
}

func (e *statusError) Error() string { return e.err.Error() }
func (e *statusError) Unwrap() error { return e.err }

func statusErr(code int, format string, a ...any) *statusError {
	return &statusError{code: code, err: fmt.Errorf(format, a...)}
}

// writeErr renders err onto w, honoring a statusError's code and defaulting to
// 502 Bad Gateway (the code the HTTP handlers used for a failed upstream call).
func writeErr(w http.ResponseWriter, err error) {
	var se *statusError
	if errors.As(err, &se) {
		http.Error(w, se.Error(), se.code)
		return
	}
	http.Error(w, err.Error(), http.StatusBadGateway)
}

// ---- workloads ----

// Workloads joins the loaded project's services (in dependency order, including
// spec-only rows) with every deployment the server reports.
func (s *Server) Workloads(ctx context.Context) ([]webWorkload, error) {
	list, err := s.client.List(ctx)
	if err != nil {
		return nil, err
	}
	byService := s.serviceByResource()
	byResource := map[string]api.DeployStatus{}
	for _, st := range list {
		byResource[st.Name] = st
	}

	var out []webWorkload
	seen := map[string]bool{}
	for _, svc := range s.order {
		plan := s.plans[svc]
		row := webWorkload{Name: plan.Resource, Service: svc, Project: s.projectName, Image: plan.Spec.Image}
		if st, ok := byResource[plan.Resource]; ok {
			row.Created = true
			row.Backend = st.Backend
			row.Instances = st.Instances
			row.Origin = st.Origin
			row.Summary, row.Running = runningSummary(st)
			if st.Image != "" {
				row.Image = st.Image
			}
		} else {
			row.Summary = "not created"
		}
		seen[plan.Resource] = true
		out = append(out, row)
	}
	rest := make([]webWorkload, 0, len(list))
	for _, st := range list {
		if seen[st.Name] {
			continue
		}
		row := webWorkload{Name: st.Name, Service: byService[st.Name], Image: st.Image, Backend: st.Backend, Created: true, Instances: st.Instances, Origin: st.Origin}
		// This workload is not part of the loaded compose project, so there is no
		// plan to name its project — attribute it to the project it recorded at
		// deploy time (lineage), if any, instead of leaving it project-less.
		if st.Origin != nil {
			row.Project = st.Origin.Project
		}
		row.Summary, row.Running = runningSummary(st)
		rest = append(rest, row)
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	out = append(out, rest...)
	if out == nil {
		out = []webWorkload{}
	}
	return out, nil
}

// WorkloadDetail returns the spec/status/tunnel join for one workload. A name that
// is neither deployed nor part of the loaded project is a statusError(404).
func (s *Server) WorkloadDetail(ctx context.Context, name string) (webWorkloadDetail, error) {
	detail := webWorkloadDetail{Name: name}
	if svc, ok := s.serviceByResource()[name]; ok {
		detail.Service = svc
		detail.Project = s.projectName
		plan := s.plans[svc]
		detail.Spec = &plan.Spec
	}
	if st, err := s.client.Status(ctx, name); err == nil {
		detail.Status = &st
	} else if detail.Spec == nil {
		return webWorkloadDetail{}, statusErr(http.StatusNotFound, "%s", err.Error())
	}
	if ts, err := s.client.TunnelStatus(ctx, name); err == nil {
		detail.Tunnel = &ts
	}
	return detail, nil
}

// WorkloadAction starts, stops, or restarts a workload. An unknown action is a
// statusError(400).
func (s *Server) WorkloadAction(ctx context.Context, name, action string) error {
	switch action {
	case "start", "stop", "restart":
	default:
		return statusErr(http.StatusBadRequest, "unknown action %q (want start, stop, or restart)", action)
	}
	return s.client.Action(ctx, name, action)
}

// WorkloadDelete deletes a deployment by name.
func (s *Server) WorkloadDelete(ctx context.Context, name string) error {
	return s.client.Delete(ctx, name)
}

// VolumeDelete deletes a named volume.
func (s *Server) VolumeDelete(ctx context.Context, name string) error {
	return s.client.DeleteVolume(ctx, name)
}

// ---- tunnels ----

// TunnelStart opens a hosted tunnel to a workload port.
func (s *Server) TunnelStart(ctx context.Context, name string, req api.TunnelRequest) (api.TunnelStatus, error) {
	return s.client.TunnelStart(ctx, name, req)
}

// TunnelStop tears a workload's tunnel down.
func (s *Server) TunnelStop(ctx context.Context, name string) error {
	return s.client.TunnelStop(ctx, name)
}

// Tunnels lists every workload tunnel, plus the client agent's live forwards and
// conduit banners.
func (s *Server) Tunnels(ctx context.Context) (webTunnelsResponse, error) {
	resp := webTunnelsResponse{Tunnels: []webTunnel{}}
	list, err := s.client.List(ctx)
	if err != nil {
		return webTunnelsResponse{}, err
	}
	for _, st := range list {
		ts, err := s.client.TunnelStatus(ctx, st.Name)
		if err != nil {
			continue
		}
		resp.Tunnels = append(resp.Tunnels, webTunnel{Workload: st.Name, TunnelStatus: ts})
	}
	if agent := s.agent.Status(); agent != nil {
		resp.Forwards = agent.Forwards
		resp.Banners = agent.Banners
	}
	return resp, nil
}

// ---- projects ----

// Projects lists the loaded compose project and any project the client agent has
// live sessions for.
func (s *Server) Projects(ctx context.Context) []webProject {
	byName := map[string]*webProject{}
	var names []string
	if s.project != nil {
		byName[s.projectName] = &webProject{Name: s.projectName, Services: s.order, Loaded: true}
		names = append(names, s.projectName)
	}
	if agent := s.agent.Status(); agent != nil {
		for name, running := range agent.Projects {
			p, ok := byName[name]
			if !ok {
				p = &webProject{Name: name}
				byName[name] = p
				names = append(names, name)
			}
			p.Running = running
		}
	}
	sort.Strings(names)
	out := make([]webProject, 0, len(names))
	for _, n := range names {
		out = append(out, *byName[n])
	}
	return out
}

// Graph returns the dependency graph of the loaded project. Any other name is a
// statusError(404).
func (s *Server) Graph(ctx context.Context, name string) (webGraph, error) {
	if s.project == nil || name != s.projectName {
		return webGraph{}, statusErr(http.StatusNotFound, "unknown project (only the loaded compose project has a graph)")
	}
	byResource := map[string]api.DeployStatus{}
	if list, err := s.client.List(ctx); err == nil {
		for _, st := range list {
			byResource[st.Name] = st
		}
	}
	g := webGraph{Project: name, Nodes: []graphNode{}, Edges: []graphEdge{}}
	for _, svc := range s.order {
		plan := s.plans[svc]
		node := graphNode{Service: svc, Resource: plan.Resource, Image: plan.Spec.Image, Summary: "not created"}
		if st, ok := byResource[plan.Resource]; ok {
			node.Created = true
			node.Summary, node.Running = runningSummary(st)
			if st.Image != "" {
				node.Image = st.Image
			}
		}
		g.Nodes = append(g.Nodes, node)
		for _, dep := range s.project.Services()[svc].DependsOn {
			g.Edges = append(g.Edges, graphEdge{From: svc, To: dep.Service, Condition: dep.Condition, Required: dep.Required})
		}
	}
	return g, nil
}

// Apply re-deploys the loaded project by re-execing `cornus compose up -d`,
// streaming its output to w. A name other than the loaded project is a
// statusError(404). The returned error is the re-exec's own failure, after any
// partial output has been written to w.
func (s *Server) Apply(ctx context.Context, name string, w io.Writer) error {
	if s.project == nil || name != s.projectName {
		return statusErr(http.StatusNotFound, "unknown project (only the loaded compose project can be applied)")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"--output", "plain"}
	if s.cfg.ConfigPath != "" {
		args = append(args, "--config-file", s.cfg.ConfigPath)
	}
	if s.cfg.Context != "" {
		args = append(args, "--context", s.cfg.Context)
	}
	args = append(args, "compose")
	for _, f := range s.composeFiles {
		args = append(args, "-f", f)
	}
	for _, f := range s.cfg.EnvFiles {
		args = append(args, "--env-file", f)
	}
	args = append(args, "-p", s.projectName)
	if s.cfg.Host != "" {
		args = append(args, "--host", s.cfg.Host)
	}
	args = append(args, "up", "-d")

	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = s.baseDir
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// ---- mounts ----

// Mounts derives the client-side mount inventory (see webMount for the status
// mapping).
func (s *Server) Mounts(ctx context.Context) []webMount {
	byResource := map[string]api.DeployStatus{}
	if list, err := s.client.List(ctx); err == nil {
		for _, st := range list {
			byResource[st.Name] = st
		}
	}
	agentLive := map[string]bool{}
	if agent := s.agent.Status(); agent != nil {
		for _, svc := range agent.Projects[s.projectName] {
			agentLive[svc] = true
		}
	}

	out := []webMount{}
	for _, svc := range s.order {
		plan := s.plans[svc]
		status := "inactive"
		if st, ok := byResource[plan.Resource]; ok {
			if _, running := runningSummary(st); running {
				status = "running"
				if agentLive[svc] {
					status = "live"
				}
			}
		}
		for _, m := range plan.Spec.Mounts {
			out = append(out, webMount{
				Project: s.projectName, Service: svc, Workload: plan.Resource,
				Kind: "bind", Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly, Status: status,
			})
		}
		for _, v := range plan.Spec.Volumes {
			st := status
			if st == "live" {
				st = "running"
			}
			out = append(out, webMount{
				Project: s.projectName, Service: svc, Workload: plan.Resource,
				Kind: "volume", Source: v.Name, Target: v.Target, ReadOnly: v.ReadOnly, Status: st,
			})
		}
	}
	return out
}

// ---- files (the editor allow-list) ----

// Files lists the exact-path editable set, sorted by path.
func (s *Server) Files() []webFile {
	out := make([]webFile, 0, len(s.editable))
	for _, f := range s.editable {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// resolveEditable validates p against the exact-file allow-list, returning the
// cleaned absolute path. Anything not enumerated by Files — including traversal
// spellings — is a statusError(403); an empty path is a statusError(400).
func (s *Server) resolveEditable(p string) (string, error) {
	if p == "" {
		return "", statusErr(http.StatusBadRequest, "missing path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if _, ok := s.editable[abs]; !ok {
		return "", statusErr(http.StatusForbidden, "path %q is not in the editable set", p)
	}
	return abs, nil
}

// FileRead returns the contents of an allow-listed file.
func (s *Server) FileRead(path string) ([]byte, error) {
	abs, err := s.resolveEditable(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, statusErr(http.StatusNotFound, "%s", err.Error())
		}
		return nil, err
	}
	return data, nil
}

// FileWrite overwrites an allow-listed file, preserving its mode. A body larger
// than maxEditableFileSize is a statusError(413).
func (s *Server) FileWrite(path string, data []byte) error {
	abs, err := s.resolveEditable(path)
	if err != nil {
		return err
	}
	if len(data) > maxEditableFileSize {
		return statusErr(http.StatusRequestEntityTooLarge, "file too large")
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(abs); err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(abs, data, mode)
}

// ---- logs (bounded tail) ----

// LogsTail captures the last tail lines of a workload's logs (non-streaming),
// bounded to maxToolCapture bytes. This is the request/response counterpart of the
// web UI's live log WebSocket.
func (s *Server) LogsTail(ctx context.Context, name string, tail int) (string, error) {
	if tail <= 0 {
		tail = 200
	}
	var buf capBuffer
	buf.cap = maxToolCapture
	opts := api.LogOptions{
		Follow: false,
		Tail:   fmt.Sprintf("%d", tail),
		Stdout: true,
		Stderr: true,
	}
	err := s.client.Logs(ctx, name, opts, &buf)
	return buf.String(), err
}

// ---- exec (one-shot) ----

// ExecResult is the captured outcome of a one-shot ExecRun.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

// ExecRun runs cmd once inside a workload and captures its stdout, stderr, and
// exit code. It is the request/response counterpart of the web UI's interactive
// exec WebSocket: no TTY, no stdin, output bounded to maxToolCapture bytes per
// stream.
func (s *Server) ExecRun(ctx context.Context, name string, cmd []string) (ExecResult, error) {
	return execCapture(ctx, s.client, name, "", cmd)
}

// maxToolCapture bounds the bytes a bounded MCP tool (logs_tail, exec_run) buffers
// in memory, so a chatty command cannot exhaust it.
const maxToolCapture = 256 << 10

// capBuffer is a bytes.Buffer that stops accepting bytes past cap, silently
// discarding the overflow. It keeps the request/response tools bounded without
// erroring a legitimately large-but-truncatable capture.
type capBuffer struct {
	buf bytes.Buffer
	cap int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.cap - c.buf.Len(); room > 0 {
		if room < len(p) {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	// Report the full length written so the producer does not treat the cap as a
	// short-write error.
	return len(p), nil
}

func (c *capBuffer) String() string { return c.buf.String() }
