// Package e2e is a Starlark-powered end-to-end test harness for cornus. A
// scenario is a .star file that drives a real cornus server (and the
// `cornus compose` client) against a Target — a Docker host or a kind-managed
// Kubernetes cluster — using builtins like serve(), build(), deploy(), wait(),
// compose_up(), registry_roundtrip(), and assert_eq().
package e2e

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
	"golang.org/x/net/proxy"

	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/compose"
)

// scenarioFileOptions make the scenario DSL imperative-friendly: for/if at the
// top level and reassignment of top-level names (a scenario naturally writes
// `st = wait(...)` more than once). Check and RunFile share these, so a --check
// pass predicts exactly what RunFile will accept.
var scenarioFileOptions = &syntax.FileOptions{
	TopLevelControl: true,
	GlobalReassign:  true,
}

// Harness runs Starlark scenarios against a Target.
type Harness struct {
	target    Target
	cornusBin string
	storage   string
	out       io.Writer

	ctx          context.Context
	registryHost string
	client       *client.Client
	server       *exec.Cmd

	sshAuthSock string // set by ssh_agent()
	sshAgentPID string // killed on teardown

	sshd     *exec.Cmd // background sshd started by sshd(), killed on teardown
	dataRoot string    // temp root for isolated per-role data dirs
	buildSeq int       // counter for fresh_cache isolated build data dirs

	advertiseURL string                // server URL an in-cluster pod dials (kube mount relay)
	attaches     map[string]*attach    // long-lived deploy_attach processes, by name
	composeUps   map[string]*bgCompose // backgrounded foreground `compose up` processes, by handle

	dockerd     *exec.Cmd // `cornus daemon docker` proxy process (dockerd_up), killed on teardown
	dockerdSock string    // its unix socket path

	// agentDir isolates this scenario's unified client agent (CORNUS_AGENT_DIR),
	// set at serve() and `cornus daemon stop`-ped on teardown so `compose up -d`
	// and `dockerd_up` share one agent per scenario, never across scenarios.
	agentDir string

	portForwards []*exec.Cmd // background `cornus port-forward` processes, killed on teardown

	webs []*exec.Cmd // background `cornus web` processes (web()), killed on teardown

	frontendStubs map[string]*frontendStub // in-process stub frontend dev servers (frontend_stub), by address; closed on teardown

	egressProxies map[string]*egressProxy // in-process recording proxies (egress_proxy), by address; closed on teardown

	traceSinks map[string]*traceSink // in-process trace-recording HTTP sinks (trace_sink), by address; closed on teardown

	otlpCollectors map[string]*otlpCollector // in-process OTLP/HTTP trace receivers (otlp_collector), by address; closed on teardown

	upstreamCleanups []func() // in-process upstream registries (upstream_registry), closed on teardown
}

// advertiser is an optional Target that knows how an in-cluster pod can reach a
// host-run cornus server (the kind gateway) — needed for the k8s mount relay.
type advertiser interface{ AdvertiseHost() string }

// attach is a running `cornus deploy --server ... --local-mount` process.
type attach struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// bgCompose is a backgrounded FOREGROUND `cornus compose up` (no -d). It exists
// so a scenario can prove a foreground up self-terminates when its workloads are
// removed elsewhere (e.g. a `compose down` from another terminal): its combined
// output is captured (buf) and its exit code recorded so the scenario can wait
// for it to exit and assert on the exit banner. buf/code are read only after
// done is closed (the cmd.Wait goroutine), so no locking is needed. exec keeps
// writes to buf from a single goroutine because Stdout and Stderr are the same
// writer value.
type bgCompose struct {
	cmd  *exec.Cmd
	buf  *bytes.Buffer
	done chan struct{}
	code int
}

// dataDir returns an isolated data dir for a role ("server", "build"), creating
// it under a per-harness temp root. The served cornus and local `cornus
// build` MUST use different data dirs: BuildKit's boltdb takes an exclusive,
// no-timeout file lock on cache.db, so sharing one deadlocks a local build the
// moment the long-running server holds the lock (its engine spins up on the
// first remote build).
func (h *Harness) dataDir(role string) (string, error) {
	if h.dataRoot == "" {
		root, err := os.MkdirTemp("", "cornus-e2e-data-")
		if err != nil {
			return "", err
		}
		h.dataRoot = root
	}
	p := filepath.Join(h.dataRoot, role)
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", err
	}
	return p, nil
}

// New creates a harness driving the given cornus binary against target (the
// compose client and Docker API proxy are subcommands of the same binary).
// storage is the default registry storage backend for serve() (e.g. "mem://").
func New(target Target, cornusBin, storage string, out io.Writer) *Harness {
	if storage == "" {
		storage = "mem://"
	}
	return &Harness{target: target, cornusBin: cornusBin, storage: storage, out: out}
}

// Check parses AND resolves a scenario without executing it, so structural
// errors (e.g. a top-level for/if, which Starlark forbids) and undefined-name
// typos are caught up front — not just tokenizer/parse errors. Resolution needs
// the predeclared builtin names; universe names (len, range, True, ...) are
// known to the resolver.
func Check(path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	names := predeclaredNames()
	_, _, err = starlark.SourceProgramOptions(scenarioFileOptions, path, src, func(n string) bool { return names[n] })
	return err
}

// predeclaredNames is the set of globals a scenario may reference. It MUST stay
// in sync with the keys of predeclared(); TestPredeclaredNamesInSync enforces it.
func predeclaredNames() map[string]bool {
	names := []string{
		"TARGET", "log", "sleep", "serve", "stop_server", "build", "ssh_agent", "sshd", "deploy",
		"deploy_attach", "attach_stop", "pod_exec",
		"status", "stats", "wait", "start", "stop", "restart", "remove",
		"compose_up", "compose_ps", "compose_down",
		"compose_build", "compose_stop", "compose_start", "compose_restart",
		"compose_up_bg", "compose_up_wait", "compose_up_stop",
		"devcontainer_up", "devcontainer_ps", "devcontainer_down",
		"registry_roundtrip", "upstream_registry", "build_upload", "cornus", "dockerd_up", "docker_compose", "devcontainer_cli", "port_forward", "tunnel", "free_port",
		"web", "frontend_stub",
		"egress_proxy", "egress_proxy_hits",
		"trace_sink", "trace_sink_headers",
		"otlp_collector", "otlp_spans",
		"http_get", "http", "ftp_roundtrip", "sh", "exec_tty", "write_file", "read_file", "temp_dir", "kubectl", "docker", "kind",
		"getenv",
		"now", "benchmark", "bench_record",
		"assert_eq", "assert_true", "assert_contains", "fail",
	}
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// RunFile executes a scenario file. The cornus server it starts is stopped
// when the scenario finishes.
func (h *Harness) RunFile(ctx context.Context, path string) error {
	h.ctx = ctx
	defer h.stopUpstreams()
	defer h.stopOTLPCollectors()
	defer h.stopTraceSinks()
	defer h.stopFrontendStubs()
	defer h.stopEgressProxies()
	defer h.stopWebs()
	defer h.stopPortForwards()
	defer h.stopServer()
	thread := &starlark.Thread{
		Name:  path,
		Print: func(_ *starlark.Thread, msg string) { fmt.Fprintln(h.out, msg) },
	}
	_, err := starlark.ExecFileOptions(scenarioFileOptions, thread, path, nil, h.predeclared())
	return err
}

func (h *Harness) logf(format string, a ...any) {
	fmt.Fprintf(h.out, format+"\n", a...)
}

func (h *Harness) predeclared() starlark.StringDict {
	bi := func(name string, fn func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error)) *starlark.Builtin {
		return starlark.NewBuiltin(name, fn)
	}
	d := starlark.StringDict{
		"TARGET":             starlark.String(h.target.Name()),
		"log":                bi("log", h.bLog),
		"sleep":              bi("sleep", h.bSleep),
		"serve":              bi("serve", h.bServe),
		"stop_server":        bi("stop_server", h.bStopServer),
		"build":              bi("build", h.bBuild),
		"ssh_agent":          bi("ssh_agent", h.bSSHAgent),
		"sshd":               bi("sshd", h.bSSHD),
		"deploy":             bi("deploy", h.bDeploy),
		"deploy_attach":      bi("deploy_attach", h.bDeployAttach),
		"attach_stop":        bi("attach_stop", h.bAttachStop),
		"pod_exec":           bi("pod_exec", h.bPodExec),
		"status":             bi("status", h.bStatus),
		"stats":              bi("stats", h.bStats),
		"wait":               bi("wait", h.bWait),
		"start":              bi("start", h.action("start")),
		"stop":               bi("stop", h.action("stop")),
		"restart":            bi("restart", h.action("restart")),
		"remove":             bi("remove", h.bRemove),
		"compose_up":         bi("compose_up", h.compose("up")),
		"compose_ps":         bi("compose_ps", h.compose("ps")),
		"compose_down":       bi("compose_down", h.compose("down")),
		"compose_build":      bi("compose_build", h.compose("build")),
		"compose_stop":       bi("compose_stop", h.compose("stop")),
		"compose_start":      bi("compose_start", h.compose("start")),
		"compose_restart":    bi("compose_restart", h.compose("restart")),
		"compose_up_bg":      bi("compose_up_bg", h.bComposeUpBg),
		"compose_up_wait":    bi("compose_up_wait", h.bComposeUpWait),
		"compose_up_stop":    bi("compose_up_stop", h.bComposeUpStop),
		"devcontainer_up":    bi("devcontainer_up", h.devcontainer("up")),
		"devcontainer_ps":    bi("devcontainer_ps", h.devcontainer("ps")),
		"devcontainer_down":  bi("devcontainer_down", h.devcontainer("down")),
		"registry_roundtrip": bi("registry_roundtrip", h.bRegistryRoundtrip),
		"upstream_registry":  bi("upstream_registry", h.bUpstreamRegistry),
		"build_upload":       bi("build_upload", h.bBuildUpload),
		"cornus":             bi("cornus", h.bCornus),
		"dockerd_up":         bi("dockerd_up", h.bDockerdUp),
		"port_forward":       bi("port_forward", h.bPortForward),
		"tunnel":             bi("tunnel", h.bTunnel),
		"web":                bi("web", h.bWeb),
		"frontend_stub":      bi("frontend_stub", h.bFrontendStub),
		"free_port":          bi("free_port", h.bFreePort),
		"egress_proxy":       bi("egress_proxy", h.bEgressProxy),
		"egress_proxy_hits":  bi("egress_proxy_hits", h.bEgressProxyHits),
		"trace_sink":         bi("trace_sink", h.bTraceSink),
		"trace_sink_headers": bi("trace_sink_headers", h.bTraceSinkHeaders),
		"otlp_collector":     bi("otlp_collector", h.bOTLPCollector),
		"otlp_spans":         bi("otlp_spans", h.bOTLPSpans),
		"docker_compose":     bi("docker_compose", h.bDockerCompose),
		"devcontainer_cli":   bi("devcontainer_cli", h.bDevcontainerCLI),
		"http_get":           bi("http_get", h.bHTTPGet),
		"http":               bi("http", h.bHTTP),
		"ftp_roundtrip":      bi("ftp_roundtrip", h.bFTPRoundtrip),
		"sh":                 bi("sh", h.bSh),
		"exec_tty":           bi("exec_tty", h.bExecTTY),
		"write_file":         bi("write_file", h.bWriteFile),
		"read_file":          bi("read_file", h.bReadFile),
		"temp_dir":           bi("temp_dir", h.bTempDir),
		"kubectl":            bi("kubectl", h.exec("kubectl")),
		"docker":             bi("docker", h.exec("docker")),
		"kind":               bi("kind", h.exec("kind")),
		"getenv":             bi("getenv", h.bGetenv),
		"now":                bi("now", h.bNow),
		"benchmark":          bi("benchmark", h.bBenchmark),
		"bench_record":       bi("bench_record", h.bBenchRecord),
		"assert_eq":          bi("assert_eq", h.bAssertEq),
		"assert_true":        bi("assert_true", h.bAssertTrue),
		"assert_contains":    bi("assert_contains", h.bAssertContains),
		"fail":               bi("fail", h.bFail),
	}
	return d
}

// --- generic builtins -------------------------------------------------------

func (h *Harness) bLog(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var msg string
	if err := starlark.UnpackArgs("log", args, kwargs, "msg", &msg); err != nil {
		return nil, err
	}
	h.logf("• %s", msg)
	return starlark.None, nil
}

// bGetenv reads an environment variable, so scenarios with EXTERNAL
// prerequisites (e.g. registry-s3 needs a live S3 server) can self-skip when
// the prerequisite is absent instead of failing — which keeps the full-glob
// containerized run (e2e/container) green without curating scenario lists.
func (h *Harness) bGetenv(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name, def string
	if err := starlark.UnpackArgs("getenv", args, kwargs, "name", &name, "default?", &def); err != nil {
		return nil, err
	}
	if v, ok := os.LookupEnv(name); ok {
		return starlark.String(v), nil
	}
	return starlark.String(def), nil
}

func (h *Harness) bSleep(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var dur string
	if err := starlark.UnpackArgs("sleep", args, kwargs, "duration", &dur); err != nil {
		return nil, err
	}
	d, err := time.ParseDuration(dur)
	if err != nil {
		return nil, err
	}
	select {
	case <-time.After(d):
	case <-h.ctx.Done():
		return nil, h.ctx.Err()
	}
	return starlark.None, nil
}

func (h *Harness) bFail(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var msg string
	if err := starlark.UnpackArgs("fail", args, kwargs, "msg", &msg); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("scenario failed: %s", msg)
}

func (h *Harness) bAssertEq(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var a, b starlark.Value
	var msg string
	if err := starlark.UnpackArgs("assert_eq", args, kwargs, "got", &a, "want", &b, "msg?", &msg); err != nil {
		return nil, err
	}
	eq, err := starlark.Equal(a, b)
	if err != nil {
		return nil, err
	}
	if !eq {
		return nil, fmt.Errorf("assert_eq failed: got %s, want %s%s", a.String(), b.String(), suffix(msg))
	}
	return starlark.None, nil
}

func (h *Harness) bAssertTrue(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	var msg string
	if err := starlark.UnpackArgs("assert_true", args, kwargs, "cond", &v, "msg?", &msg); err != nil {
		return nil, err
	}
	if !bool(v.Truth()) {
		return nil, fmt.Errorf("assert_true failed%s", suffix(msg))
	}
	return starlark.None, nil
}

func (h *Harness) bAssertContains(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s, sub string
	var msg string
	if err := starlark.UnpackArgs("assert_contains", args, kwargs, "s", &s, "sub", &sub, "msg?", &msg); err != nil {
		return nil, err
	}
	if !strings.Contains(s, sub) {
		return nil, fmt.Errorf("assert_contains failed: %q does not contain %q%s", s, sub, suffix(msg))
	}
	return starlark.None, nil
}

func suffix(msg string) string {
	if msg == "" {
		return ""
	}
	return ": " + msg
}

// --- cornus builtins ------------------------------------------------------

