package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
	"cornus/pkg/remotecompanion"
	"cornus/pkg/wire"
)

// logStreamHandlerErr records a backend error from a streaming/tunnel handler
// (logs, exec, attach, port-forward). These endpoints upgrade to a raw tunnel or
// commit a 200 before the backend runs, so a failure can no longer be returned as
// an HTTP status — it would otherwise vanish. A client-side teardown (Ctrl-C on a
// --follow or a closed port-forward: the request context is cancelled) is expected
// and logged at Debug; anything else (RBAC denied, no pod, dial failure) is a real
// fault logged at Warn so an operator can see why, e.g., `cornus port-forward`
// silently produced nothing. op is the human label ("logs", "port-forward", ...).
func logStreamHandlerErr(r *http.Request, op, name string, err error) {
	if err == nil {
		return
	}
	ctx := r.Context()
	log := logging.FromContext(ctx)
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		log.DebugContext(ctx, "deploy "+op+" ended on client disconnect", "deployment", name, "error", err)
		return
	}
	log.WarnContext(ctx, "deploy "+op+" failed", "deployment", name, "error", err)
}

// agentForwardAllowed reports whether name's exec session on backend may
// forward an ssh-agent: either backend is RemoteCapable and currently in
// remote mode (dockerhost/containerdhost, a backend-wide toggle), or backend
// implements deploy.AgentForwardCapable and name's own applied spec opted in
// (kubernetes, a per-deployment toggle — see api.DeploySpec.AgentForward). A
// backend implementing neither (or an AgentForwardCapable error, e.g. name not
// found) means agent-forwarding is not available; err is only non-nil on an
// unexpected backend error, not a plain "not supported" answer.
func agentForwardAllowed(ctx context.Context, backend deploy.Backend, name string) (bool, error) {
	if rc, ok := backend.(deploy.RemoteCapable); ok && rc.Remote() {
		return true, nil
	}
	if afc, ok := backend.(deploy.AgentForwardCapable); ok {
		enabled, err := afc.AgentForwardEnabled(ctx, name)
		if err != nil {
			return false, err
		}
		return enabled, nil
	}
	return false, nil
}

const agentForwardNotAllowedMsg = "ssh-agent forwarding into exec requires either a remote-mode dockerhost/containerdhost backend (CORNUS_DOCKER_REMOTE / CORNUS_CONTAINERD_REMOTE) or, on kubernetes, a deployment applied with AgentForward set"

// handleDeployExecCreate serves POST /.cornus/v1/deploy/{name}/exec: it creates an exec
// in the deployment's first instance and returns the backend exec id as
// {"Id": ...} (docker exec create semantics).
func (s *Server) handleDeployExecCreate(w http.ResponseWriter, r *http.Request, backend deploy.Backend, name string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var cfg api.ExecConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid exec config: " + err.Error()})
		return
	}
	if cfg.ForwardAgent {
		allowed, err := agentForwardAllowed(r.Context(), backend, name)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !allowed {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": agentForwardNotAllowedMsg})
			return
		}
		// The exec'd process's SSH_AUTH_SOCK points at the companion's fixed
		// agent-relay socket, already visible inside the instance via its shared
		// scratch volume — no new mount is needed at exec time. The client must
		// open the matching agent channel (see handleDeployExecAgentChannel)
		// before running any command that touches the agent, or connecting to
		// the socket just fails closed.
		cfg.Env = append(cfg.Env, "SSH_AUTH_SOCK="+remotecompanion.AgentSocketPath)
	}
	execID, err := backend.ExecCreate(r.Context(), name, cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"Id": execID})
}

