// Package clientagent is the unified client-side background agent: one process,
// reached over a single control socket, that hosts every client-held workload
// session for the developer — compose projects (client-local 9P mounts + port
// conduit) today, and the Docker API proxy frontend next. It replaces the former
// per-project `cornus daemon mounts` and standalone `cornus daemon docker`
// daemons. This file defines the control-socket wire protocol and the client
// stub used to reach the agent.
package clientagent

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"cornus/pkg/api"
)

// ProtocolVersion is stamped on every Response. The agent is always re-exec'd
// from the same binary as the client, so the protocol may evolve freely; the
// version only matters when a long-lived agent spawned by an OLDER binary is
// still running after the binary was replaced (its replies carry Protocol 0),
// letting a newer client detect it and warn.
//
// Version 2: `up` recreates a running service whose spec fingerprint changed and
// reports per-service Statuses (inherited from the compose mounts daemon).
//
// Version 3: adds web-serve/web-stop — the agent hosts the web UI's BFF and
// publishes it in the shared conduit. A v2 agent left running by an older binary
// answers web-serve with "unknown action", so a v3 client checks Ping().Protocol
// before publishing and reports the stale agent instead.
//
// Version 4: adds project configuration warnings and explicit conduit wire types.
//
// Version 5: carries ingress server-certificate selection rules.
//
// Version 6: adds `up --watch` — an up may carry Watch/WatchFiles/Reload so the
// agent watches the project's compose + env files and re-execs the CLI to reload
// on edit. A Watch up is also the complete desired set (the agent prunes held
// services absent from it). A v5 agent left running by an older binary simply
// ignores the new fields (no watch), which a v6 client can detect via
// Ping().Protocol.
//
// Version 7: an `up` for a project the agent already holds is reconciled against
// the request instead of silently keeping the first writer's settings. A changed
// conduit configuration (including the whole ingress configuration: mode, suffix,
// CA, certificates, native controller) is reconciled in place and reported as a
// warning; a changed server connection is refused with an error naming what
// differs. A v6 agent left running by an older binary still has the first-writer
// behavior, which a v7 client detects via Ping().Protocol and reports.
//
// Version 8: `web-serve` may ask to JOIN a shared socks5 conduit the agent
// already runs for the connection (Web.JoinConduit), which demotes Request.Conduit
// to a fallback used only when there is none; it may leave Web.Name empty for the
// agent to derive from the conduit it actually joined, and the agent reports what
// it published in Response.WebName. A v7 agent ignores JoinConduit and rejects an
// empty Web.Name with "web-serve: missing name" — which a v8 client never reaches,
// because `cornus web` already refuses an agent older than ProtocolVersion before
// publishing (cmd/cornus/web.go). The practical consequence is that a live v7
// agent must be stopped (`cornus daemon stop`) before the first publish.
const ProtocolVersion = 8

// Per-service outcome of an `up` request, reported in Response.Statuses.
const (
	StatusStarted   = "started"    // fresh session opened
	StatusUpToDate  = "up-to-date" // already running an identical spec; left alone
	StatusRecreated = "recreated"  // was running a different spec; torn down and restarted
)

// Service is one workload the agent should hold a session for: a deploy-attach
// session for client-local mounts, local port conduit for published ports
// (ForwardPorts), or both. ForwardOnly marks a service the client already
// deployed fire-and-forget, so the agent only holds its conduit and opens no
// deploy-attach session. The DeploySpec's mount sources are already absolute
// (resolved client-side).
type Service struct {
	Name         string         `json:"name"`
	Spec         api.DeploySpec `json:"spec"`
	ForwardPorts bool           `json:"forwardPorts,omitempty"`
	ForwardOnly  bool           `json:"forwardOnly,omitempty"`
}