func (h *Harness) bServe(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	storage := h.storage
	var envv starlark.Value
	if err := starlark.UnpackArgs("serve", args, kwargs, "storage?", &storage, "env?", &envv); err != nil {
		return nil, err
	}
	extraServeEnv, err := strMap(envv)
	if err != nil {
		return nil, fmt.Errorf("serve: env: %w", err)
	}
	addr, err := freePort()
	if err != nil {
		return nil, err
	}
	// addr is 127.0.0.1:PORT. When the target can advertise a host address to
	// in-cluster pods (kube), bind all interfaces so the mount-agent sidecar can
	// reach this server via the kind gateway, and advertise that URL.
	_, port, _ := net.SplitHostPort(addr)
	bindAddr := addr
	if a, ok := h.target.(advertiser); ok && a.AdvertiseHost() != "" {
		// Bind all interfaces (both address families) so an in-cluster pod can
		// reach us over whatever the gateway offers; JoinHostPort brackets an
		// IPv6 advertise host correctly.
		bindAddr = net.JoinHostPort("", port)
		h.advertiseURL = "ws://" + net.JoinHostPort(a.AdvertiseHost(), port)
	}
	serverData, err := h.dataDir("server")
	if err != nil {
		return nil, err
	}
	// Isolate this scenario's client agent (shared by compose up -d and dockerd_up)
	// under the per-harness data root, and let the CLI subprocesses inherit it via
	// os.Environ(). Stopped on teardown.
	if h.agentDir == "" {
		h.agentDir = filepath.Join(h.dataRoot, "agent")
		_ = os.Setenv("CORNUS_AGENT_DIR", h.agentDir)
	}
	cmd := exec.CommandContext(h.ctx, h.cornusBin, "serve", "--addr", bindAddr, "--storage", storage)
	cmd.Env = append(os.Environ(), h.target.ServeEnv()...)
	cmd.Env = append(cmd.Env, "CORNUS_DATA="+serverData)
	if h.advertiseURL != "" {
		cmd.Env = append(cmd.Env, "CORNUS_ADVERTISE_URL="+h.advertiseURL)
	}
	// Scenario-supplied env wins (appended last) — e.g. serve(env={"CORNUS_API_POLICY": ...})
	// to boot an auth-enabled server for a negative-path scenario.
	for k, v := range extraServeEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout, cmd.Stderr = h.out, h.out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start cornus serve: %w", err)
	}
	h.server = cmd
	h.registryHost = addr
	h.client = client.New("http://" + addr)
	if err := h.waitHealthy("http://" + addr + "/healthz"); err != nil {
		h.stopServer()
		return nil, err
	}
	h.logf("✓ serving on %s (backend %s, storage %s)", addr, h.target.Name(), storage)
	return starlark.String(addr), nil
}

// bStopServer stops the running cornus server without tearing down the rest of
// the harness (data dirs, ssh agent). Paired with a subsequent serve() against
// the SAME --storage dir, it lets a scenario prove persistence across a restart.
func (h *Harness) bStopServer(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("stop_server", args, kwargs); err != nil {
		return nil, err
	}
	if h.server != nil && h.server.Process != nil {
		_ = h.server.Process.Kill()
		_ = h.server.Wait()
		h.server = nil
	}
	h.logf("• server stopped")
	return starlark.None, nil
}

// bCornus runs the cornus binary itself with the target's serve env (so e.g.
// `deploy -f` reaches the Docker host via DOCKER_HOST). Used to exercise the CLI
// surface the harness otherwise bypasses by calling the client library directly:
// push, deploy -f/--delete, health, version. Positional args are the CLI argv.
// Keywords: env={...} adds environment variables (appended last, so they win over
// the target's serve env) — used to point CORNUS_CONFIG at a throwaway client
// config so the connection-profile surface can be driven hermetically;
// expect_fail=True asserts the command must exit non-zero (e.g. an unauthenticated
// call to an auth-enabled server), returning its combined output for assertions
// instead of aborting the scenario.
func (h *Harness) bCornus(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var cmdArgs []string
	for _, a := range args {
		s, ok := starlark.AsString(a)
		if !ok {
			return nil, fmt.Errorf("cornus: arguments must be strings")
		}
		cmdArgs = append(cmdArgs, s)
	}
	extraEnv := append([]string{}, h.target.ServeEnv()...)
	expectFail := false
	for _, kv := range kwargs {
		name, _ := starlark.AsString(kv[0])
		switch name {
		case "env":
			envMap, err := strMap(kv[1])
			if err != nil {
				return nil, fmt.Errorf("cornus: env: %w", err)
			}
			for k, v := range envMap {
				extraEnv = append(extraEnv, k+"="+v)
			}
		case "expect_fail":
			expectFail = bool(kv[1].Truth())
		default:
			return nil, fmt.Errorf("cornus: unexpected keyword argument %q", name)
		}
	}
	out, err := h.capture(h.cornusBin, extraEnv, cmdArgs...)
	if expectFail {
		if err == nil {
			return nil, fmt.Errorf("cornus %s: expected failure but it succeeded: %s", strings.Join(cmdArgs, " "), out)
		}
		return starlark.String(out), nil
	}
	if err != nil {
		return nil, fmt.Errorf("cornus %s: %w: %s", strings.Join(cmdArgs, " "), err, out)
	}
	return starlark.String(out), nil
}

// bDockerdUp launches the `cornus daemon docker` API proxy against the running
// cornus server and returns its DOCKER_HOST (unix://...). Point the `docker`
// builtin at it with `docker("-H", host, ...)` to drive real docker commands
// through the proxy. Stopped on scenario teardown.
func (h *Harness) bDockerdUp(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("dockerd_up", args, kwargs); err != nil {
		return nil, err
	}
	if h.registryHost == "" {
		return nil, fmt.Errorf("dockerd_up: call serve() first")
	}
	dir, err := os.MkdirTemp("", "cornus-docker-proxy-")
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, "docker.sock")
	cmd := exec.CommandContext(h.ctx, h.cornusBin, "daemon", "docker", "--host", "http://"+h.registryHost, "--socket", sock)
	cmd.Env = append(os.Environ(), h.target.ServeEnv()...)
	cmd.Stdout, cmd.Stderr = h.out, h.out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start cornus daemon docker: %w", err)
	}
	h.dockerd = cmd
	h.dockerdSock = sock
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			h.logf("✓ cornus daemon docker on unix://%s -> http://%s", sock, h.registryHost)
			return starlark.String("unix://" + sock), nil
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		}
	}
	return nil, fmt.Errorf("cornus daemon docker socket %s did not appear", sock)
}

// bPortForward starts a background `cornus port-forward` from the harness host to
// a deployment's container port through the running cornus server, and returns the
// local "127.0.0.1:PORT" address it is forwarding. It exercises the full
// CLI -> server -> backend port-forward path end to end (dockerhost dials the
// container IP; kube rides the pods/portforward SPDY subresource), so a scenario
// can then http_get() the returned address and prove it reaches a container port
// that was never published to a host. The process is killed on scenario teardown.
func (h *Harness) bPortForward(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name, server string
	var port int
	var env starlark.Value
	if err := starlark.UnpackArgs("port_forward", args, kwargs, "name", &name, "port", &port, "server?", &server, "env?", &env); err != nil {
		return nil, err
	}
	// env (e.g. {"CORNUS_CONFIG": cfg}) drives the CLI through a stored connection
	// profile instead of a bare --server: when provided, omit --server so the
	// endpoint comes from the profile (this is how the in-cluster/cluster-profile
	// path — and its direct-vs-server toggle via CORNUS_VIA_SERVER — is exercised).
	envMap, err := strMap(env)
	if err != nil {
		return nil, fmt.Errorf("port_forward: env: %w", err)
	}
	var extraEnv []string
	for k, v := range envMap {
		extraEnv = append(extraEnv, k+"="+v)
	}
	if server == "" && len(extraEnv) == 0 {
		if h.registryHost == "" {
			return nil, fmt.Errorf("port_forward: call serve() first (or pass server= / env=)")
		}
		server = "http://" + h.registryHost
	}
	localAddr, err := freePort()
	if err != nil {
		return nil, err
	}
	_, localPort, _ := net.SplitHostPort(localAddr)
	mapping := localPort + ":" + strconv.Itoa(port)
	cmdArgs := []string{"port-forward"}
	if server != "" {
		cmdArgs = append(cmdArgs, "--server", server)
	}
	cmdArgs = append(cmdArgs, name, mapping)
	cmd := exec.CommandContext(h.ctx, h.cornusBin, cmdArgs...)
	cmd.Env = append(append(os.Environ(), h.target.ServeEnv()...), extraEnv...)
	cmd.Stdout, cmd.Stderr = h.out, h.out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start cornus port-forward: %w", err)
	}
	h.portForwards = append(h.portForwards, cmd)
	via := server
	if via == "" {
		via = "profile"
	}
	// Wait until the local listener accepts a connection (the CLI binds it before
	// serving), bounded so a broken forward fails fast instead of hanging.
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, derr := net.DialTimeout("tcp", localAddr, 500*time.Millisecond)
		if derr == nil {
			_ = c.Close()
			h.logf("✓ port-forward %s -> %s:%d on %s", via, name, port, localAddr)
			return starlark.String(localAddr), nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("port-forward local listener %s did not come up", localAddr)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		}
	}
}

// bTunnel starts a background `cornus tunnel <name> <port>` against the running
// cornus server, which hosts a public tunnel (ngrok) and bridges it to the
// deployment's container port. It captures the public URL the CLI prints and
// returns it, so a scenario can then http_get() that URL and prove the full
// CLI -> server -> ngrok relay -> server -> backend -> container path end to end.
// The ngrok authtoken is taken from the harness process env (NGROK_AUTHTOKEN),
// inherited by the CLI — never placed on argv. The process is killed on teardown.
func (h *Harness) bTunnel(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name, server, proto string
	var port int
	if err := starlark.UnpackArgs("tunnel", args, kwargs, "name", &name, "port", &port, "server?", &server, "proto?", &proto); err != nil {
		return nil, err
	}
	if server == "" {
		if h.registryHost == "" {
			return nil, fmt.Errorf("tunnel: call serve() first (or pass server=)")
		}
		server = "http://" + h.registryHost
	}
	cmdArgs := []string{"tunnel", "--server", server}
	if proto != "" {
		cmdArgs = append(cmdArgs, "--proto", proto)
	}
	cmdArgs = append(cmdArgs, name, strconv.Itoa(port))
	cmd := exec.CommandContext(h.ctx, h.cornusBin, cmdArgs...)
	cmd.Env = append(os.Environ(), h.target.ServeEnv()...)

	// Capture the CLI's combined output through a pipe so we can parse the printed
	// public URL while still teeing it to the harness log. Both stdout and stderr
	// are wired to the write end (an *os.File the child inherits as fds 1 and 2),
	// so exec hands the child the fd directly rather than spawning its own copy
	// goroutine — which would keep the pipe's write end open in the parent forever
	// and rob the reader below of the EOF it needs to fail fast.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("start cornus tunnel: %w", err)
	}
	// Close the parent's copy of the write end; the child holds its own dup. Now
	// the reader's Scan sees EOF exactly when the tunnel process exits and its
	// stdout/stderr close, making the empty-URL early-exit path reachable.
	pw.Close()
	h.portForwards = append(h.portForwards, cmd)

	// The CLI prints "Tunnel to <name>:<port> ready at <URL>" once the tunnel is
	// live, then blocks. Scan for that line, bounded so a broken tunnel fails fast.
	// Keep draining (and teeing) past the match for the process's lifetime so the
	// still-running tunnel never blocks on a full stdout pipe; report "" only if
	// the process exits (EOF) before ever printing a URL.
	urlCh := make(chan string, 1)
	go func() {
		defer pr.Close()
		sc := bufio.NewScanner(pr)
		sent := false
		for sc.Scan() {
			fmt.Fprintln(h.out, sc.Text())
			if !sent {
				if i := strings.Index(sc.Text(), "ready at "); i >= 0 {
					urlCh <- strings.TrimSpace(sc.Text()[i+len("ready at "):])
					sent = true
				}
			}
		}
		if !sent {
			urlCh <- ""
		}
	}()
	select {
	case u := <-urlCh:
		if u == "" {
			return nil, fmt.Errorf("cornus tunnel exited before printing a public URL")
		}
		h.logf("✓ tunnel %s:%d live at %s", name, port, u)
		return starlark.String(u), nil
	case <-time.After(60 * time.Second):
		return nil, fmt.Errorf("cornus tunnel did not report a public URL within 60s")
	case <-h.ctx.Done():
		return nil, h.ctx.Err()
	}
}

// bFreePort returns a free local TCP port as a string (listen on :0, note the
// port, release it) — for scenarios that need to pick a host port up front,
// e.g. a published-port mapping whose auto-forward the scenario then curls.
func (h *Harness) bFreePort(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("free_port", args, kwargs); err != nil {
		return nil, err
	}
	addr, err := freePort()
	if err != nil {
		return nil, err
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	return starlark.String(port), nil
}

// stopPortForwards kills every background port-forward started this scenario. The
// processes also die on context cancellation (CommandContext), but killing them
// explicitly frees the local ports promptly between scenarios.
func (h *Harness) stopPortForwards() {
	for _, cmd := range h.portForwards {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	h.portForwards = nil
}

// bWeb starts a background `cornus web` (the local web UI + its /.cornus/web/*
// backend-for-frontend) against the running cornus server and returns its
// "http://127.0.0.1:PORT" base URL, so a scenario can http_get() the BFF and
// prove it reflects real deployed workloads / compose projects / mounts. Pass
// compose_file (+ project) to give the project/graph/mounts endpoints a project,
// and frontend="127.0.0.1:PORT" (e.g. a frontend_stub) to exercise the
// detached-frontend reverse-proxy mode. The process is killed on teardown.
func (h *Harness) bWeb(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var host, composeFile, project, frontend string
	mcp := true // co-hosted MCP is on by default, matching `cornus web`
	if err := starlark.UnpackArgs("web", args, kwargs, "host?", &host, "compose_file?", &composeFile, "project?", &project, "frontend?", &frontend, "mcp?", &mcp); err != nil {
		return nil, err
	}
	if host == "" {
		if h.registryHost == "" {
			return nil, fmt.Errorf("web: call serve() first (or pass host=)")
		}
		host = "http://" + h.registryHost
	}
	addr, err := freePort()
	if err != nil {
		return nil, err
	}
	cmdArgs := []string{"web", "--addr", addr, "--host", host}
	if !mcp {
		cmdArgs = append(cmdArgs, "--no-mcp")
	}
	if composeFile != "" {
		cmdArgs = append(cmdArgs, "-f", composeFile)
	}
	if project != "" {
		cmdArgs = append(cmdArgs, "-p", project)
	}
	if frontend != "" {
		// Accept a bare "host:port" (as frontend_stub returns) or a full URL.
		feURL := frontend
		if !strings.Contains(feURL, "://") {
			feURL = "http://" + feURL
		}
		cmdArgs = append(cmdArgs, "--frontend", feURL)
	}
	cmd := exec.CommandContext(h.ctx, h.cornusBin, cmdArgs...)
	cmd.Env = append(os.Environ(), h.target.ServeEnv()...)
	cmd.Stdout, cmd.Stderr = h.out, h.out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start cornus web: %w", err)
	}
	h.webs = append(h.webs, cmd)
	// Wait until the listener accepts (cornus web binds before serving), bounded so
	// a broken start fails fast instead of hanging the scenario.
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, derr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if derr == nil {
			_ = c.Close()
			h.logf("✓ cornus web on http://%s -> %s", addr, host)
			return starlark.String("http://" + addr), nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("cornus web listener %s did not come up", addr)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		}
	}
}