// handleDeployExecAgentChannel serves GET /.cornus/v1/deploy/{name}/exec-agent-channel:
// it upgrades to a yamux SERVER session (like the caretaker's own connection)
// and registers it as deployment name's currently-active forwarded-agent
// client channel (keyed by instance "name/0" — exec always targets the first
// instance) for the connection's lifetime. The caretaker's AgentRelayRole
// opens a new stream on this session for every local connection a process
// inside the instance makes to the forwarded agent socket; the CLIENT (see
// cmd/cornus/exec.go) accepts each and relays it to the real local agent.
// Only one such channel is tracked per instance at a time — a later
// --forward-agent exec session replaces the prior one's registration.
func (s *Server) handleDeployExecAgentChannel(w http.ResponseWriter, r *http.Request, backend deploy.Backend, name string) {
	allowed, err := agentForwardAllowed(r.Context(), backend, name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !allowed {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": agentForwardNotAllowedMsg})
		return
	}
	sess, err := wire.Accept(w, r)
	if err != nil {
		return
	}
	defer sess.Close()
	instance := remotecompanion.InstanceKey(name, 0)
	s.execAgentChannels.Put(instance, sess)
	defer s.execAgentChannels.Remove(instance, sess)
	<-sess.CloseChan()
}

// handleExecItem serves /.cornus/v1/deploy/exec/{id}/...:
//
//	GET /.cornus/v1/deploy/exec/{id}/json  -> exec state (docker exec inspect)
//	WS  /.cornus/v1/deploy/exec/{id}/start -> upgrade to a raw stdio tunnel and run the
//	                                   exec, after a JSON ExecStartConfig preamble
//	POST /.cornus/v1/deploy/exec/{id}/resize?h=&w= -> resize the exec's TTY (out of band)
func (s *Server) handleExecItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/.cornus/v1/deploy/exec/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing exec id"})
		return
	}
	// start (which actually runs the command in the workload) and resize are
	// gated on the "exec" action exactly like exec-create in handleDeployItem
	// ("deploy" implies it) — a leaked or guessed exec id must not bypass the
	// policy. The pure read ("json", exec inspect) stays ungated like
	// logs/stats. Checked before the WebSocket upgrade so a denied caller gets
	// a real 403.
	if (action == "start" || action == "resize") && !s.apiPolicy.AllowExec(Identity(r)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to exec"})
		return
	}
	backend, err := s.getBackend()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "deploy backend unavailable: " + err.Error()})
		return
	}

	switch action {
	case "json":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		st, err := backend.ExecInspect(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, st)
	case "start":
		conn, err := wire.AcceptConn(w, r)
		if err != nil {
			// The connection is already hijacked for the WS upgrade; nothing to
			// write back on failure.
			return
		}
		defer conn.Close()
		var cfg api.ExecStartConfig
		pc, err := readPreamble(conn, &cfg)
		if err != nil {
			return
		}
		// ExecStart bridges pc <-> the backend's exec stream until either closes.
		logStreamHandlerErr(r, "exec", id, backend.ExecStart(r.Context(), id, cfg, pc))
	case "resize":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		q := r.URL.Query()
		h, err := strconv.ParseUint(q.Get("h"), 10, 32)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid h: " + err.Error()})
			return
		}
		wd, err := strconv.ParseUint(q.Get("w"), 10, 32)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid w: " + err.Error()})
			return
		}
		if err := backend.ExecResize(r.Context(), id, uint(h), uint(wd)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown exec action: " + action})
	}
}