// WebSpec is one web UI to host: the agent builds the BFF from these (already
// absolute, since the agent's cwd is frozen at spawn) and publishes it in the
// shared conduit under Name:Port, reached through the proxy with no bound port.
type WebSpec struct {
	Files       []string `json:"files,omitempty"`       // compose file(s), absolute
	EnvFiles    []string `json:"envFiles,omitempty"`    // env file(s), absolute
	ProjectName string   `json:"projectName,omitempty"` // compose project name override
	Frontend    string   `json:"frontend,omitempty"`    // detached frontend dev-server URL
	// Name is the conduit host to publish under (e.g. cornus.internal). EMPTY asks
	// the agent to derive the default from the conduit it actually publishes in —
	// the only place that answer is knowable, because with JoinConduit the adopted
	// conduit's service-host suffix need not be the one this client resolved. A
	// non-empty Name is `--publish-name` and always wins.
	Name string `json:"name"`
	Port int    `json:"port"` // conduit port the name answers on
	// JoinConduit asks the agent to publish this UI in a SHARED socks5 conduit it
	// ALREADY runs for this connection, rather than starting a second one from
	// Request.Conduit — which is then only the FALLBACK, used when there is nothing
	// to join. It is set when the caller did not PIN conduit settings on the command
	// line; a caller that named an address or a suffix leaves it false and gets
	// exact-identity sharing, as before.
	//
	// It lives here rather than beside Request.Conduit because `up` and
	// `docker-serve` read that field too and gain no such behavior: a published web
	// UI is the one frontend with no legitimate opinion about listen/suffix/ingress,
	// since a browser has exactly one proxy setting to point at both it and the
	// workloads.
	JoinConduit bool   `json:"joinConduit,omitempty"`
	Version     string `json:"version,omitempty"` // cornus version string shown in the UI
	MCP         bool   `json:"mcp,omitempty"`     // co-host the MCP server at /.cornus/mcp
	// LocalRoots are extra file-explorer roots (`cornus web --local-root`), paths
	// already absolute: the agent's working directory is frozen at spawn and is
	// not the caller's, so a relative path here would resolve somewhere nobody
	// named. Parsed and validated client-side before it is sent.
	LocalRoots []WebLocalRoot `json:"localRoots,omitempty"`
}