// stopWebs kills every background `cornus web` started this scenario.
func (h *Harness) stopWebs() {
	for _, cmd := range h.webs {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	h.webs = nil
}

func (h *Harness) bBuild(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name_, ctxDir, dockerfile, ssh string
	var buildArgs, secret, buildContext, cacheTo, cacheFrom starlark.Value
	var remote, noCache, expectFail, lazy, lazy9p, noPush, capture, freshCache bool
	if err := starlark.UnpackArgs("build", args, kwargs,
		"name", &name_, "context", &ctxDir, "dockerfile?", &dockerfile, "args?", &buildArgs,
		"secret?", &secret, "build_context?", &buildContext, "ssh?", &ssh,
		"builder?", &remote, "no_cache?", &noCache, "expect_fail?", &expectFail,
		"cache_to?", &cacheTo, "cache_from?", &cacheFrom, "lazy?", &lazy,
		"lazy_9p?", &lazy9p, "no_push?", &noPush, "capture?", &capture, "fresh_cache?", &freshCache); err != nil {
		return nil, err
	}
	// lazy_9p backs lazy named contexts with a real kernel-9p mount of an
	// in-process p9 server (CORNUS_LAZY_9P) so the local engine reports how many
	// bytes it actually pulled ("CORNUS-9P served N bytes"). It only applies to
	// the local build path (a remote --builder build already 9p-backs its contexts
	// over the wire); it implies lazy.
	if lazy9p {
		lazy = true
	}
	if h.registryHost == "" {
		return nil, fmt.Errorf("build: call serve() first")
	}
	tag := h.registryHost + "/" + name_ + ":latest"
	cmdArgs := []string{"build", "-t", tag, "--insecure"}
	if dockerfile != "" {
		cmdArgs = append(cmdArgs, "-f", dockerfile)
	}
	if noCache {
		cmdArgs = append(cmdArgs, "--no-cache")
	}
	if lazy {
		cmdArgs = append(cmdArgs, "--lazy")
	}
	if noPush {
		cmdArgs = append(cmdArgs, "--no-push")
	}
	cacheTos, err := strOrList(cacheTo)
	if err != nil {
		return nil, err
	}
	for _, c := range cacheTos {
		cmdArgs = append(cmdArgs, "--cache-to", c)
	}
	cacheFroms, err := strOrList(cacheFrom)
	if err != nil {
		return nil, err
	}
	for _, c := range cacheFroms {
		cmdArgs = append(cmdArgs, "--cache-from", c)
	}
	bargs, err := strMap(buildArgs)
	if err != nil {
		return nil, err
	}
	for k, v := range bargs {
		cmdArgs = append(cmdArgs, "--build-arg", k+"="+v)
	}
	secrets, err := strMap(secret)
	if err != nil {
		return nil, err
	}
	for id, path := range secrets {
		cmdArgs = append(cmdArgs, "--secret", "id="+id+",src="+path)
	}
	ctxs, err := strMap(buildContext)
	if err != nil {
		return nil, err
	}
	for cname, path := range ctxs {
		cmdArgs = append(cmdArgs, "--build-context", cname+"="+path)
	}
	var extraEnv []string
	if ssh != "" {
		cmdArgs = append(cmdArgs, "--ssh", ssh)
		if h.sshAuthSock != "" {
			extraEnv = append(extraEnv, "SSH_AUTH_SOCK="+h.sshAuthSock)
		}
	}
	// builder=True runs the build remotely on the current server over 9P/WebSocket.
	if remote {
		cmdArgs = append(cmdArgs, "--builder", "ws://"+h.registryHost+"/.cornus/v1/build/attach")
	} else {
		// Local builds run their own in-process engine; give it a data dir
		// separate from the server's, or the shared boltdb lock deadlocks.
		// fresh_cache=True hands out a brand-new data dir so the local cache is
		// empty — needed to prove a --cache-from import is what produces a hit.
		role := "build"
		if freshCache {
			role = fmt.Sprintf("build-%d", h.buildSeq)
			h.buildSeq++
		}
		buildData, derr := h.dataDir(role)
		if derr != nil {
			return nil, derr
		}
		extraEnv = append(extraEnv, "CORNUS_DATA="+buildData)
		// Kernel-9p-backed lazy contexts (measurable pull) instead of the default
		// host-dir bind. Needs the 9p kernel module; the scenario is gated on Cap9P.
		if lazy9p {
			extraEnv = append(extraEnv, "CORNUS_LAZY_9P=1")
		}
	}
	cmdArgs = append(cmdArgs, ctxDir)

	where := "local"
	if remote {
		where = "remote"
	}
	// expect_fail=True asserts the build must fail (e.g. a COPY of a
	// .dockerignore'd file), proving the backend rejects it rather than silently
	// succeeding.
	var log string
	if capture {
		log, err = h.streamCapture(extraEnv, cmdArgs...)
	} else {
		err = h.stream(h.cornusBin, extraEnv, cmdArgs...)
	}
	if expectFail {
		if err == nil {
			return nil, fmt.Errorf("build %s (%s): expected failure but it succeeded", name_, where)
		}
		h.logf("✓ build %s (%s) failed as expected", name_, where)
		// capture=True hands the failed build's log back so a scenario can assert on
		// the failure reason (e.g. the `BUILD FAILED:` stream trailer).
		if capture {
			return anyDict(map[string]any{"tag": tag, "log": log}), nil
		}
		return starlark.None, nil
	}
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", name_, err)
	}
	// no_push builds stay local to the engine; nothing to prepare for the target.
	if !noPush {
		if err := h.target.PrepareImage(h.ctx, tag); err != nil {
			return nil, err
		}
	}
	h.logf("✓ built %s (%s)", tag, where)
	// capture=True returns the build's combined output alongside the tag so a
	// scenario can assert on progress markers (e.g. CACHED / served-bytes).
	if capture {
		return anyDict(map[string]any{"tag": tag, "log": log}), nil
	}
	return starlark.String(tag), nil
}

func (h *Harness) bDeploy(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name_, image, restart, hubIdentity, docker, telemetry string
	var ports, env, command, entrypoint, mounts, volumes, dns, hubExport, hubImport, ingress, knative starlark.Value
	var privileged, expectFail, agentForward bool
	replicas := 1
	if err := starlark.UnpackArgs("deploy", args, kwargs,
		"name", &name_, "image", &image, "ports?", &ports, "env?", &env, "replicas?", &replicas,
		"restart?", &restart, "command?", &command, "entrypoint?", &entrypoint, "mounts?", &mounts,
		"privileged?", &privileged, "volumes?", &volumes, "dns?", &dns, "hub_identity?", &hubIdentity,
		"hub_export?", &hubExport, "hub_import?", &hubImport, "docker?", &docker, "ingress?", &ingress,
		"knative?", &knative, "expect_fail?", &expectFail, "agent_forward?", &agentForward,
		"telemetry?", &telemetry); err != nil {
		return nil, err
	}
	if h.registryHost == "" {
		return nil, fmt.Errorf("deploy: call serve() first")
	}
	spec := api.DeploySpec{Name: name_, Image: image, Replicas: replicas, Restart: restart}
	spec.Privileged = privileged
	// agent_forward=True requests the kubernetes-only AgentRelayRole opt-in (see
	// api.DeploySpec.AgentForward), so `cornus exec --forward-agent` works
	// against this deployment (dockerhost/containerdhost gate this instead on
	// CORNUS_DOCKER_REMOTE/CORNUS_CONTAINERD_REMOTE — an env var, not a spec
	// field — so those targets never need this kwarg).
	spec.AgentForward = agentForward
	// hub_export=["name=port[:deliver]"], hub_import=["name=port[,port...]"] join the
	// workload to the overlay: it hosts the exported services and reaches the imported
	// ones (a synthetic-IP DNS record + Reach listener per import).
	hubSpec, err := parseHubSpec(hubIdentity, hubExport, hubImport)
	if err != nil {
		return nil, err
	}
	spec.Hub = hubSpec
	// dns={name: ip, ...} requests the caretaker DNS role: the pod resolves those
	// names locally (to the given user-network IPs) and forwards the rest to the
	// cluster DNS.
	if dns != nil && dns != starlark.None {
		recs, err := strMap(dns)
		if err != nil {
			return nil, err
		}
		if len(recs) > 0 {
			spec.DNS = &api.DNSSpec{Records: recs}
		}
	}
	// docker="tcp"|"unix"|"both" requests the caretaker Docker-API role: the sidecar
	// runs a Docker Engine API proxy on a pod-loopback endpoint and injects
	// DOCKER_HOST into the app container (see api.DockerSpec).
	if docker != "" {
		spec.Docker = &api.DockerSpec{Transport: docker}
	}
	// telemetry="host:port" (grpc) or a URL requests the caretaker otel role: an
	// embedded OpenTelemetry Collector receives the app's OTLP on pod loopback and
	// exports to the given endpoint; OTEL_* env is auto-injected into the app (see
	// api.TelemetrySpec). Requires the sidecar image to embed the collector
	// (-tags otelcol).
	if telemetry != "" {
		spec.Telemetry = &api.TelemetrySpec{Endpoint: telemetry}
	}
	// ingress=<host> or ingress={host,path,path_type,port,class_name,tls_secret,
	// tls_issuer,enabled} requests a kubernetes Ingress fronting the service (kube
	// only; other backends warn-and-ignore). A bare string is the host; the empty
	// dict {} enables ingress with an auto-derived host (CORNUS_INGRESS_DOMAIN).
	if ingress != nil && ingress != starlark.None {
		is, err := parseIngressSpec(ingress)
		if err != nil {
			return nil, err
		}
		spec.Ingress = is
	}
	// knative=True / {} or knative={min_scale,max_scale,target,concurrency,class,
	// metric,timeout_seconds,port} deploys as a Knative Serving Service on a
	// Knative-enabled kube cluster (other backends warn-and-ignore). Dict values
	// are strings, matching the ingress convention.
	if knative != nil && knative != starlark.None {
		kn, err := parseKnativeSpec(knative)
		if err != nil {
			return nil, err
		}
		spec.Knative = kn
	}
	envMap, err := strMap(env)
	if err != nil {
		return nil, err
	}
	spec.Env = envMap
	portList, err := strSlice(ports)
	if err != nil {
		return nil, err
	}
	for _, p := range portList {
		pm, err := parsePort(p)
		if err != nil {
			return nil, err
		}
		spec.Ports = append(spec.Ports, pm)
	}
	cmdList, err := strSlice(command)
	if err != nil {
		return nil, err
	}
	spec.Command = cmdList
	// entrypoint=[...] overrides the image ENTRYPOINT (api.DeploySpec.Entrypoint,
	// the kube container.command); command then supplies its arguments. Needed to
	// keep an ENTRYPOINT-bearing image (e.g. cornus:e2e's `cornus`) alive with a
	// plain `sleep` instead of running it as an argument to that entrypoint.
	entrypointList, err := strSlice(entrypoint)
	if err != nil {
		return nil, err
	}
	spec.Entrypoint = entrypointList
	// Host bind mounts (api.Mount): the backend binds host paths into the
	// container. Distinct from deploy_attach's client-local 9P mounts.
	mountList, err := strSlice(mounts)
	if err != nil {
		return nil, err
	}
	for _, m := range mountList {
		mt, err := parseMount(m)
		if err != nil {
			return nil, err
		}
		spec.Mounts = append(spec.Mounts, mt)
	}
	// Managed volumes (api.VolumeSpec): "[name@]target[:size[:storageclass]]".
	// Without a "name@" prefix the volume is anonymous (per-deployment, ephemeral);
	// with it the volume is named (shared across deployments, persistent). On the
	// kube backend each becomes a PVC — per-deployment+owned for anonymous, a
	// stable shared+un-owned claim for named.
	volList, err := strSlice(volumes)
	if err != nil {
		return nil, err
	}
	for _, v := range volList {
		vs := api.VolumeSpec{}
		if name, rest, ok := strings.Cut(v, "@"); ok {
			vs.Name = name
			v = rest
		}
		parts := strings.SplitN(v, ":", 3)
		vs.Target = parts[0]
		if len(parts) > 1 {
			vs.Size = parts[1]
		}
		if len(parts) > 2 {
			vs.StorageClass = parts[2]
		}
		spec.Volumes = append(spec.Volumes, vs)
	}
	// expect_fail=True asserts the deploy must fail (e.g. the kube backend rejects
	// a plain bind mount), proving the backend rejects it rather than silently
	// succeeding — without aborting the scenario.
	st, err := h.client.Deploy(h.ctx, spec)
	if expectFail {
		if err == nil {
			return nil, fmt.Errorf("deploy %s: expected failure but it succeeded", name_)
		}
		h.logf("✓ deploy %s failed as expected", name_)
		// Return the rejection message so a scenario can assert_contains on it
		// (e.g. "port is already allocated", "disabled by policy").
		return starlark.String(err.Error()), nil
	}
	if err != nil {
		return nil, fmt.Errorf("deploy %s: %w", name_, err)
	}
	return statusDict(st), nil
}

