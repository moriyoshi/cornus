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
const ProtocolVersion = 3

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
	Name        string   `json:"name"`                  // conduit host to publish under (e.g. cornus.internal)
	Port        int      `json:"port"`                  // conduit port the name answers on
	Version     string   `json:"version,omitempty"`     // cornus version string shown in the UI
	MCP         bool     `json:"mcp,omitempty"`         // co-host the MCP server at /.cornus/mcp
}

// Request is a client→agent command over the control socket. Conn/Conduit
// identify the target server and its conduit for the up/docker-serve/web-serve
// actions; Project/Services/Names carry compose work; Socket/NoForwardPorts carry
// a docker frontend; Web carries a web UI to host.
type Request struct {
	Action  string     `json:"action"` // ping|up|down|docker-serve|docker-stop|web-serve|status|stop
	Conn    ConnSpec   `json:"conn,omitempty"`
	Conduit ConduitCfg `json:"conduit,omitempty"`
	// compose
	Project  string    `json:"project,omitempty"`
	Services []Service `json:"services,omitempty"`
	Names    []string  `json:"names,omitempty"` // for "down"; empty = the whole project
	// docker
	Socket         string `json:"socket,omitempty"`
	NoForwardPorts bool   `json:"noForwardPorts,omitempty"`
	// web
	Web WebSpec `json:"web,omitempty"`
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
	Protocol  int                 `json:"protocol,omitempty"`
	Inventory *Inventory          `json:"inventory,omitempty"` // for "status"
}

// Inventory is the agent's self-description, returned by the status action.
type Inventory struct {
	Servers  []string            `json:"servers,omitempty"`  // resolved endpoints in use
	Projects map[string][]string `json:"projects,omitempty"` // project -> running services
	Dockers  []string            `json:"dockers,omitempty"`  // docker frontend socket paths
	Webs     []string            `json:"webs,omitempty"`     // published web UI names (e.g. cornus.internal:80)
	Banners  []string            `json:"banners,omitempty"`  // conduit banners (SOCKS5 proxy lines)
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