// WebLocalRoot is one entry of WebSpec.LocalRoots.
type WebLocalRoot struct {
	Label    string `json:"label,omitempty"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// Request is a client→agent command over the control socket. Conn/Conduit
// identify the target server and its conduit for the up/docker-serve/web-serve
// actions; Project/Services/Names carry compose work; Socket/NoForwardPorts carry
// a docker frontend; Web carries a web UI to host.
type Request struct {
	Action  string         `json:"action"` // ping|up|down|docker-serve|docker-stop|web-serve|status|stop
	Conn    ConnSpec       `json:"conn,omitempty"`
	Conduit WireConduitCfg `json:"conduit,omitempty"`
	// compose
	Project  string    `json:"project,omitempty"`
	Services []Service `json:"services,omitempty"`
	Names    []string  `json:"names,omitempty"` // for "down"; empty = the whole project
	// watch (up --watch): Watch marks this up as a watched project — the agent
	// watches WatchFiles and re-execs Reload on edit, and treats Services as the
	// COMPLETE desired set (pruning held services absent from it). WatchFiles are
	// absolute (the agent's cwd is frozen). Set on both the initial up and each
	// reload re-exec.
	Watch      bool        `json:"watch,omitempty"`
	WatchFiles []string    `json:"watchFiles,omitempty"`
	Reload     *ReloadSpec `json:"reload,omitempty"`
	// docker
	Socket         string `json:"socket,omitempty"`
	NoForwardPorts bool   `json:"noForwardPorts,omitempty"`
	// web
	Web WebSpec `json:"web,omitempty"`
}

// ReloadSpec is how the agent re-invokes the compose CLI to reload a watched
// project after an edit: it re-execs this binary with Argv (the original
// `compose ... up -d --watch ...` arguments) in working directory Dir with
// environment Env — all captured on the ORIGINAL client so the re-plan sees the
// same cwd/env the developer ran in (variable interpolation, relative
// --env-file, KUBECONFIG, and CORNUS_AGENT_DIR, which targets THIS agent). The
// re-exec reconnects to the same agent and sends a fresh up, which reconciles.
type ReloadSpec struct {
	Argv []string `json:"argv"`
	Dir  string   `json:"dir"`
	Env  []string `json:"env"`
}

// Response is the agent's reply. Forwards reports the live local port-forwards
// per service (e.g. "127.0.0.1:8080 -> :80"). Statuses reports, per service of an
// "up" request, whether the session was started, kept (up-to-date), or recreated
// (Status* values). Protocol is ProtocolVersion; 0 (absent) marks an agent from
// an older build.
type Response struct {
	OK       bool                `json:"ok"`
	Error    string              `json:"error,omitempty"`
	Running  []string            `json:"running,omitempty"`
	Forwards map[string][]string `json:"forwards,omitempty"`
	Statuses map[string]string   `json:"statuses,omitempty"`
	Banners  []string            `json:"banners,omitempty"` // conduit banner (SOCKS5 proxy address) for up/docker-serve
	Warnings []string            `json:"warnings,omitempty"`
	// WebName is the conduit name a web-serve actually published under. The client
	// prints it, so what the user is told is what the agent did rather than what the
	// client guessed before it knew which conduit would be adopted.
	WebName   string     `json:"webName,omitempty"`
	Protocol  int        `json:"protocol,omitempty"`
	Inventory *Inventory `json:"inventory,omitempty"` // for "status"
}

// Inventory is the agent's self-description, returned by the status action.
type Inventory struct {
	Servers  []string            `json:"servers,omitempty"`  // resolved endpoints in use
	Projects map[string][]string `json:"projects,omitempty"` // project -> running services
	Dockers  []string            `json:"dockers,omitempty"`  // docker frontend socket paths
	Webs     []string            `json:"webs,omitempty"`     // published web UI names (e.g. cornus.internal:80)
	Banners  []string            `json:"banners,omitempty"`  // conduit banners (SOCKS5 proxy lines)
	// Ingress describes how THIS client realizes ingress, one entry per live
	// conduit that routes any (deduplicated across conduits sharing settings).
	// Empty means no conduit routes ingress — which is a real answer, and a
	// different one from "no agent answered at all", where there is no Inventory
	// to read in the first place. A consumer must keep those two apart.
	Ingress []AgentIngress `json:"ingress,omitempty"`
}

// AgentIngress is one live conduit's ingress settings: how the CLIENT realizes
// ingress, as opposed to the front door the SERVER advertises in
// api.ServerInfo.Ingress. The two are independent — a cluster with a real
// controller can still be reached through a client-side emulated proxy — so a
// reader needs both to know what a request to an ingress host actually traverses.
type AgentIngress struct {
	// Mode is "native" (transparent tunnel to the real cluster ingress controller)
	// or "emulate" (client-side HTTP(S) reverse proxy). Never empty: a conduit with
	// ingress off contributes no entry at all rather than an entry saying nothing.
	Mode string `json:"mode,omitempty"`
	// Domain is the suffix ingress hosts are derived under (IngressConfig.SuffixDomain).
	// Empty means the conduit falls back to its service-host suffix stem.
	Domain string `json:"domain,omitempty"`
	// Controller is the native-passthrough target. Nil in emulate mode, where there
	// is no controller to reach.
	Controller *AgentIngressController `json:"controller,omitempty"`
	// Trust is the conduit's own account of the TLS material a browser has to accept
	// (clientconduit.Conduit.CAInfo): the emulated per-host certificates and the
	// fallback CA. Empty in native mode, where the real controller serves its own.
	//
	// Human-readable lines, not paths to parse — and deliberately NOT the CA private
	// key's location, which CAInfo also omits and the auth-less web UI has no use for.
	Trust []string `json:"trust,omitempty"`
}

// AgentIngressController names the cluster ingress controller a native-mode
// conduit tunnels to. It mirrors clientconfig.IngressController.
type AgentIngressController struct {
	KubeContext string `json:"kubeContext,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Service     string `json:"service,omitempty"`
	HTTPPort    int    `json:"httpPort,omitempty"`
	HTTPSPort   int    `json:"httpsPort,omitempty"`
}

// Send sends one request to the agent on socket and returns its response.
func Send(socket string, req Request) (*Response, error) {
	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute)) // deploy-attach ready can be slow (image pull)
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SendHold sends req and returns the ack together with the still-open connection.
// It is for an action (web-serve) whose registration lives exactly as long as the
// caller holds the connection: closing it is the withdrawal signal the agent waits
// on. Unlike Send it sets no deadline and does not close the connection on
// success; the caller closes it. A non-OK ack closes the connection and returns an
// error.
func SendHold(socket string, req Request) (*Response, net.Conn, error) {
	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return nil, nil, err
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if !resp.OK {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("%s", resp.Error)
	}
	return &resp, conn, nil
}

// Ping reports whether a live agent answers on socket, returning its reply (nil
// when none answers) so callers can inspect the Protocol version.
func Ping(socket string) *Response {
	resp, err := Send(socket, Request{Action: "ping"})
	if err != nil || !resp.OK {
		return nil
	}
	return resp
}

// WaitReady polls until the agent answers ping or the deadline passes.
func WaitReady(socket string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if Ping(socket) != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("agent did not become ready on %s", socket)
}