// bDeployAttach runs the long-lived `cornus deploy --server ... --local-mount`
// path in the background: the caller stays connected for the workload's lifetime
// while its local mount dirs are served over 9P. It blocks until the deployment
// reports the desired running count, then returns its status; the process is
// stopped by attach_stop or on scenario teardown.
func (h *Harness) bDeployAttach(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name_, image, restart, timeout, credentialsJSON, egressMode string
	var ports, env, localMounts, command, entrypoint starlark.Value
	var privileged bool
	replicas := 1
	timeout = "180s"
	if err := starlark.UnpackArgs("deploy_attach", args, kwargs,
		"name", &name_, "image", &image, "local_mount?", &localMounts, "command?", &command,
		"entrypoint?", &entrypoint, "ports?", &ports, "env?", &env, "replicas?", &replicas,
		"restart?", &restart, "timeout?", &timeout, "privileged?", &privileged,
		"credentials_json?", &credentialsJSON, "egress?", &egressMode); err != nil {
		return nil, err
	}
	if h.registryHost == "" {
		return nil, fmt.Errorf("deploy_attach: call serve() first")
	}
	spec := api.DeploySpec{Name: name_, Image: image, Replicas: replicas, Restart: restart}
	spec.Privileged = privileged
	// credentials_json is a raw JSON array of api.CredentialSource, letting a
	// scenario broker client-sourced credentials without a nested Starlark encoding.
	if credentialsJSON != "" {
		var sources []api.CredentialSource
		if err := json.Unmarshal([]byte(credentialsJSON), &sources); err != nil {
			return nil, fmt.Errorf("deploy_attach: parse credentials_json: %w", err)
		}
		spec.Credentials = &api.CredentialSpec{Sources: sources}
	}
	// egress="proxy"|"transparent" routes the app's egress through a client-side
	// caretaker companion (realized by the host backend's EgressBackend). The
	// deploy-attach session carries the relay the companion dials.
	if egressMode != "" {
		spec.Egress = &api.EgressSpec{Mode: egressMode}
	}
	envMap, err := strMap(env)
	if err != nil {
		return nil, err
	}
	spec.Env = envMap
	portList, err := strSlice(ports)
	if err != nil {
		return nil, err
	}
	for _, p := range portList {
		pm, err := parsePort(p)
		if err != nil {
			return nil, err
		}
		spec.Ports = append(spec.Ports, pm)
	}
	cmdList, err := strSlice(command)
	if err != nil {
		return nil, err
	}
	spec.Command = cmdList
	// entrypoint=[...] overrides the image ENTRYPOINT (see bDeploy); command
	// supplies its arguments.
	entrypointList, err := strSlice(entrypoint)
	if err != nil {
		return nil, err
	}
	spec.Entrypoint = entrypointList

	dir, err := h.dataDir("attach")
	if err != nil {
		return nil, err
	}
	specPath := filepath.Join(dir, name_+".json")
	blob, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(specPath, blob, 0o644); err != nil {
		return nil, err
	}

	cmdArgs := []string{"deploy", "--server", "ws://" + h.registryHost, "-f", specPath}
	mounts, err := strSlice(localMounts)
	if err != nil {
		return nil, err
	}
	for _, m := range mounts {
		cmdArgs = append(cmdArgs, "--local-mount", m)
	}
	c := exec.CommandContext(h.ctx, h.cornusBin, cmdArgs...)
	c.Env = os.Environ()
	c.Stdout, c.Stderr = h.out, h.out
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("start deploy_attach %s: %w", name_, err)
	}
	at := &attach{cmd: c, done: make(chan struct{})}
	go func() { _ = c.Wait(); close(at.done) }()
	if h.attaches == nil {
		h.attaches = map[string]*attach{}
	}
	h.attaches[name_] = at

	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(dur)
	for {
		st, err := h.client.Status(h.ctx, name_)
		if err == nil && countRunning(st) >= replicas {
			h.logf("✓ deploy_attach %s: %d running (local mounts served over 9P)", name_, countRunning(st))
			return statusDict(st), nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("deploy_attach %s: timed out after %s waiting for %d running", name_, timeout, replicas)
		}
		select {
		case <-time.After(time.Second):
		case <-at.done:
			return nil, fmt.Errorf("deploy_attach %s: `cornus deploy` exited before the workload became ready", name_)
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		}
	}
}

// bAttachStop gracefully stops a deploy_attach process (SIGINT -> the caller
// requests teardown), so the server removes the workload and unwinds mounts.
func (h *Harness) bAttachStop(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name_ string
	if err := starlark.UnpackArgs("attach_stop", args, kwargs, "name", &name_); err != nil {
		return nil, err
	}
	at := h.attaches[name_]
	if at == nil {
		return nil, fmt.Errorf("attach_stop: no deploy_attach named %q", name_)
	}
	if at.cmd.Process != nil {
		_ = at.cmd.Process.Signal(syscall.SIGINT)
	}
	select {
	case <-at.done:
	case <-time.After(30 * time.Second):
		if at.cmd.Process != nil {
			_ = at.cmd.Process.Kill()
		}
		<-at.done
	}
	delete(h.attaches, name_)
	return starlark.None, nil
}

// bPodExec execs a shell command in a deployment's app container (kube target
// only) — used to read a mounted file back and confirm the 9P sidecar mount is
// live inside the pod.
func (h *Harness) bPodExec(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var app, cmdStr string
	if err := starlark.UnpackArgs("pod_exec", args, kwargs, "app", &app, "cmd", &cmdStr); err != nil {
		return nil, err
	}
	kt, ok := h.target.(*KubeTarget)
	if !ok {
		return nil, fmt.Errorf("pod_exec: only supported on the kube target")
	}
	ns := kt.NS()
	// Resolving + exec can race with pod recreation: `wait` reports Running, but a
	// pod can then be rescheduled (new name) or briefly have no ready `app`
	// container. `items[0]` also risks picking a Terminating pod. Re-resolve the
	// newest Running pod and retry a few times on transient kubectl failures so a
	// mid-flight churn surfaces as a retryable state, not a hard scenario abort.
	var out string
	deadline := time.Now().Add(30 * time.Second)
	for {
		var pod string
		pod, err := h.capture("kubectl", h.toolEnv(), "-n", ns, "get", "pods",
			"-l", "cornus.app="+app, "--field-selector=status.phase=Running",
			"--sort-by=.metadata.creationTimestamp",
			"-o", "jsonpath={.items[-1:].metadata.name}")
		if err == nil && pod != "" {
			out, err = h.capture("kubectl", h.toolEnv(), "-n", ns, "exec", pod, "-c", "app", "--", "sh", "-c", cmdStr)
			if err == nil {
				return starlark.String(out), nil
			}
		}
		// Non-transient exec failures (e.g. the command inside the container exited
		// non-zero) must surface immediately; only retry pod-churn races.
		if pod != "" && !isTransientExecErr(out) {
			return nil, fmt.Errorf("pod_exec %s: %w: %s", app, err, out)
		}
		if time.Now().After(deadline) {
			if pod == "" {
				return nil, fmt.Errorf("pod_exec: locate running pod for %q: %v", app, err)
			}
			return nil, fmt.Errorf("pod_exec %s: %w: %s", app, err, out)
		}
		select {
		case <-time.After(time.Second):
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		}
	}
}