// handleDeployAttachStream serves WS /.cornus/v1/deploy/{name}/attach: it upgrades to a
// raw stdio tunnel, reads a JSON AttachConfig preamble, then bridges the tunnel
// to the deployment's first instance (docker attach).
func (s *Server) handleDeployAttachStream(w http.ResponseWriter, r *http.Request, backend deploy.Backend, name string) {
	conn, err := wire.AcceptConn(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	var cfg api.AttachConfig
	pc, err := readPreamble(conn, &cfg)
	if err != nil {
		return
	}
	logStreamHandlerErr(r, "attach", name, backend.Attach(r.Context(), name, cfg, pc))
}

// udpPortForwarder is the optional capability a deploy.Backend implements when
// its ForwardPort can bridge proto "udp" tunnels (framed datagrams). dockerhost
// and containerd implement it; kubernetes does not (its pods/portforward
// subresource is TCP-only).
type udpPortForwarder interface {
	SupportsUDPPortForward() bool
}

// handleDeployPortForward serves WS /.cornus/v1/deploy/{name}/portforward: it upgrades to
// a raw byte tunnel, reads a JSON PortForwardConfig preamble (the container port +
// protocol), then bridges the tunnel to that port inside the deployment's first
// instance (kubectl port-forward). A tcp tunnel carries one connection's raw byte
// stream; a udp tunnel carries length-prefixed datagram frames for one client
// flow, and is answered with a newline-JSON PortForwardAck (ok or a rejection
// when the backend cannot forward UDP) before any frames flow — tcp tunnels stay
// ack-free so the wire format is unchanged for old clients. The CLI opens a fresh
// tunnel per accepted local connection (tcp) or per client source address (udp).
func (s *Server) handleDeployPortForward(w http.ResponseWriter, r *http.Request, backend deploy.Backend, name string) {
	conn, err := wire.AcceptConn(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	var cfg api.PortForwardConfig
	pc, err := readPreamble(conn, &cfg)
	if err != nil {
		return
	}
	if cfg.Protocol == "udp" {
		var ack api.PortForwardAck
		if u, ok := backend.(udpPortForwarder); !ok || !u.SupportsUDPPortForward() {
			ack.Error = fmt.Sprintf("UDP port-forward is not supported by the %s backend (kubernetes pods/portforward is TCP-only)", backend.Name())
		}
		b, merr := json.Marshal(ack)
		if merr != nil {
			return
		}
		if _, werr := pc.Write(append(b, '\n')); werr != nil || ack.Error != "" {
			if ack.Error != "" {
				ctx := r.Context()
				logging.FromContext(ctx).WarnContext(ctx, "deploy port-forward rejected", "deployment", name, "port", cfg.Port, "proto", cfg.Protocol, "error", ack.Error)
			}
			return
		}
	}
	// A TCP tunnel is a raw passthrough with no post-preamble error channel, so a
	// setup failure here (RBAC denied on pods/portforward, no pod, dial failure)
	// cannot be reported back to the client mid-connection — it only manifests as
	// the tunnel closing. Log it server-side so the cause is not lost. (The CLI
	// prefers a direct-to-pod forward for cluster profiles precisely to avoid the
	// server ServiceAccount's typically-missing pods/portforward RBAC.)
	logStreamHandlerErr(r, "port-forward", name, backend.ForwardPort(r.Context(), name, cfg.Port, cfg.Protocol, pc))
}

// preambleConn is a net.Conn whose reads are served from a buffered reader (so
// any stream bytes buffered while reading the newline-delimited preamble are not
// lost) while writes and Close go to the underlying connection.
type preambleConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *preambleConn) Read(b []byte) (int, error) { return p.r.Read(b) }

// maxPreambleBytes bounds the newline-delimited JSON preamble read on a
// hijacked exec/attach/port-forward tunnel. The preamble is a small config
// object, so a generous fixed cap is ample; the point is only that a client
// which streams data containing no '\n' cannot make the server buffer it
// without bound (a memory-exhaustion DoS).
const maxPreambleBytes = 64 << 10 // 64 KiB

// readPreamble reads a single newline-delimited JSON preamble carrying the
// start/attach config from conn, decodes it into v, and returns a preambleConn
// positioned at the raw stream that follows (the caller bridges that conn).
//
// The read is size-bounded: the bufio.Reader is capped at maxPreambleBytes, so
// ReadSlice returns bufio.ErrBufferFull (rather than growing without bound) if
// the preamble line exceeds the cap. The same reader keeps serving the
// post-preamble raw stream, so any bytes already buffered past the newline are
// preserved.
func readPreamble(conn net.Conn, v any) (*preambleConn, error) {
	br := bufio.NewReaderSize(conn, maxPreambleBytes)
	line, err := br.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return nil, fmt.Errorf("preamble exceeds %d bytes without a newline", maxPreambleBytes)
		}
		return nil, err
	}
	if err := json.Unmarshal(line, v); err != nil {
		return nil, err
	}
	return &preambleConn{Conn: conn, r: br}, nil
}
