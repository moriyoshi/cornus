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
// Version 9: the `web-serve` and `web-stop` actions are gone, with WebSpec and
// Response.WebName. `cornus web --publish-in-conduit` hosts its own BFF and
// publishes it into whichever conduit is serving, through the rendezvous in
// pkg/conduithost — so the agent no longer hosts web UIs and no longer has an
// inventory of them. An older `cornus web` talking to a v9 agent gets "unknown
// action: web-serve", which is the right answer: it would otherwise publish a UI
// into a conduit the agent may not even be hosting.

const ProtocolVersion = 9

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

// Request is a client→agent command over the control socket. Conn/Conduit
// identify the target server and its conduit for the up/docker-serve actions;
// Project/Services/Names carry compose work; Socket/NoForwardPorts carry a docker
// frontend.
type Request struct {
	Action  string         `json:"action"` // ping|up|down|docker-serve|docker-stop|status|stop
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
	OK        bool                `json:"ok"`
	Error     string              `json:"error,omitempty"`
	Running   []string            `json:"running,omitempty"`
	Forwards  map[string][]string `json:"forwards,omitempty"`
	Statuses  map[string]string   `json:"statuses,omitempty"`
	Banners   []string            `json:"banners,omitempty"` // conduit banner (SOCKS5 proxy address) for up/docker-serve
	Warnings  []string            `json:"warnings,omitempty"`
	Protocol  int                 `json:"protocol,omitempty"`
	Inventory *Inventory          `json:"inventory,omitempty"` // for "status"
}

// Inventory is the agent's self-description, returned by the status action.
type Inventory struct {
	Servers  []string            `json:"servers,omitempty"`  // resolved endpoints in use
	Projects map[string][]string `json:"projects,omitempty"` // project -> running services
	Dockers  []string            `json:"dockers,omitempty"`  // docker frontend socket paths
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