// isTransientExecErr reports whether a `kubectl exec` failure reflects a pod that
// is being (re)created rather than the container command genuinely failing, so
// pod_exec can re-resolve and retry instead of aborting the scenario.
func isTransientExecErr(out string) bool {
	for _, s := range []string{
		"not found",                    // pod deleted between resolve and exec
		"unable to upgrade connection", // container/pod gone mid-exec
		"container not found",          // app container not ready yet
		"ContainerCreating",
		"error dialing backend",
		"container is not created or running",
	} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

func (h *Harness) bStatus(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name_ string
	if err := starlark.UnpackArgs("status", args, kwargs, "name", &name_); err != nil {
		return nil, err
	}
	if h.registryHost == "" {
		return nil, fmt.Errorf("status: call serve() first")
	}
	st, err := h.client.Status(h.ctx, name_)
	if err != nil {
		return nil, err
	}
	return statusDict(st), nil
}

// bStats fetches a single Docker-format stats frame for a deployment's first
// instance (the `--no-stream` shape) and returns the CLI-visible counters as a
// dict, so a scenario can assert the backend actually produced metrics
// (memory usage, cumulative CPU, pid count). Exercises the Backend.Stats path
// end to end — the only place the bare backend's direct cgroup read is covered
// against a live container.
func (h *Harness) bStats(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name_ string
	if err := starlark.UnpackArgs("stats", args, kwargs, "name", &name_); err != nil {
		return nil, err
	}
	if h.registryHost == "" {
		return nil, fmt.Errorf("stats: call serve() first")
	}
	var buf bytes.Buffer
	if err := h.client.Stats(h.ctx, name_, api.StatsOptions{Stream: false}, &buf); err != nil {
		return nil, fmt.Errorf("stats %q: %w", name_, err)
	}
	var frame struct {
		Memory struct {
			Usage uint64 `json:"usage"`
			Limit uint64 `json:"limit"`
		} `json:"memory_stats"`
		CPU struct {
			Usage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
		} `json:"cpu_stats"`
		Pids struct {
			Current uint64 `json:"current"`
		} `json:"pids_stats"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &frame); err != nil {
		return nil, fmt.Errorf("stats %q: decode frame %q: %w", name_, buf.String(), err)
	}
	return anyDict(map[string]any{
		"mem_usage": int64(frame.Memory.Usage),
		"mem_limit": int64(frame.Memory.Limit),
		"cpu_total": int64(frame.CPU.Usage.Total),
		"pids":      int64(frame.Pids.Current),
	}), nil
}

// kubeWaitDiag returns best-effort pod diagnostics for a deployment that failed
// to reach Running, so a `wait` timeout on the kube target is debuggable from the
// CI log instead of opaque (e.g. why an app gated behind a caretaker sidecar never
// became Ready). It returns "" on non-kube targets, and is entirely best-effort:
// any kubectl error just omits that section. `describe pod` carries the container
// states and the scheduler/kubelet Events (image pull, CrashLoopBackOff, probe
// failures); the sidecar logs (current + previous, native sidecar = an init
// container) show why it crashed.
func (h *Harness) kubeWaitDiag(app string) string {
	kt, ok := h.target.(*KubeTarget)
	if !ok {
		return ""
	}
	ns := kt.NS()
	sel := "cornus.app=" + app
	var b strings.Builder
	add := func(title string, args ...string) {
		out, err := h.capture("kubectl", h.toolEnv(), args...)
		if err != nil && out == "" {
			return
		}
		b.WriteString("\n--- " + title + " ---\n")
		b.WriteString(out)
		b.WriteString("\n")
	}
	add("kubectl describe pod -l "+sel, "-n", ns, "describe", "pod", "-l", sel)
	add("caretaker sidecar logs", "-n", ns, "logs", "-l", sel, "-c", "cornus-caretaker", "--tail=60", "--prefix")
	add("caretaker sidecar logs (previous crash)", "-n", ns, "logs", "-l", sel, "-c", "cornus-caretaker", "--previous", "--tail=60", "--prefix")
	add("app container logs", "-n", ns, "logs", "-l", sel, "--all-containers", "--tail=40", "--prefix")
	return b.String()
}

func (h *Harness) bWait(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name_ string
	running := 1
	timeout := "60s"
	if err := starlark.UnpackArgs("wait", args, kwargs, "name", &name_, "running?", &running, "timeout?", &timeout); err != nil {
		return nil, err
	}
	if h.registryHost == "" {
		return nil, fmt.Errorf("wait: call serve() first")
	}
	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(dur)
	for {
		st, err := h.client.Status(h.ctx, name_)
		if err == nil && countRunning(st) >= running {
			return statusDict(st), nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("wait %s: timed out after %s waiting for %d running%s", name_, timeout, running, h.kubeWaitDiag(name_))
		}
		select {
		case <-time.After(time.Second):
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		}
	}
}

func (h *Harness) action(verb string) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var name_ string
		if err := starlark.UnpackArgs(verb, args, kwargs, "name", &name_); err != nil {
			return nil, err
		}
		if h.registryHost == "" {
			return nil, fmt.Errorf("%s: call serve() first", verb)
		}
		if err := h.client.Action(h.ctx, name_, verb); err != nil {
			return nil, fmt.Errorf("%s %s: %w", verb, name_, err)
		}
		return starlark.None, nil
	}
}

func (h *Harness) bRemove(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name_ string
	if err := starlark.UnpackArgs("remove", args, kwargs, "name", &name_); err != nil {
		return nil, err
	}
	if h.registryHost == "" {
		return nil, fmt.Errorf("remove: call serve() first")
	}
	if err := h.client.Delete(h.ctx, name_); err != nil {
		return nil, fmt.Errorf("remove %s: %w", name_, err)
	}
	return starlark.None, nil
}

// composeCallTimeout bounds a single compose builtin invocation. Every compose
// subcommand the harness drives is meant to be deploy-and-return; the cap exists
// so a misused foreground `up` (which holds until Ctrl-C) fails fast instead of
// hanging the suite until the CI job cap. Generous enough for a real image build
// on `up --build`. A var so tests can shrink it.
var composeCallTimeout = 5 * time.Minute

func (h *Harness) compose(sub string) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var file, project, conduit string
		var detach, watch bool
		if err := starlark.UnpackArgs("compose_"+sub, args, kwargs, "file", &file, "project?", &project, "detach?", &detach, "conduit?", &conduit, "watch?", &watch); err != nil {
			return nil, err
		}
		cargs := []string{"-f", file}
		if project != "" {
			cargs = append(cargs, "-p", project)
		}
		cargs = append(cargs, sub)
		if sub == "up" && detach {
			cargs = append(cargs, "-d") // detached: compose backgrounds a mounts daemon for mount services
		}
		if sub == "up" && watch {
			// --watch: the background agent watches the loaded compose/env files and
			// re-execs this CLI to reload on edit (see compose-watch-reload.star).
			cargs = append(cargs, "--watch")
		}
		if sub == "up" && conduit != "" {
			// e.g. "socks5://.shared:PORT" (shared proxy) or "socks5://127.0.0.1:PORT"
			// (session-local) — the background agent hosts the proxy for the session.
			cargs = append(cargs, "--conduit", conduit)
		}
		// kube: pre-build and kind-load any `build:` service images so the pods
		// can run them (no-op elsewhere; see prepareComposeBuildImages).
		if sub == "up" {
			if err := h.prepareComposeBuildImages(file, project); err != nil {
				return nil, err
			}
		}
		env := []string{"CORNUS_HOST=http://" + h.registryHost}
		// Forward the scenario's ssh-agent (started by ssh_agent()) so a compose
		// service with a `build.ssh` section resolves its "default" agent to the
		// live socket — the same propagation the standalone build() builtin does.
		if h.sshAuthSock != "" {
			env = append(env, "SSH_AUTH_SOCK="+h.sshAuthSock)
		}
		// Defensive cap: a compose subcommand should complete quickly here. In
		// particular a FOREGROUND `up` (no -d) holds the session until Ctrl-C
		// (auto-forwarding is on by default), so a scenario that ran one
		// synchronously would otherwise wedge the whole suite until the CI job
		// timeout — turn that into a fast, diagnosable failure instead. Bounded by
		// h.ctx so a harness-wide cancel still wins.
		ctx, cancel := context.WithTimeout(h.ctx, composeCallTimeout)
		defer cancel()
		out, err := h.captureCtx(ctx, h.cornusBin, env, append([]string{"compose"}, cargs...)...)
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded && h.ctx.Err() == nil {
				return nil, fmt.Errorf("compose %s: did not return within %s — a foreground `compose up` (no -d) holds until Ctrl-C; pass detach=True / -d for a deploy-and-return: %s", sub, composeCallTimeout, out)
			}
			return nil, fmt.Errorf("compose %s: %w: %s", sub, err, out)
		}
		return starlark.String(out), nil
	}
}

// bComposeUpBg backgrounds a FOREGROUND `cornus compose up` (no -d) so a
// scenario can drive a `compose down` against it and prove the up self-exits
// when its workloads are removed. Unlike compose_up(detach=True) (which hands
// mounts/forwards to a background helper and returns), this holds the up in the
// foreground the way an interactive terminal session does. It returns a handle
// (the project, or the file when no project is given) to pass to
// compose_up_wait; the process is stopped on teardown if still running.
func (h *Harness) bComposeUpBg(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var file, project, conduit string
	var envv starlark.Value
	if err := starlark.UnpackArgs("compose_up_bg", args, kwargs, "file", &file, "project?", &project, "conduit?", &conduit, "env?", &envv); err != nil {
		return nil, err
	}
	if h.registryHost == "" {
		return nil, fmt.Errorf("compose_up_bg: call serve() first")
	}
	// env={...} sets extra environment on the HELD (foreground) client process — it
	// serves the deploy-attach backings, so this is how a scenario points the client's
	// OWN egress dialer at a proxy (ALL_PROXY / HTTP(S)_PROXY) to prove client-side
	// egress leaves through the client's sanctioned proxy.
	extraEnv, err := strMap(envv)
	if err != nil {
		return nil, fmt.Errorf("compose_up_bg: env: %w", err)
	}
	// kube: pre-build+load any `build:` service images, as compose_up() does.
	if err := h.prepareComposeBuildImages(file, project); err != nil {
		return nil, err
	}
	cargs := []string{"compose", "-f", file}
	if project != "" {
		cargs = append(cargs, "-p", project)
	}
	cargs = append(cargs, "up") // foreground: holds mounts/forwards until the workloads go away
	if conduit != "" {
		// e.g. "socks5://127.0.0.1:PORT" -> the held session hosts a SOCKS5 proxy that
		// reaches every service by name (and short/bare alias) for the scenario to fetch.
		cargs = append(cargs, "--conduit", conduit)
	}
	c := exec.CommandContext(h.ctx, h.cornusBin, cargs...)
	c.Env = append(os.Environ(), "CORNUS_HOST=http://"+h.registryHost)
	for k, v := range extraEnv {
		c.Env = append(c.Env, k+"="+v)
	}
	buf := &bytes.Buffer{}
	w := io.MultiWriter(h.out, buf) // same value for both streams => exec serializes writes
	c.Stdout, c.Stderr = w, w
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("start compose_up_bg: %w", err)
	}
	bg := &bgCompose{cmd: c, buf: buf, done: make(chan struct{})}
	go func() {
		err := c.Wait()
		if ee, ok := err.(*exec.ExitError); ok {
			bg.code = ee.ExitCode()
		} else if err != nil {
			bg.code = -1
		}
		close(bg.done)
	}()
	key := project
	if key == "" {
		key = file
	}
	if h.composeUps == nil {
		h.composeUps = map[string]*bgCompose{}
	}
	h.composeUps[key] = bg
	return starlark.String(key), nil
}

// bComposeUpWait waits for a compose_up_bg process to exit (self-terminating
// after its workloads were removed) and returns {"output", "code"}. It errors if
// the process is still running after timeout (default "60s") — i.e. the
// foreground up did NOT notice its workloads went away.
func (h *Harness) bComposeUpWait(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var handle, timeout string
	timeout = "60s"
	if err := starlark.UnpackArgs("compose_up_wait", args, kwargs, "handle", &handle, "timeout?", &timeout); err != nil {
		return nil, err
	}
	bg := h.composeUps[handle]
	if bg == nil {
		return nil, fmt.Errorf("compose_up_wait: no backgrounded compose up %q", handle)
	}
	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, err
	}
	select {
	case <-bg.done:
	case <-time.After(dur):
		return nil, fmt.Errorf("compose_up_wait %q: foreground `compose up` still running after %s (it did not self-terminate when its workloads were removed)", handle, timeout)
	case <-h.ctx.Done():
		return nil, h.ctx.Err()
	}
	delete(h.composeUps, handle)
	return anyDict(map[string]any{"output": bg.buf.String(), "code": bg.code}), nil
}

// bComposeUpStop sends SIGINT to a backgrounded foreground `compose up` — like an
// interactive Ctrl-C — and waits for it to exit, returning {"output", "code"}.
// Where compose_up_wait waits for the up to self-terminate after an EXTERNAL
// `down` removes its workloads, this ACTIVELY terminates the still-held up so a
// scenario can assert the Ctrl-C teardown: a foreground exit removes the
// mount-free deployments the up created (like `docker compose up`), so the
// workloads must be gone afterward with no `down` involved. Errors if the up does
// not exit within timeout (default "60s").
func (h *Harness) bComposeUpStop(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var handle, timeout string
	timeout = "60s"
	if err := starlark.UnpackArgs("compose_up_stop", args, kwargs, "handle", &handle, "timeout?", &timeout); err != nil {
		return nil, err
	}
	bg := h.composeUps[handle]
	if bg == nil {
		return nil, fmt.Errorf("compose_up_stop: no backgrounded compose up %q", handle)
	}
	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, err
	}
	if bg.cmd.Process != nil {
		if err := bg.cmd.Process.Signal(syscall.SIGINT); err != nil {
			return nil, fmt.Errorf("compose_up_stop %q: signal: %w", handle, err)
		}
	}
	select {
	case <-bg.done:
	case <-time.After(dur):
		return nil, fmt.Errorf("compose_up_stop %q: foreground `compose up` still running %s after SIGINT (it did not tear down on Ctrl-C)", handle, timeout)
	case <-h.ctx.Done():
		return nil, h.ctx.Err()
	}
	delete(h.composeUps, handle)
	return anyDict(map[string]any{"output": bg.buf.String(), "code": bg.code}), nil
}

// prepareComposeBuildImages makes a compose project's `build:` service images
// runnable on the kube target before `compose up` deploys them. The compose
// client builds and pushes each such image to cornus's registry under the
// <project>-<service> tag, but an in-cluster pod cannot pull from the
// host-bound registry (127.0.0.1 in a pod is the pod itself) — like build(),
// the image must be loaded into the cluster's nodes (PrepareImage = `kind load
// image-archive`) so imagePullPolicy IfNotPresent finds it. So: enumerate the
// build services from the compose model (mirroring the compose CLI's own
// plan/tag derivation in composecli.buildService), pre-build+push them via
// `cornus compose build`, and PrepareImage each resulting tag; `up`'s own
// rebuild is then a warm-cache hit on the same tag. No-op on non-kube targets
// and on build-free compose files, and idempotent: rerunning hits the build
// cache and re-loading an already-loaded image into kind is harmless.
func (h *Harness) prepareComposeBuildImages(file, project string) error {
	if _, ok := h.target.(*KubeTarget); !ok {
		return nil // other targets pull straight from the same-host registry
	}
	refs, project, err := composeBuildImageRefs(h.registryHost, file, project)
	if err != nil {
		return fmt.Errorf("compose up: %w", err)
	}
	if len(refs) == 0 {
		return nil
	}
	env := []string{"CORNUS_HOST=http://" + h.registryHost}
	if out, err := h.capture(h.cornusBin, env, "compose", "-f", file, "-p", project, "build"); err != nil {
		return fmt.Errorf("compose build (kube image pre-load): %w: %s", err, out)
	}
	for _, ref := range refs {
		if err := h.target.PrepareImage(h.ctx, ref); err != nil {
			return fmt.Errorf("prepare compose image %s: %w", ref, err)
		}
		h.logf("✓ loaded compose-built image %s into the cluster", ref)
	}
	return nil
}

// composeBuildImageRefs enumerates the registry tags `cornus compose` will push
// for a compose file's `build:` services: <registryHost>/<project>-<service>:latest
// (composecli.buildService tags each build with its plan's Resource name). It
// also returns the resolved project name — the compose CLI's resolution: an
// explicit project wins verbatim, else the file's `name:` / sanitized
// directory-name default. The refs are sorted for deterministic loading order.
func composeBuildImageRefs(registryHost, file, project string) (refs []string, resolvedProject string, err error) {
	proj, err := compose.Load(file)
	if err != nil {
		return nil, "", fmt.Errorf("load %s: %w", file, err)
	}
	if project == "" {
		project = proj.ResolveName(filepath.Dir(file))
	}
	plans, err := proj.Plan(project)
	if err != nil {
		return nil, "", fmt.Errorf("plan %s: %w", file, err)
	}
	for _, plan := range plans {
		if plan.Build == nil {
			continue
		}
		refs = append(refs, registryHost+"/"+plan.Resource+":latest")
	}
	sort.Strings(refs)
	return refs, project, nil
}

// devcontainer drives the compose client's devcontainer path: it runs
// `cornus compose --devcontainer <dir> <sub>` so a repo with no Compose file
// (only a .devcontainer/devcontainer.json) can be brought up/torn down. `dir` is
// a directory to search for .devcontainer/devcontainer.json (or a path straight
// to a devcontainer.json). It mirrors compose() — same CORNUS_HOST env and the
// same `project`/`detach` knobs (a single-container devcontainer always bind-mounts
// the workspace, so it deploys over the deploy-attach 9P path; `up` needs -d to
// background the mount helper, exactly like a compose service with bind mounts).
func (h *Harness) devcontainer(sub string) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var dir, project string
		var detach bool
		if err := starlark.UnpackArgs("devcontainer_"+sub, args, kwargs, "dir", &dir, "project?", &project, "detach?", &detach); err != nil {
			return nil, err
		}
		cargs := []string{"--devcontainer", dir}
		if project != "" {
			cargs = append(cargs, "-p", project)
		}
		cargs = append(cargs, sub)
		if sub == "up" && detach {
			cargs = append(cargs, "-d") // detached: compose backgrounds a mounts daemon for the workspace mount
		}
		env := []string{"CORNUS_HOST=http://" + h.registryHost}
		out, err := h.capture(h.cornusBin, env, append([]string{"compose"}, cargs...)...)
		if err != nil {
			return nil, fmt.Errorf("devcontainer %s: %w: %s", sub, err, out)
		}
		return starlark.String(out), nil
	}
}

func (h *Harness) bRegistryRoundtrip(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var ref string
	if err := starlark.UnpackArgs("registry_roundtrip", args, kwargs, "ref", &ref); err != nil {
		return nil, err
	}
	full := h.registryHost + "/" + ref
	r, err := name.ParseReference(full, name.Insecure)
	if err != nil {
		return nil, err
	}
	img, err := random.Image(1024, 2)
	if err != nil {
		return nil, err
	}
	if err := remote.Write(r, img); err != nil {
		return nil, fmt.Errorf("push %s: %w", full, err)
	}
	pulled, err := remote.Image(r)
	if err != nil {
		return nil, fmt.Errorf("pull %s: %w", full, err)
	}
	want, _ := img.Digest()
	got, _ := pulled.Digest()
	if want != got {
		return nil, fmt.Errorf("digest mismatch: pushed %s pulled %s", want, got)
	}
	return starlark.String(got.String()), nil
}

// bBuildUpload drives the raw POST /.cornus/v1/build tar-upload endpoint — a thinner
// surface than the `build` builtin (which goes through the client / build-attach
// path): it tars the context dir and POSTs it with the target ref as ?t=, then
// returns the streamed text/plain progress log verbatim. No secrets / ssh /
// named-contexts / cache-import / lazy — just context-tar in, progress out.
func (h *Harness) bBuildUpload(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var target, contextDir, dockerfile string
	var noPush, noCache bool
	if err := starlark.UnpackArgs("build_upload", args, kwargs,
		"target", &target, "context", &contextDir, "dockerfile?", &dockerfile,
		"no_push?", &noPush, "no_cache?", &noCache); err != nil {
		return nil, err
	}

	// Tar the context directory (regular files + dirs; the endpoint skips links).
	var body bytes.Buffer
	tw := tar.NewWriter(&body)
	root := filepath.Clean(contextDir)
	err := filepath.Walk(root, func(p string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if fi.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("tar context %s: %w", contextDir, err)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("t", target)
	if noPush {
		q.Set("push", "false")
	}
	if noCache {
		q.Set("no-cache", "true")
	}
	if dockerfile != "" {
		q.Set("dockerfile", dockerfile)
	}
	reqURL := "http://" + h.registryHost + "/.cornus/v1/build?" + q.Encode()
	req, err := http.NewRequestWithContext(h.ctx, http.MethodPost, reqURL, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return anyDict(map[string]any{"status": resp.StatusCode, "log": string(out)}), nil
}

// bDockerCompose runs `docker compose <args...>` against the `cornus daemon docker`
// proxy started by dockerd_up(). The compose CLI plugin does not reliably honor
// the `-H` flag the `docker` builtin uses, so the proxy is selected via the
// DOCKER_HOST environment instead (appended last so it wins over any inherited
// value). Each compose service becomes a cornus deploy named
// <project>-<service>-1 on the server.
func (h *Harness) bDockerCompose(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if h.dockerdSock == "" {
		return nil, fmt.Errorf("docker_compose: call dockerd_up() first")
	}
	cmdArgs := []string{"compose"}
	for _, a := range args {
		s, ok := starlark.AsString(a)
		if !ok {
			return nil, fmt.Errorf("docker_compose: arguments must be strings")
		}
		cmdArgs = append(cmdArgs, s)
	}
	out, err := h.capture("docker", []string{"DOCKER_HOST=unix://" + h.dockerdSock}, cmdArgs...)
	if err != nil {
		return nil, fmt.Errorf("docker compose %s: %w: %s", strings.Join(cmdArgs[1:], " "), err, out)
	}
	return starlark.String(out), nil
}

// bDevcontainerCLI runs the OFFICIAL `devcontainer` CLI (@devcontainers/cli —
// the engine VS Code's Dev Containers extension shells out to) against the
// `cornus daemon docker` proxy started by dockerd_up(), selected via
// DOCKER_HOST exactly like docker_compose. This is the real VS Code
// devcontainer toolchain, distinct from the devcontainer_* builtins above,
// which drive cornus's OWN `cornus compose --devcontainer` translation.
func (h *Harness) bDevcontainerCLI(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if h.dockerdSock == "" {
		return nil, fmt.Errorf("devcontainer_cli: call dockerd_up() first")
	}
	var cmdArgs []string
	for _, a := range args {
		s, ok := starlark.AsString(a)
		if !ok {
			return nil, fmt.Errorf("devcontainer_cli: arguments must be strings")
		}
		cmdArgs = append(cmdArgs, s)
	}
	out, err := h.capture("devcontainer", []string{"DOCKER_HOST=unix://" + h.dockerdSock}, cmdArgs...)
	if err != nil {
		return nil, fmt.Errorf("devcontainer %s: %w: %s", strings.Join(cmdArgs, " "), err, out)
	}
	return starlark.String(out), nil
}

func (h *Harness) bHTTPGet(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var url, socks5Addr, caFile string
	var allowError, insecure, retry5xx bool
	retry := "15s"
	if err := starlark.UnpackArgs("http_get", args, kwargs, "url", &url, "retry?", &retry, "socks5?", &socks5Addr, "allow_error?", &allowError, "insecure?", &insecure, "ca_file?", &caFile, "retry_5xx?", &retry5xx); err != nil {
		return nil, err
	}
	dur, err := time.ParseDuration(retry)
	if err != nil {
		return nil, err
	}
	// socks5="127.0.0.1:PORT" routes the GET through a client-side SOCKS5 proxy (a
	// conduit in socks5 mode), so a scenario can prove a workload is reachable by
	// name through the split-tunnel — the host is resolved by the proxy, not DNS.
	// insecure=True skips TLS verification, for reaching an https endpoint that
	// terminates TLS with a locally-generated cert (e.g. the emulated ingress), while
	// ca_file verifies it against a specific PEM certificate or CA bundle.
	httpClient := http.DefaultClient
	if socks5Addr != "" || insecure || caFile != "" {
		tr := &http.Transport{}
		if socks5Addr != "" {
			pd, err := proxy.SOCKS5("tcp", socks5Addr, nil, proxy.Direct)
			if err != nil {
				return nil, fmt.Errorf("http_get: socks5 proxy %s: %w", socks5Addr, err)
			}
			tr.DialContext = pd.(proxy.ContextDialer).DialContext
		}
		if insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test-only opt-in
		}
		if caFile != "" {
			if insecure {
				return nil, fmt.Errorf("http_get: ca_file and insecure are mutually exclusive")
			}
			pemData, err := os.ReadFile(caFile)
			if err != nil {
				return nil, fmt.Errorf("http_get: read ca_file %s: %w", caFile, err)
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(pemData) {
				return nil, fmt.Errorf("http_get: ca_file %s contains no certificates", caFile)
			}
			tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		}
		httpClient = &http.Client{Transport: tr}
	}
	// A freshly published port frequently accepts the TCP connection (docker-proxy
	// is already listening) before the workload behind it is serving, so a GET
	// issued the instant a deploy reports "running" can be reset or refused. This
	// window is sub-second on a fast host but wider under Docker-in-Docker/CI.
	// Retry transient connection-level errors until `retry` elapses; the moment we
	// get any HTTP response we return it verbatim, so status assertions stay honest
	// (a real 500 is not retried away). retry_5xx=True additionally retries a 5xx
	// response body: on the ingress-emulation path a local reverse proxy answers
	// 502/503 while its upstream workload is still coming up, which is transient in
	// the same way a connection refusal is — but it surfaces as an HTTP response,
	// not a dial error, so the plain retry loop cannot absorb it. The retry stays
	// bounded by `retry`; once the deadline passes the last 5xx is returned as-is,
	// so a workload that never recovers still fails the assertion honestly.
	deadline := time.Now().Add(dur)
	for {
		req, _ := http.NewRequestWithContext(h.ctx, http.MethodGet, url, nil)
		resp, err := httpClient.Do(req)
		if err == nil {
			if retry5xx && resp.StatusCode >= 500 && time.Now().Before(deadline) {
				resp.Body.Close()
			} else {
				defer resp.Body.Close()
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				return anyDict(map[string]any{"status": resp.StatusCode, "body": string(body)}), nil
			}
		} else if time.Now().After(deadline) {
			// allow_error lets a scenario assert a request FAILS (e.g. a name that must
			// not tunnel inward through a proxy egresses directly and is unreachable),
			// returning {"error": ...} instead of aborting the scenario.
			if allowError {
				return anyDict(map[string]any{"error": err.Error()}), nil
			}
			return nil, err
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		}
	}
}

// bHTTP issues a single arbitrary HTTP request so a scenario can exercise
// wire-protocol edges the higher-level builtins (registry_roundtrip, http_get)
// do not reach: HEAD, chunked/resumable blob uploads (POST/PATCH/PUT), cross-repo
// mounts, and DELETE. Unlike bHTTPGet there is no retry loop — the registry is
// already healthy once serve() returns, and edge assertions must see the exact
// status the server returned. The response dict exposes headers keyed by their
// canonical Go name (resp.Header is already canonical), joining multi-value
// headers with ", ", so a scenario can read resp["headers"]["Location"] and
// resp["headers"]["Docker-Content-Digest"].
func (h *Harness) bHTTP(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var method, url, body string
	var headersVal starlark.Value
	if err := starlark.UnpackArgs("http", args, kwargs, "method", &method, "url", &url, "body?", &body, "headers?", &headersVal); err != nil {
		return nil, err
	}
	headers, err := strMap(headersVal)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(h.ctx, method, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	respHeaders := make(map[string]string, len(resp.Header))
	for k, v := range resp.Header {
		respHeaders[k] = strings.Join(v, ", ")
	}
	return anyDict(map[string]any{
		"status":  resp.StatusCode,
		"body":    string(respBody),
		"headers": respHeaders,
	}), nil
}

// bFTPRoundtrip performs a real FTP round-trip against a deployed FTP server,
// proving BOTH directions of the data channel work: it uploads content (STOR,
// client->server) then downloads it back (RETR, server->client) and reports
// whether the bytes match. A protocol failure is returned in the "error" field
// with ok=false rather than aborting the harness, so the scenario asserts on the
// result (and can retry a racy server startup). Returns {"ok": bool, "downloaded":
// str, "n": int, "error": str}.
//
// The default is passive mode (PASV -> the client dials the server's data port).
// active=True switches to active mode (PORT -> the SERVER dials back to a data
// listener the client opens); advertise_host overrides the address the client
// tells the server to connect back to (defaults to the control connection's local
// host). Active mode is what a scenario uses to exercise the server->client
// connect-back path, which — unlike passive — does NOT traverse a published data
// port.
func (h *Harness) bFTPRoundtrip(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var addr, user, password, content, advertiseHost string
	var active bool
	path := "rt.dat"
	if err := starlark.UnpackArgs("ftp_roundtrip", args, kwargs,
		"addr", &addr, "user", &user, "password", &password, "content", &content, "path?", &path,
		"active?", &active, "advertise_host?", &advertiseHost); err != nil {
		return nil, err
	}
	downloaded, err := h.ftpRoundtrip(addr, user, password, path, []byte(content), active, advertiseHost)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	return anyDict(map[string]any{
		"ok":         err == nil,
		"downloaded": string(downloaded),
		"n":          len(downloaded),
		"error":      errStr,
	}), nil
}

// ftpRoundtrip speaks a minimal FTP control protocol (net/bufio only, no
// third-party dependency) to STOR then RETR a file, returning the downloaded
// bytes. It always returns the bytes it managed to read; err is non-nil on any
// protocol failure OR when the downloaded bytes differ from what was uploaded.
//
// In PASSIVE mode (active=false) the data connection is dialed to the CONTROL
// connection's host with the port parsed from the 227 PASV reply — deliberately
// IGNORING the h1-h4 address the server advertises in that reply. A
// masqueraded/private passive address (common behind port publishing / NAT) would
// otherwise be unreachable from the test host.
//
// In ACTIVE mode (active=true) the roles reverse: for each transfer the CLIENT
// opens a data listener and sends PORT h1,h2,h3,h4,p1,p2, and the SERVER dials
// back to it. advertiseHost overrides the address sent in PORT (the address the
// server connects back to); empty means the control connection's own local host.
// The STOR/RETR transfer logic is shared between the two modes via openData.
func (h *Harness) ftpRoundtrip(addr, user, password, path string, content []byte, active bool, advertiseHost string) ([]byte, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("bad addr %q: %w", addr, err)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial control %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(conn)

	// readReply reads one FTP reply, transparently consuming a multiline reply
	// ("NNN-...\r\n ... \r\nNNN <text>"), and returns its numeric code + text.
	readReply := func() (int, string, error) {
		line, err := br.ReadString('\n')
		if err != nil {
			return 0, "", fmt.Errorf("read reply: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 4 {
			return 0, "", fmt.Errorf("short reply %q", line)
		}
		code, err := strconv.Atoi(line[:3])
		if err != nil {
			return 0, "", fmt.Errorf("bad reply code %q", line)
		}
		if line[3] == '-' { // multiline: read until "NNN " terminator
			term := line[:3] + " "
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return 0, "", fmt.Errorf("read multiline reply: %w", err)
				}
				if strings.HasPrefix(strings.TrimRight(l, "\r\n"), term) {
					break
				}
			}
		}
		return code, line[4:], nil
	}
	send := func(line string) error {
		if _, err := fmt.Fprintf(conn, "%s\r\n", line); err != nil {
			return fmt.Errorf("write %q: %w", line, err)
		}
		return nil
	}
	expect := func(want int) (string, error) {
		code, msg, err := readReply()
		if err != nil {
			return "", err
		}
		if code != want {
			return "", fmt.Errorf("expected %d, got %d: %s", want, code, msg)
		}
		return msg, nil
	}
	// cmd sends a command and asserts its reply code.
	cmd := func(line string, want int) error {
		if err := send(line); err != nil {
			return err
		}
		_, err := expect(want)
		return err
	}
	// pasv issues PASV, parses the "(h1,h2,h3,h4,p1,p2)" reply, and dials a fresh
	// data connection to host:p1*256+p2 (host from the control addr, not the reply).
	pasv := func() (net.Conn, error) {
		if err := send("PASV"); err != nil {
			return nil, err
		}
		msg, err := expect(227)
		if err != nil {
			return nil, err
		}
		open := strings.IndexByte(msg, '(')
		close := strings.LastIndexByte(msg, ')')
		if open < 0 || close < 0 || close < open {
			return nil, fmt.Errorf("cannot find (h1..p2) in PASV reply %q", msg)
		}
		nums := strings.Split(msg[open+1:close], ",")
		if len(nums) != 6 {
			return nil, fmt.Errorf("PASV reply wants 6 fields, got %q", msg)
		}
		p1, err1 := strconv.Atoi(strings.TrimSpace(nums[4]))
		p2, err2 := strconv.Atoi(strings.TrimSpace(nums[5]))
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad PASV port in %q", msg)
		}
		dataAddr := net.JoinHostPort(host, strconv.Itoa(p1*256+p2))
		dc, err := dialer.DialContext(ctx, "tcp", dataAddr)
		if err != nil {
			return nil, fmt.Errorf("dial data %s: %w", dataAddr, err)
		}
		_ = dc.SetDeadline(time.Now().Add(30 * time.Second))
		return dc, nil
	}
	// openData sets up the data channel for one transfer per the mode, issues the
	// STOR/RETR command, waits for the 150 go-ahead, and returns a live data
	// connection ready for the caller to write (STOR) or read (RETR). This shared
	// helper is where passive (PASV -> dial) and active (PORT -> listen+accept)
	// diverge; the STOR/RETR body around it is identical for both modes.
	openData := func(verb, path string) (net.Conn, error) {
		if !active {
			// PASSIVE: dial the server's advertised data port, THEN send the command.
			dc, err := pasv()
			if err != nil {
				return nil, err
			}
			if err := send(verb + " " + path); err != nil {
				dc.Close()
				return nil, err
			}
			if _, err := expect(150); err != nil {
				dc.Close()
				return nil, fmt.Errorf("%s: %w", verb, err)
			}
			return dc, nil
		}
		// ACTIVE: open our own data listener, tell the server where to connect back
		// via PORT, send the command, then Accept the connection the server dials.
		ln, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			return nil, fmt.Errorf("active data listen: %w", err)
		}
		if tl, ok := ln.(*net.TCPListener); ok {
			_ = tl.SetDeadline(time.Now().Add(30 * time.Second))
		}
		_, portStr, _ := net.SplitHostPort(ln.Addr().String())
		dport, err := strconv.Atoi(portStr)
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("active listener port %q: %w", portStr, err)
		}
		// Advertise host: the caller-supplied override, else the control
		// connection's own local host (what the server sees us connect FROM).
		ah := advertiseHost
		if ah == "" {
			ah, _, _ = net.SplitHostPort(conn.LocalAddr().String())
		}
		quads := strings.Split(ah, ".")
		if len(quads) != 4 {
			ln.Close()
			return nil, fmt.Errorf("active advertise host %q is not an IPv4 dotted-quad", ah)
		}
		portCmd := fmt.Sprintf("PORT %s,%s,%s,%s,%d,%d", quads[0], quads[1], quads[2], quads[3], dport/256, dport%256)
		if err := send(portCmd); err != nil {
			ln.Close()
			return nil, err
		}
		if _, err := expect(200); err != nil {
			ln.Close()
			return nil, fmt.Errorf("PORT: %w", err)
		}
		if err := send(verb + " " + path); err != nil {
			ln.Close()
			return nil, err
		}
		if _, err := expect(150); err != nil {
			ln.Close()
			return nil, fmt.Errorf("%s: %w", verb, err)
		}
		dc, err := ln.Accept()
		ln.Close() // one connection per transfer; stop listening once accepted
		if err != nil {
			return nil, fmt.Errorf("active accept for %s: %w", verb, err)
		}
		_ = dc.SetDeadline(time.Now().Add(30 * time.Second))
		return dc, nil
	}

	if _, err := expect(220); err != nil {
		return nil, fmt.Errorf("greeting: %w", err)
	}
	if err := cmd("USER "+user, 331); err != nil {
		return nil, fmt.Errorf("USER: %w", err)
	}
	if err := cmd("PASS "+password, 230); err != nil {
		return nil, fmt.Errorf("PASS: %w", err)
	}
	if err := cmd("TYPE I", 200); err != nil {
		return nil, fmt.Errorf("TYPE: %w", err)
	}

	// STOR: upload content over a fresh data connection (client -> server).
	dc, err := openData("STOR", path)
	if err != nil {
		return nil, fmt.Errorf("STOR: %w", err)
	}
	if _, err := dc.Write(content); err != nil {
		dc.Close()
		return nil, fmt.Errorf("STOR write: %w", err)
	}
	dc.Close() // signals EOF to the server so it finalizes the upload
	if _, err := expect(226); err != nil {
		return nil, fmt.Errorf("STOR complete: %w", err)
	}

	// RETR: download it back over another fresh data connection (server -> client).
	dc, err = openData("RETR", path)
	if err != nil {
		return nil, fmt.Errorf("RETR: %w", err)
	}
	downloaded, rerr := io.ReadAll(dc)
	dc.Close()
	if rerr != nil {
		return downloaded, fmt.Errorf("RETR read: %w", rerr)
	}
	if _, err := expect(226); err != nil {
		return downloaded, fmt.Errorf("RETR complete: %w", err)
	}

	_ = send("QUIT") // best-effort; ignore the 221

	if !bytes.Equal(downloaded, content) {
		return downloaded, fmt.Errorf("downloaded %d bytes != uploaded %d bytes", len(downloaded), len(content))
	}
	return downloaded, nil
}

func (h *Harness) bSh(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var script string
	if err := starlark.UnpackArgs("sh", args, kwargs, "cmd", &script); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(h.ctx, "sh", "-c", script)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return nil, err
	}
	return anyDict(map[string]any{"code": code, "output": strings.TrimSpace(string(out))}), nil
}

// bExecTTY runs a command under a real PTY so a scenario can drive interactive
// `-it` sessions the plain shell-out builtins cannot: native `cornus exec -i -t`
// and `docker exec -i -t` through the `cornus daemon docker` proxy, including terminal
// resize (the PTY window size propagates through to the remote TTY). It writes
// `input` to the PTY master, reads ALL output back until the process exits / the
// PTY hits EOF (bounded by `timeout`), and returns {"output": <str>, "code": <int>}.
//
// argv[0] is resolved against the harness binaries so scenarios stay path-agnostic:
// "cornus" -> h.cornusBin, "docker" -> "docker" (PATH lookup); anything else is
// used verbatim.
func (h *Harness) bExecTTY(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var argv starlark.Value
	var input, timeout string
	var envVal starlark.Value
	rows, cols := 24, 80
	timeout = "30s"
	if err := starlark.UnpackArgs("exec_tty", args, kwargs,
		"argv", &argv, "input?", &input, "rows?", &rows, "cols?", &cols,
		"timeout?", &timeout, "env?", &envVal); err != nil {
		return nil, err
	}
	cmdArgs, err := strSlice(argv)
	if err != nil {
		return nil, fmt.Errorf("exec_tty: %w", err)
	}
	if len(cmdArgs) == 0 {
		return nil, fmt.Errorf("exec_tty: argv must be a non-empty list")
	}
	// Resolve argv[0] against the harness binaries (mirrors h.exec / h.cornusBin).
	bin := cmdArgs[0]
	switch bin {
	case "cornus":
		bin = h.cornusBin
	case "docker":
		bin = "docker"
	}
	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, fmt.Errorf("exec_tty: %w", err)
	}

	cmd := exec.Command(bin, cmdArgs[1:]...)
	cmd.Env = os.Environ()
	// Forward the scenario's ssh-agent (started by ssh_agent()), so a scenario
	// exercising `cornus exec --forward-agent`/`cornus compose exec
	// --forward-agent` finds the live socket — the same propagation build()
	// and compose() already do for their own ssh-forwarding purposes.
	if h.sshAuthSock != "" {
		cmd.Env = append(cmd.Env, "SSH_AUTH_SOCK="+h.sshAuthSock)
	}
	if envVal != nil && envVal != starlark.None {
		envMap, err := strMap(envVal)
		if err != nil {
			return nil, fmt.Errorf("exec_tty: %w", err)
		}
		for k, v := range envMap {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, fmt.Errorf("exec_tty: start %s under pty: %w", bin, err)
	}
	defer ptmx.Close()

	// Read the PTY master concurrently, ANSWERING terminal queries like a real
	// terminal, and (optionally) writing input once the child is ready.
	//
	// An interactive remote shell (busybox ash) whose TERM implies a capable
	// terminal — CI runners set TERM=xterm — probes the terminal on startup with a
	// cursor-position report request (DSR, "ESC[6n") and BLOCKS reading the reply.
	// The harness PTY is not a real terminal, so without an answer the shell hangs
	// forever and any typed input is swallowed as the (never-arriving) reply — the
	// exact CI hang this reproduces. We answer ESC[6n with a cursor-position report
	// ("ESC[<rows>;<cols>R") so the shell proceeds to its prompt. (It also emits an
	// OSC 11 background-color query, "ESC]11;?"; busybox does NOT block on that one,
	// and answering it leaks the reply onto the command line, so we leave it be.)
	//
	// The input is still deferred until the child's startup output goes quiet — it
	// has drawn its prompt and is ready for a command — so it is not read as part
	// of a query reply; blasting it up front also used to deadlock past the PTY
	// buffer. Reading a PTY master after the child exits yields an *os.PathError
	// wrapping EIO on Linux — the normal end-of-stream, not a failure.
	cpr := []byte(fmt.Sprintf("\x1b[%d;%dR", rows, cols))
	dsr := []byte("\x1b[6n")
	var buf bytes.Buffer
	activity := make(chan struct{}, 1)
	readDone := make(chan error, 1)
	go func() {
		b := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(b)
			if n > 0 {
				buf.Write(b[:n])
				for i := 0; i < bytes.Count(b[:n], dsr); i++ {
					_, _ = ptmx.Write(cpr) // answer the cursor-position query
				}
				select {
				case activity <- struct{}{}:
				default:
				}
			}
			if rerr != nil {
				readDone <- rerr
				return
			}
		}
	}()

	if input != "" {
		go writeAfterSettle(h.ctx, ptmx, input, activity)
	}

	// buf is read only after readDone fires (or after Kill + <-readDone), which
	// happens-after the reader goroutine's final write, so there is no data race.
	var out []byte
	timedOut := false
	select {
	case rerr := <-readDone:
		out = buf.Bytes()
		if rerr != nil && !isPTYEOF(rerr) {
			return nil, fmt.Errorf("exec_tty: read pty: %w", rerr)
		}
	case <-time.After(dur):
		timedOut = true
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-readDone // Read unblocks once the process is gone / ptmx closes
		out = buf.Bytes()
	case <-h.ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-readDone
		return nil, h.ctx.Err()
	}

	// The exit code: `cornus exec` and the docker CLI os.Exit with the remote
	// process's status, so a non-zero code arrives as an *exec.ExitError.
	code := 0
	werr := cmd.Wait()
	if ee, ok := werr.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if werr != nil && !timedOut {
		return nil, fmt.Errorf("exec_tty %s: %w", strings.Join(cmdArgs, " "), werr)
	}
	if timedOut {
		return nil, fmt.Errorf("exec_tty %s: timed out after %s\noutput so far:\n%s", strings.Join(cmdArgs, " "), timeout, string(out))
	}
	return anyDict(map[string]any{"output": string(out), "code": code}), nil
}

// isPTYEOF reports whether err from reading a PTY master is the expected
// end-of-stream: a plain io.EOF, or the *os.PathError wrapping EIO that Linux
// returns once the child has closed the slave.
func isPTYEOF(err error) bool {
	if err == nil || err == io.EOF {
		return true
	}
	var pe *os.PathError
	if errors.As(err, &pe) && errors.Is(pe.Err, syscall.EIO) {
		return true
	}
	return errors.Is(err, syscall.EIO)
}

// execTTYSettle and execTTYMaxWait tune writeAfterSettle: how long the child's
// output must be idle before pre-typed input is written, and the overall cap so a
// silent child still gets its input. They are vars (not consts) so tests can
// shrink them.
var (
	execTTYSettle  = 400 * time.Millisecond
	execTTYMaxWait = 5 * time.Second
)

// writeAfterSettle writes input to an interactive PTY only once the child's
// startup output has gone quiet, so an interactive shell has reached its prompt
// and the pre-typed bytes are not swallowed by its line-editor cursor-position
// query (ESC[6n). It waits for the first output (the prompt), then for a `settle`
// gap with no further output, all bounded by an overall deadline so a silent
// child still receives the input. `activity` is pulsed by the reader on every
// read from the same PTY.
func writeAfterSettle(ctx context.Context, w io.Writer, input string, activity <-chan struct{}) {
	settle, maxWait := execTTYSettle, execTTYMaxWait
	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()

	// Wait for the child to start producing output (its prompt), or write anyway
	// if it stays silent until the deadline.
	select {
	case <-activity:
	case <-deadline.C:
		_, _ = io.WriteString(w, input)
		return
	case <-ctx.Done():
		return
	}

	// Then wait until output has been idle for `settle` — prompt fully drawn and
	// the cursor-position query timed out — bounded by the overall deadline.
	quiet := time.NewTimer(settle)
	defer quiet.Stop()
	for {
		select {
		case <-activity:
			if !quiet.Stop() {
				<-quiet.C
			}
			quiet.Reset(settle)
		case <-quiet.C:
			_, _ = io.WriteString(w, input)
			return
		case <-deadline.C:
			_, _ = io.WriteString(w, input)
			return
		case <-ctx.Done():
			return
		}
	}
}

// bWriteFile writes content to path (creating parent dirs), so a scenario can seed
// or MUTATE a build context file between builds — e.g. touch a file a RUN step
// consumes to prove the build cache invalidates. Path is taken verbatim (scenarios
// point it at a mktemp dir), so it never has to touch the committed tree.
func (h *Harness) bWriteFile(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path, content string
	if err := starlark.UnpackArgs("write_file", args, kwargs, "path", &path, "content", &content); err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("write_file %s: %w", path, err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("write_file %s: %w", path, err)
	}
	return starlark.None, nil
}

// bReadFile returns the contents of path as a string (verbatim, NOT trimmed —
// unlike sh(), so exact-content assertions see what is really in the file). If
// the file does not exist and `default` was given, that value is returned
// instead, so a scenario can poll for a file a workload writes asynchronously
// without shelling out to `cat ... || true`.
func (h *Harness) bReadFile(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	var def starlark.Value
	if err := starlark.UnpackArgs("read_file", args, kwargs, "path", &path, "default?", &def); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if def != nil && def != starlark.None && errors.Is(err, os.ErrNotExist) {
			return def, nil
		}
		return nil, fmt.Errorf("read_file %s: %w", path, err)
	}
	return starlark.String(b), nil
}

// bTempDir creates a fresh temporary directory and returns its path — the
// harness replacement for the `sh(cmd="mktemp -d")` idiom. The dir is chmod'd
// 0755 (mktemp/MkdirTemp default to 0700) because scenarios bind-mount these
// dirs into containers whose processes may run as a non-root uid and must be
// able to traverse them (e.g. nginx serving a mounted docroot).
func (h *Harness) bTempDir(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("temp_dir", args, kwargs); err != nil {
		return nil, err
	}
	d, err := os.MkdirTemp("", "cornus-e2e-scenario-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(d, 0o755); err != nil {
		return nil, fmt.Errorf("temp_dir: %w", err)
	}
	return starlark.String(d), nil
}

func (h *Harness) exec(bin string) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var cmdArgs []string
		for _, a := range args {
			s, ok := starlark.AsString(a)
			if !ok {
				return nil, fmt.Errorf("%s: arguments must be strings", bin)
			}
			cmdArgs = append(cmdArgs, s)
		}
		// Opt-in retry=<duration>: re-run the command until it exits 0, up to the
		// deadline. A resource queried straight after a deploy (e.g. `kubectl get
		// pvc <name>` right after wait(running=1)) can race the backend's async
		// reconcile and momentarily 404, which fails the whole scenario. Callers
		// that expect the target to appear shortly pass retry= to poll through that
		// window; without it the behavior is unchanged (single attempt, fail-hard).
		var retry string
		for _, kv := range kwargs {
			k, _ := starlark.AsString(kv[0])
			switch k {
			case "retry":
				retry, _ = starlark.AsString(kv[1])
			default:
				return nil, fmt.Errorf("%s: unexpected keyword argument %q", bin, k)
			}
		}
		if retry != "" {
			d, perr := time.ParseDuration(retry)
			if perr != nil {
				return nil, fmt.Errorf("%s: retry: %w", bin, perr)
			}
			deadline := time.Now().Add(d)
			var out string
			var err error
			for {
				out, err = h.capture(bin, h.toolEnv(), cmdArgs...)
				if err == nil {
					return starlark.String(out), nil
				}
				if !time.Now().Before(deadline) {
					return nil, fmt.Errorf("%s %s (after retry %s): %w: %s", bin, strings.Join(cmdArgs, " "), retry, err, out)
				}
				select {
				case <-h.ctx.Done():
					return nil, h.ctx.Err()
				case <-time.After(500 * time.Millisecond):
				}
			}
		}
		out, err := h.capture(bin, h.toolEnv(), cmdArgs...)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w: %s", bin, strings.Join(cmdArgs, " "), err, out)
		}
		return starlark.String(out), nil
	}
}

// toolEnv augments the environment for kubectl/kind so they target the harness's
// kind cluster.
func (h *Harness) toolEnv() []string {
	if kt, ok := h.target.(*KubeTarget); ok && kt.Kubeconfig() != "" {
		return []string{"KUBECONFIG=" + kt.Kubeconfig()}
	}
	return nil
}

// --- helpers ----------------------------------------------------------------

func (h *Harness) stream(bin string, extraEnv []string, args ...string) error {
	cmd := exec.CommandContext(h.ctx, bin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout, cmd.Stderr = h.out, h.out
	return cmd.Run()
}

// streamCapture runs the cornus binary, teeing its combined output to h.out
// (so the scenario log stays live) AND into a buffer it returns, so a builtin
// can assert on build progress markers.
func (h *Harness) streamCapture(extraEnv []string, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(h.ctx, h.cornusBin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	w := io.MultiWriter(h.out, &buf)
	cmd.Stdout, cmd.Stderr = w, w
	err := cmd.Run()
	return buf.String(), err
}

func (h *Harness) capture(bin string, extraEnv []string, args ...string) (string, error) {
	return h.captureCtx(h.ctx, bin, extraEnv, args...)
}

// captureCtx is capture bounded by ctx, so a caller can impose a per-command
// timeout (e.g. the compose builtin's defensive cap) on top of the harness ctx.
func (h *Harness) captureCtx(ctx context.Context, bin string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	// Once ctx cancels (e.g. the compose cap fires) CommandContext kills the
	// process, but CombinedOutput would still block until the output pipes close —
	// which a surviving grandchild could hold open indefinitely. WaitDelay bounds
	// that post-kill wait so the timeout actually takes effect.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (h *Harness) waitHealthy(url string) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(h.ctx, http.MethodGet, url, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("server did not become healthy at %s", url)
}

func (h *Harness) stopServer() {
	// Stop any long-lived deploy_attach processes first: killing them drops the
	// caller connection, so the server tears the workload down and unwinds mounts.
	for name, at := range h.attaches {
		if at.cmd.Process != nil {
			_ = at.cmd.Process.Kill()
		}
		<-at.done
		delete(h.attaches, name)
	}
	// Stop any leftover backgrounded foreground `compose up` processes (a scenario
	// that didn't compose_up_wait them). SIGINT lets each unwind its held
	// mounts/forwards cleanly before the server goes; force-kill if it lingers.
	for key, bg := range h.composeUps {
		if bg.cmd.Process != nil {
			_ = bg.cmd.Process.Signal(syscall.SIGINT)
		}
		select {
		case <-bg.done:
		case <-time.After(15 * time.Second):
			if bg.cmd.Process != nil {
				_ = bg.cmd.Process.Kill()
			}
			<-bg.done
		}
		delete(h.composeUps, key)
	}
	// The unified client agent holds every compose mount session and docker
	// frontend as a detached background process; stop it (gracefully unwinding
	// those workloads) before the server dies. Best-effort; the per-scenario
	// CORNUS_AGENT_DIR keeps it isolated so this never touches a developer's agent.
	if h.agentDir != "" {
		stop := exec.Command(h.cornusBin, "daemon", "stop")
		stop.Env = append(os.Environ(), "CORNUS_AGENT_DIR="+h.agentDir)
		_ = stop.Run()
		_ = os.Unsetenv("CORNUS_AGENT_DIR")
		h.agentDir = ""
	}
	// The dockerd proxy process (a now-orphaned foreground CLI) is reaped next.
	if h.dockerd != nil && h.dockerd.Process != nil {
		_ = h.dockerd.Process.Kill()
		_ = h.dockerd.Wait()
		h.dockerd = nil
	}
	if h.server != nil && h.server.Process != nil {
		_ = h.server.Process.Kill()
		_ = h.server.Wait()
		h.server = nil
	}
	if h.sshAgentPID != "" {
		_ = exec.Command("kill", h.sshAgentPID).Run()
		h.sshAgentPID = ""
	}
	if h.sshd != nil && h.sshd.Process != nil {
		_ = h.sshd.Process.Kill()
		_ = h.sshd.Wait()
		h.sshd = nil
	}
	if h.dataRoot != "" {
		_ = os.RemoveAll(h.dataRoot)
		h.dataRoot = ""
	}
}

// bSSHAgent starts an ssh-agent with a fresh ed25519 key for the scenario, so a
// subsequent build(ssh="default") forwards it. Returns the key fingerprint.
func (h *Harness) bSSHAgent(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("ssh_agent", args, kwargs); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "cornus-e2e-agent-")
	if err != nil {
		return nil, err
	}
	key := filepath.Join(dir, "id_ed25519")
	if out, err := h.capture("ssh-keygen", nil, "-t", "ed25519", "-N", "", "-f", key, "-q"); err != nil {
		return nil, fmt.Errorf("ssh-keygen: %w: %s", err, out)
	}
	sock := filepath.Join(dir, "agent.sock")
	out, err := h.capture("ssh-agent", nil, "-a", sock)
	if err != nil {
		return nil, fmt.Errorf("ssh-agent: %w: %s", err, out)
	}
	h.sshAuthSock = sock
	h.sshAgentPID = parseAgentPID(out)
	if out, err := h.capture("ssh-add", []string{"SSH_AUTH_SOCK=" + sock}, key); err != nil {
		return nil, fmt.Errorf("ssh-add: %w: %s", err, out)
	}
	fp, err := h.capture("ssh-keygen", nil, "-lf", key+".pub")
	if err != nil {
		return nil, fmt.Errorf("ssh fingerprint: %w", err)
	}
	h.logf("✓ ssh-agent ready (%s)", fp)
	return starlark.String(fp), nil
}

// sshdCandidatePaths are where the sshd binary typically lives (not usually on
// PATH for a non-root user).
var sshdCandidatePaths = []string{"/usr/sbin/sshd", "/usr/bin/sshd", "/sbin/sshd"}

// bSSHD starts an OpenSSH sshd on a free loopback port for the ssh-tunnel
// connection scenario, with a fresh host key and a fresh client key authorized for
// the current user. It returns a dict {host, port, addr, user, identity,
// known_hosts} the scenario feeds into `cornus config set-context --ssh-*`. The
// process is killed on scenario teardown. It self-skips (returns None) when no sshd
// binary is present.
func (h *Harness) bSSHD(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("sshd", args, kwargs); err != nil {
		return nil, err
	}
	sshdBin := ""
	for _, p := range sshdCandidatePaths {
		if _, err := os.Stat(p); err == nil {
			sshdBin = p
			break
		}
	}
	if sshdBin == "" {
		h.logf("sshd: no sshd binary found; scenario should skip")
		return starlark.None, nil
	}

	dir, err := os.MkdirTemp("", "cornus-e2e-sshd-")
	if err != nil {
		return nil, err
	}
	hostKey := filepath.Join(dir, "hostkey")
	clientKey := filepath.Join(dir, "id_ed25519")
	if out, err := h.capture("ssh-keygen", nil, "-t", "ed25519", "-N", "", "-f", hostKey, "-q"); err != nil {
		return nil, fmt.Errorf("sshd host key: %w: %s", err, out)
	}
	if out, err := h.capture("ssh-keygen", nil, "-t", "ed25519", "-N", "", "-f", clientKey, "-q"); err != nil {
		return nil, fmt.Errorf("sshd client key: %w: %s", err, out)
	}
	// Authorize the client key.
	clientPub, err := os.ReadFile(clientKey + ".pub")
	if err != nil {
		return nil, fmt.Errorf("sshd read client pub: %w", err)
	}
	authKeys := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authKeys, clientPub, 0o600); err != nil {
		return nil, fmt.Errorf("sshd authorized_keys: %w", err)
	}
	// Pin the host key in a known_hosts for [127.0.0.1]:port.
	hostPub, err := os.ReadFile(hostKey + ".pub")
	if err != nil {
		return nil, fmt.Errorf("sshd read host pub: %w", err)
	}

	addr, err := freePort() // 127.0.0.1:PORT
	if err != nil {
		return nil, err
	}
	_, port, _ := net.SplitHostPort(addr)

	knownHosts := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(knownHosts, []byte(fmt.Sprintf("[127.0.0.1]:%s %s", port, strings.TrimSpace(string(hostPub)))), 0o600); err != nil {
		return nil, fmt.Errorf("sshd known_hosts: %w", err)
	}

	user := currentUser()
	pidFile := filepath.Join(dir, "sshd.pid")
	logFile := filepath.Join(dir, "sshd.log")
	cfg := strings.Join([]string{
		"Port " + port,
		"ListenAddress 127.0.0.1",
		"HostKey " + hostKey,
		"PidFile " + pidFile,
		"AuthorizedKeysFile " + authKeys,
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"UsePAM no",
		"StrictModes no",
		"AllowTcpForwarding yes",
		"LogLevel VERBOSE",
		"",
	}, "\n")
	cfgPath := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return nil, fmt.Errorf("sshd config: %w", err)
	}

	// Ensure the privilege-separation directory exists. Modern OpenSSH sshd aborts
	// at startup with "Missing privilege separation directory: /run/sshd" when its
	// compiled-in privsep dir is absent. In the containerized E2E runner /run is a
	// fresh tmpfs at container start, so the openssh-server package's /run/sshd does
	// not survive — recreate it best-effort (a no-op where it already exists, e.g. a
	// systemd dev host). Ignore the error: if we cannot create it (unprivileged),
	// sshd fails exactly as it does today, so this only ever helps.
	_ = os.MkdirAll("/run/sshd", 0o755)

	// sshd requires an absolute config path; -D keeps it in the foreground so the
	// harness owns the process and kills it on teardown.
	cmd := exec.CommandContext(h.ctx, sshdBin, "-D", "-f", cfgPath, "-E", logFile)
	cmd.Stdout, cmd.Stderr = h.out, h.out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start sshd: %w", err)
	}
	h.sshd = cmd

	// Wait for the port to accept connections.
	if err := waitForListen(addr, 10*time.Second); err != nil {
		if b, rerr := os.ReadFile(logFile); rerr == nil {
			h.logf("sshd log:\n%s", b)
		}
		return nil, fmt.Errorf("sshd did not come up on %s: %w", addr, err)
	}
	h.logf("✓ sshd ready on %s (user %s)", addr, user)

	return anyDict(map[string]any{
		"host":        "127.0.0.1",
		"port":        port,
		"addr":        addr,
		"user":        user,
		"identity":    clientKey,
		"known_hosts": knownHosts,
	}), nil
}

// currentUser returns the invoking user's name for the sshd scenario (sshd
// authenticates that user; as non-root it can only be the invoker).
func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if n := os.Getenv("USER"); n != "" {
		return n
	}
	return "root"
}

// waitForListen blocks until a TCP dial to addr succeeds or the timeout elapses.
func waitForListen(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", addr)
}

// parseAgentPID extracts SSH_AGENT_PID=<n> from ssh-agent's output.
func parseAgentPID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "SSH_AGENT_PID="); i >= 0 {
			rest := line[i+len("SSH_AGENT_PID="):]
			pid := rest
			if j := strings.IndexAny(rest, "; \t"); j >= 0 {
				pid = rest[:j]
			}
			return pid
		}
	}
	return ""
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

func parsePort(s string) (api.PortMapping, error) {
	proto := "tcp"
	if i := strings.LastIndex(s, "/"); i >= 0 {
		proto, s = s[i+1:], s[:i]
	}
	host, container, ok := strings.Cut(s, ":")
	if !ok {
		host, container = s, s
	}
	hp, err := strconv.Atoi(host)
	if err != nil {
		return api.PortMapping{}, fmt.Errorf("invalid port %q", s)
	}
	cp, err := strconv.Atoi(container)
	if err != nil {
		return api.PortMapping{}, fmt.Errorf("invalid port %q", s)
	}
	return api.PortMapping{Host: hp, Container: cp, Protocol: proto}, nil
}

// parseIngressSpec builds an api.IngressSpec from the deploy() ingress kwarg: a
// bare string (a single host) or a {str: str} dict with keys host / hosts (comma-
// separated) / domain / path / path_type / port / class_name / tls_secret /
// tls_issuer / enabled. Its presence enables ingress; enabled="false" disables it,
// and tls_secret/tls_issuer add a TLS block.
func parseIngressSpec(v starlark.Value) (*api.IngressSpec, error) {
	if s, ok := v.(starlark.String); ok {
		return &api.IngressSpec{Enabled: true, Hosts: []string{string(s)}}, nil
	}
	m, err := strMap(v)
	if err != nil {
		return nil, fmt.Errorf("ingress: %w", err)
	}
	is := &api.IngressSpec{Enabled: true}
	for k, val := range m {
		switch k {
		case "enabled":
			is.Enabled = val != "false"
		case "host":
			is.Hosts = append(is.Hosts, val)
		case "hosts":
			// comma-separated (dict values are strings).
			for _, h := range strings.Split(val, ",") {
				if h = strings.TrimSpace(h); h != "" {
					is.Hosts = append(is.Hosts, h)
				}
			}
		case "domain":
			is.Domain = val
		case "subdomain":
			is.Subdomain = val
		case "path":
			is.Path = val
		case "path_type":
			is.PathType = val
		case "class_name":
			is.ClassName = val
		case "port":
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("ingress: port %q: %w", val, err)
			}
			is.Port = n
		case "tls_secret":
			if is.TLS == nil {
				is.TLS = &api.IngressTLS{}
			}
			is.TLS.SecretName = val
		case "tls_issuer":
			if is.TLS == nil {
				is.TLS = &api.IngressTLS{}
			}
			is.TLS.ClusterIssuer = val
		default:
			return nil, fmt.Errorf("ingress: unknown key %q", k)
		}
	}
	return is, nil
}

// parseKnativeSpec builds an api.KnativeSpec from the deploy() knative= kwarg:
// True (or an empty dict) enables it with defaults, or a dict of string values
// sets the autoscaling knobs. Mirrors parseIngressSpec's string-valued-dict
// convention.
func parseKnativeSpec(v starlark.Value) (*api.KnativeSpec, error) {
	if b, ok := v.(starlark.Bool); ok {
		if !bool(b) {
			return nil, nil
		}
		return &api.KnativeSpec{Enabled: true}, nil
	}
	m, err := strMap(v)
	if err != nil {
		return nil, fmt.Errorf("knative: %w", err)
	}
	kn := &api.KnativeSpec{Enabled: true}
	intPtr := func(key, val string) (*int, error) {
		n, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("knative: %s %q: %w", key, val, err)
		}
		return &n, nil
	}
	for k, val := range m {
		switch k {
		case "enabled":
			kn.Enabled = val != "false"
		case "min_scale":
			if kn.MinScale, err = intPtr(k, val); err != nil {
				return nil, err
			}
		case "max_scale":
			if kn.MaxScale, err = intPtr(k, val); err != nil {
				return nil, err
			}
		case "target":
			if kn.Target, err = intPtr(k, val); err != nil {
				return nil, err
			}
		case "concurrency":
			if kn.Concurrency, err = intPtr(k, val); err != nil {
				return nil, err
			}
		case "timeout_seconds":
			if kn.TimeoutSeconds, err = intPtr(k, val); err != nil {
				return nil, err
			}
		case "class":
			kn.Class = val
		case "metric":
			kn.Metric = val
		case "port":
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("knative: port %q: %w", val, err)
			}
			kn.Port = n
		default:
			return nil, fmt.Errorf("knative: unknown key %q", k)
		}
	}
	return kn, nil
}

// parseHubSpec builds an api.HubSpec from the deploy() hub_* kwargs, or nil when
// none are set. export entries are "name=port[/udp][:deliver]"; import entries are
// "name=port[/udp][,port...]". A "/udp" protocol suffix on the port selects UDP
// (datagrams are length-prefix framed over the byte-agnostic hub relay); the
// default is TCP.
func parseHubSpec(identity string, export, import_ starlark.Value) (*api.HubSpec, error) {
	exp, err := strSlice(export)
	if err != nil {
		return nil, err
	}
	imp, err := strSlice(import_)
	if err != nil {
		return nil, err
	}
	if identity == "" && len(exp) == 0 && len(imp) == 0 {
		return nil, nil
	}
	hs := &api.HubSpec{Identity: identity}
	for _, e := range exp {
		name, rest, ok := strings.Cut(e, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("hub_export %q: want name=port[/udp][:deliver]", e)
		}
		portSpec, mode, _ := strings.Cut(rest, ":")
		portStr, proto := splitProto(portSpec)
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("hub_export %q: bad port: %w", e, err)
		}
		hs.Export = append(hs.Export, api.HubExport{Name: name, Port: port, Deliver: mode == "deliver", Protocol: proto})
	}
	for _, im := range imp {
		name, portsStr, ok := strings.Cut(im, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("hub_import %q: want name=port[/udp][,port...]", im)
		}
		var ports []int
		var proto string
		for _, ps := range strings.Split(portsStr, ",") {
			portStr, p2 := splitProto(ps)
			if p2 != "" {
				proto = p2
			}
			p, err := strconv.Atoi(portStr)
			if err != nil {
				return nil, fmt.Errorf("hub_import %q: bad port %q: %w", im, ps, err)
			}
			ports = append(ports, p)
		}
		hs.Import = append(hs.Import, api.HubImport{Name: name, Ports: ports, Protocol: proto})
	}
	return hs, nil
}

// splitProto splits a "port[/proto]" token into the port string and the protocol
// ("" for the TCP default, "udp" for a "/udp" suffix).
func splitProto(s string) (port, proto string) {
	port, suffix, ok := strings.Cut(s, "/")
	if !ok {
		return s, ""
	}
	return port, suffix
}

// parseMount parses a host bind-mount spec "src:dst[:ro]" into an api.Mount.
func parseMount(s string) (api.Mount, error) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return api.Mount{}, fmt.Errorf("invalid mount %q (want src:dst[:ro])", s)
	}
	m := api.Mount{Source: parts[0], Target: parts[1]}
	if len(parts) == 3 {
		if parts[2] != "ro" {
			return api.Mount{}, fmt.Errorf("invalid mount mode %q in %q (want ro)", parts[2], s)
		}
		m.ReadOnly = true
	}
	return m, nil
}

func countRunning(st api.DeployStatus) int {
	n := 0
	for _, in := range st.Instances {
		if in.Running {
			n++
		}
	}
	return n
}

func statusDict(st api.DeployStatus) *starlark.Dict {
	insts := make([]any, len(st.Instances))
	for i, in := range st.Instances {
		m := map[string]any{
			"id":      in.ID,
			"state":   in.State,
			"running": in.Running,
			"health":  in.Health,
			// message carries a diagnostic for a stuck instance (e.g.
			// "app: CrashLoopBackOff: ...") so a scenario can assert on why a
			// workload is not running.
			"message": in.Message,
		}
		if in.ExitCode != nil {
			m["exit_code"] = *in.ExitCode
		}
		insts[i] = m
	}
	return anyDict(map[string]any{
		"name":      st.Name,
		"image":     st.Image,
		"backend":   st.Backend,
		"running":   countRunning(st),
		"total":     len(st.Instances),
		"instances": insts,
		// url carries a Knative Service's status.url (empty for non-Knative
		// workloads), so a scenario can http_get the serverless front door.
		"url": st.URL,
	})
}
