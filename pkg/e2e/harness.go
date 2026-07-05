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
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	starlarkjson "go.starlark.net/lib/json"
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
	// serverLog accumulates the running server's own stdout+stderr, for the
	// server_log() builtin. Replaced on each serve(); nil before the first.
	serverLog *syncBuffer

	ctx          context.Context
	registryHost string
	client       *client.Client
	server       *exec.Cmd
	// serverContainer is the container name when the server was started by
	// serve_container() instead of as a host process; "" otherwise. Removed in
	// stopServer, which cannot kill it as a process.
	serverContainer string
	// skips collects the self-skip messages the running scenario logged, so the
	// runner can report them instead of folding them into "all N passed". A
	// scenario that decides it cannot run says so with log(); nothing else in the
	// suite noticed, which is how a registered, parse-checked, subset-listed
	// scenario went four consecutive green runs without executing once.
	skips []string
	// serverInstance is the incus instance name when the server was started by
	// serve_instance(); "" otherwise. Separate from serverContainer because the
	// two are torn down by different daemons, and a single field would make
	// stopServer guess which CLI to reach for.
	serverInstance string

	sshAuthSock string // set by ssh_agent()
	sshAgentPID string // killed on teardown

	sshd *exec.Cmd // background sshd started by sshd(), killed on teardown

	// tempDirs records every directory temp_dir() minted for this scenario. It is
	// the ONLY thing remove_all will delete inside (see removeall.go): the sandbox
	// is anchored to harness-created dirs, not to the caller's argument.
	tempDirsMu sync.Mutex
	tempDirs   []string
	dataRoot   string // temp root for isolated per-role data dirs
	buildSeq   int    // counter for fresh_cache isolated build data dirs

	advertiseURL string                // server URL an in-cluster pod dials (kube mount relay)
	attaches     map[string]*attach    // long-lived deploy_attach processes, by name
	composeUps   map[string]*bgCompose // backgrounded foreground `compose up` processes, by handle

	// deployed and composedUp record what THIS scenario created, so end-of-scenario
	// cleanup can remove exactly that and nothing else. See reapScenarioWorkloads
	// for why the obvious alternative is dangerous.
	deployed   map[string]bool    // deployment names from deploy() / deploy_attach()
	composedUp map[[2]string]bool // (file, project) pairs from compose_up()

	dockerd     *exec.Cmd // `cornus daemon docker` proxy process (dockerd_up), killed on teardown
	dockerdSock string    // its unix socket path

	// agentDir isolates this scenario's unified client agent (CORNUS_AGENT_DIR),
	// set at serve() and `cornus daemon stop`-ped on teardown so `compose up -d`
	// and `dockerd_up` share one agent per scenario, never across scenarios.
	agentDir string

	portForwards []*exec.Cmd // background `cornus port-forward` processes, killed on teardown

	webs []*exec.Cmd // background `cornus web` processes (web()), killed on teardown

	// webPublished holds the `cornus web --publish-in-conduit` clients (web(publish=True)),
	// keyed by the conduit name the UI is published under. Unlike a port-bound
	// `cornus web` these are stopped mid-scenario by web_stop() so a scenario can
	// assert the published name is WITHDRAWN when the client exits; whatever is
	// left is interrupted on teardown.
	webPublished map[string]*bgWeb

	mcpSessions map[string]*mcpSession // live `cornus web --mcp-stdio` clients, by opaque scenario handle
	mcpSeq      int                    // monotonically increasing MCP handle suffix

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
		// os.MkdirTemp creates 0700, which makes this root untraversable by any
		// user but the harness — an artifact of the temp dir, not of how cornus
		// is installed. A production data dir sits under /var/lib, whose parents
		// are 0755, and a runtime that reaches cornus's files as an ORDINARY USER
		// (rootless podman, incus) has to walk that path.
		//
		// Without this the rootless leg fails with `statfs ...: permission
		// denied` on a directory cornus never created, which reads exactly like a
		// cornus bug and is not one. 0711: traversable, still not listable.
		if err := os.Chmod(root, 0o711); err != nil {
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
		"TARGET", "CORNUS_BIN", "log", "sleep", "serve", "serve_container", "serve_instance", "stop_server", "build", "ssh_agent", "sshd", "deploy",
		"deploy_attach", "attach_stop", "pod_exec",
		"status", "stats", "wait", "start", "stop", "restart", "remove",
		"compose_up", "compose_ps", "compose_down",
		"compose_build", "compose_stop", "compose_start", "compose_restart",
		"compose_up_bg", "compose_up_wait", "compose_up_stop",
		"devcontainer_up", "devcontainer_ps", "devcontainer_down",
		"registry_roundtrip", "upstream_registry", "build_upload", "cornus", "cornus_stream", "cornus_bg", "dockerd_up", "docker_compose", "devcontainer_cli", "port_forward", "tunnel", "ingress_tunnel", "free_port", "server_log",
		"redis", "tcp_echo", "dial_echo",
		"web", "web_stop", "frontend_stub",
		"mcp_stdio", "mcp_list_tools", "mcp_call", "mcp_list_resources", "mcp_read_resource", "mcp_close",
		"egress_proxy", "egress_proxy_hits",
		"trace_sink", "trace_sink_headers",
		"otlp_collector", "otlp_spans", "otlp_logs", "otlp_metrics",
		"http_get", "http", "ftp_roundtrip", "sh", "exec_tty", "write_file", "read_file", "temp_dir", "remove_all", "kubectl", "docker", "kind",
		"getenv",
		"now", "benchmark", "bench_record",
		"assert_eq", "assert_true", "assert_contains", "fail",
		"json",
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
	// AFTER stopServer in source order means BEFORE it at run time (defers are
	// LIFO), which is what this needs: the server has to still be alive to be
	// asked to remove anything.
	defer h.reapScenarioWorkloads()
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
		"TARGET": starlark.String(h.target.Name()),
		// CORNUS_BIN is the binary UNDER TEST, for the scenarios that must run it
		// somewhere the harness does not manage — inside a container they create
		// themselves, to exercise the server's own view of a containerized host.
		//
		// Exposed rather than left to getenv so it cannot drift from the binary
		// every other builtin uses. That drift is a real hazard here, not a
		// hypothetical: server-in-container.star carries a warning about a stale
		// image silently exercising old server code, which once cost a mutation
		// check that "passed" against a server that never had the mutation.
		"CORNUS_BIN":         starlark.String(h.cornusBin),
		"log":                bi("log", h.bLog),
		"sleep":              bi("sleep", h.bSleep),
		"serve":              bi("serve", h.bServe),
		"serve_container":    bi("serve_container", h.bServeContainer),
		"serve_instance":     bi("serve_instance", h.bServeInstance),
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
		"redis":              bi("redis", h.bRedis),
		"cornus_bg":          bi("cornus_bg", h.bCornusBG),
		"tcp_echo":           bi("tcp_echo", h.bTCPEcho),
		"dial_echo":          bi("dial_echo", h.bDialEcho),
		"build_upload":       bi("build_upload", h.bBuildUpload),
		"cornus":             bi("cornus", h.bCornus),
		"cornus_stream":      bi("cornus_stream", h.bCornusStream),
		"dockerd_up":         bi("dockerd_up", h.bDockerdUp),
		"port_forward":       bi("port_forward", h.bPortForward),
		"server_log":         bi("server_log", h.bServerLog),
		"tunnel":             bi("tunnel", h.bTunnel),
		"ingress_tunnel":     bi("ingress_tunnel", h.bIngressTunnel),
		"web":                bi("web", h.bWeb),
		"web_stop":           bi("web_stop", h.bWebStop),
		"frontend_stub":      bi("frontend_stub", h.bFrontendStub),
		"mcp_stdio":          bi("mcp_stdio", h.bMCPStdio),
		"mcp_list_tools":     bi("mcp_list_tools", h.bMCPListTools),
		"mcp_call":           bi("mcp_call", h.bMCPCall),
		"mcp_list_resources": bi("mcp_list_resources", h.bMCPListResources),
		"mcp_read_resource":  bi("mcp_read_resource", h.bMCPReadResource),
		"mcp_close":          bi("mcp_close", h.bMCPClose),
		"free_port":          bi("free_port", h.bFreePort),
		"egress_proxy":       bi("egress_proxy", h.bEgressProxy),
		"egress_proxy_hits":  bi("egress_proxy_hits", h.bEgressProxyHits),
		"trace_sink":         bi("trace_sink", h.bTraceSink),
		"trace_sink_headers": bi("trace_sink_headers", h.bTraceSinkHeaders),
		"otlp_collector":     bi("otlp_collector", h.bOTLPCollector),
		"otlp_logs":          bi("otlp_logs", h.bOTLPLogs),
		"otlp_metrics":       bi("otlp_metrics", h.bOTLPMetrics),
		"otlp_spans":         bi("otlp_spans", h.bOTLPSpans),
		"docker_compose":     bi("docker_compose", h.bDockerCompose),
		"devcontainer_cli":   bi("devcontainer_cli", h.bDevcontainerCLI),
		// The standard Starlark json module (json.decode / json.encode). Scenarios
		// that only need to know a substring appears in a response body should keep
		// using assert_contains; this is for the cases where the SHAPE matters —
		// comparing values across a list of series, say, where substring matching
		// would pass on output that means the opposite of what was intended.
		"json":            starlarkjson.Module,
		"http_get":        bi("http_get", h.bHTTPGet),
		"http":            bi("http", h.bHTTP),
		"ftp_roundtrip":   bi("ftp_roundtrip", h.bFTPRoundtrip),
		"sh":              bi("sh", h.bSh),
		"exec_tty":        bi("exec_tty", h.bExecTTY),
		"write_file":      bi("write_file", h.bWriteFile),
		"read_file":       bi("read_file", h.bReadFile),
		"temp_dir":        bi("temp_dir", h.bTempDir),
		"remove_all":      bi("remove_all", h.bRemoveAll),
		"kubectl":         bi("kubectl", h.exec("kubectl")),
		"docker":          bi("docker", h.exec("docker")),
		"kind":            bi("kind", h.exec("kind")),
		"getenv":          bi("getenv", h.bGetenv),
		"now":             bi("now", h.bNow),
		"benchmark":       bi("benchmark", h.bBenchmark),
		"bench_record":    bi("bench_record", h.bBenchRecord),
		"assert_eq":       bi("assert_eq", h.bAssertEq),
		"assert_true":     bi("assert_true", h.bAssertTrue),
		"assert_contains": bi("assert_contains", h.bAssertContains),
		"fail":            bi("fail", h.bFail),
	}
	return d
}

// --- generic builtins -------------------------------------------------------

func (h *Harness) bLog(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var msg string
	if err := starlark.UnpackArgs("log", args, kwargs, "msg", &msg); err != nil {
		return nil, err
	}
	if isSelfSkip(msg) {
		h.skips = append(h.skips, msg)
	}
	h.logf("• %s", msg)
	return starlark.None, nil
}

// Skips returns the self-skip messages the last RunFile logged, in order.
//
// Empty for a scenario that ran normally. The runner prints these rather than
// letting them vanish into a passing count — a skip and a pass are indistinguishable
// in "all N scenario(s) passed", and that is not a hypothetical: it hid
// server-in-container-containerd.star for four green runs while it was reported as
// covering the topology it names.
func (h *Harness) Skips() []string { return h.skips }

// isSelfSkip recognizes the suite's self-skip convention, which is a log message of
// the form "<label>: skipped (<reason>)" — 168 of them across e2e/scenarios as of
// 2026-08-06.
//
// Matching on "skipped (" rather than on "skip" is deliberate. A scenario that skips
// only PART of its work and keeps going writes something else ("! curl absent:
// skipping the raw wait-body assertions (the docker CLI assertions still ran)"), and
// counting that as a skipped scenario would under-report real coverage — the opposite
// mistake, but a mistake.
//
// Detection by convention rather than structure is the cheap half of this: a skip()
// builtin would be exact and would not need a string match, but every existing
// scenario would have to adopt it before the reporting improved at all. See TODO.md.
func isSelfSkip(msg string) bool { return strings.Contains(msg, "skipped (") }

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
	replica, replicaAddr := "", ""
	var envv starlark.Value
	if err := starlark.UnpackArgs("serve", args, kwargs, "storage?", &storage, "env?", &envv, "name?", &replica, "addr?", &replicaAddr); err != nil {
		return nil, err
	}
	extraServeEnv, err := strMap(envv)
	if err != nil {
		return nil, fmt.Errorf("serve: env: %w", err)
	}
	// A named serve() is an ADDITIONAL replica: it gets its own data dir and handle
	// and leaves h.server/h.client/h.registryHost alone, so the unnamed call keeps
	// meaning "the server" for every other builtin and no existing scenario changes.
	if replica != "" {
		return h.serveReplica(replica, storage, replicaAddr, extraServeEnv)
	}
	if replicaAddr != "" {
		return nil, fmt.Errorf("serve: addr is only meaningful with name= (an additional replica)")
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
	// Tee the server's own stream so a scenario can assert on it. Some contracts
	// have NO other observable: a TCP port-forward is a raw passthrough with no
	// post-preamble error channel (see the handler in pkg/server/deploy_exec.go),
	// so a setup failure — the podman rootless refusal, a kube RBAC denial, a dead
	// pod — reaches the operator through the server log and nowhere else. Without
	// this, the diagnostic those paths exist to produce is untestable end to end,
	// and "the tunnel closed" is all a scenario could ever see.
	srvLog := &syncBuffer{}
	h.serverLog = srvLog
	cmd.Stdout, cmd.Stderr = io.MultiWriter(h.out, srvLog), io.MultiWriter(h.out, srvLog)
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

// removeServerContainer stops and removes a serve_container() server.
//
// It is a sibling container, not a child process, so nothing reaps it when the
// harness exits. The stop is GRACEFUL first, and that matters more than it
// looks: the server holds kernel 9P mounts that propagate out of its container
// onto the daemon's filesystem, and only it can unwind them. SIGKILL it (plain
// `rm -f`) while a mount is live and the mountpoint is stranded on the host,
// where clearing it needs root — the failure mode that has repeatedly poisoned
// this dev host's later runs. SIGTERM gives the server its normal shutdown, and
// `rm -f` then cleans up whatever is left.
func (h *Harness) removeServerContainer() {
	// Killing the attach CLIENT (done by the caller) makes the server tear the
	// workload down and unwind its mounts, but that happens asynchronously and
	// the server has to still be alive to do it. Destroy the container first and
	// the propagated mountpoint is stranded on the daemon's filesystem for good.
	h.waitMountsUnwound(20 * time.Second)
	_ = exec.Command("docker", "stop", "--timeout", "15", h.serverContainer).Run()
	_ = exec.Command("docker", "rm", "-f", h.serverContainer).Run()
	h.serverContainer = ""
}

// removeServerInstance stops and deletes a serve_instance() server.
//
// No graceful-stop dance is needed here, unlike removeServerContainer: this
// backend realizes no client-local mount at all (it is not a deploy.MountingBackend),
// so the server holds no kernel 9P mount that could be stranded by a hard stop.
// `delete --force` stops and removes in one call.
func (h *Harness) removeServerInstance() {
	_ = exec.Command("incus", "delete", "--force", h.serverInstance).Run()
	h.serverInstance = ""
}

// waitMountsUnwound blocks until no 9P mount remains under this harness's data
// root, or timeout elapses. Best-effort: on timeout it returns and lets the
// caller proceed, since a stranded mount is bad but hanging the whole run is
// worse.
func (h *Harness) waitMountsUnwound(timeout time.Duration) {
	if h.dataRoot == "" {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !h.hasMountsUnderDataRoot() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	h.logf("• warning: 9P mounts under %s did not unwind before the server container was removed", h.dataRoot)
}

func (h *Harness) hasMountsUnderDataRoot() bool {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false // not Linux, or unreadable: nothing we can wait on
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		if strings.HasPrefix(f[4], h.dataRoot+"/") {
			return true
		}
	}
	return false
}

// containerDataDir is where serve_container mounts the server's data directory
// inside its container. It is pinned (with a matching CORNUS_DATA) so the bind
// destination and the server's own idea of its data dir cannot drift apart —
// they must be identical for translation to resolve, and an image that sets no
// CORNUS_DATA would otherwise default somewhere else entirely.
const containerDataDir = "/var/lib/cornus"

// bServeContainer starts the cornus server AS A CONTAINER on the target's
// docker host, instead of as a host process, and points the harness at it — so
// every other builtin (deploy, deploy_attach, status, exec_tty, ...) exercises
// the containerized server unchanged.
//
// This is the one topology the rest of the suite cannot reach. A host-run server
// shares the daemon's filesystem, so the paths it hands over mean the same thing
// on both sides and no translation is ever exercised. Containerize it and they
// diverge: the interesting failure is that a client-local mount silently becomes
// an empty directory, which no assertion about the deploy SUCCEEDING would
// catch (see pkg/hostenv).
//
// data_dir controls the one bind that decides it. With it, the server's data dir
// is a host directory the daemon can also see, and mounts work; without it, the
// mount directory exists only inside the server's container and the deploy must
// be refused rather than silently emptied.
func (h *Harness) bServeContainer(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var image string
	dataDir := true
	var envv starlark.Value
	if err := starlark.UnpackArgs("serve_container", args, kwargs, "image", &image, "data_dir?", &dataDir, "env?", &envv); err != nil {
		return nil, err
	}
	if image == "" {
		return nil, fmt.Errorf("serve_container: image is required (a cornus-embedding image, e.g. cornus:e2e)")
	}
	extraEnv, err := strMap(envv)
	if err != nil {
		return nil, fmt.Errorf("serve_container: env: %w", err)
	}
	addr, err := freePort()
	if err != nil {
		return nil, err
	}
	_, port, _ := net.SplitHostPort(addr)

	// The bind source must be a path the DAEMON can resolve, which is what makes
	// this a real test of translation rather than of itself: the harness's data
	// root is on the daemon's own filesystem (the host for the docker target,
	// the runner container for the containerized runner, where the in-container
	// dockerd is the one being driven).
	hostData, err := h.dataDir("server-container")
	if err != nil {
		return nil, err
	}
	if h.agentDir == "" {
		h.agentDir = filepath.Join(h.dataRoot, "agent")
		_ = os.Setenv("CORNUS_AGENT_DIR", h.agentDir)
	}

	name := "cornus-e2e-server-" + port
	_ = exec.Command("docker", "rm", "-f", name).Run() // stale from an interrupted run
	runArgs := []string{
		"run", "-d", "--name", name,
		// Builds and the kernel 9P mount both need it, exactly as the published
		// image's own documentation says.
		"--privileged",
		"-p", "127.0.0.1:" + port + ":" + port,
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
	}
	// The bind destination and the server's data dir must be the SAME path, and
	// only the image knows its own default (the published image sets
	// CORNUS_DATA; the minimal e2e image does not, and would fall back to an XDG
	// path under $HOME). State it rather than assume it.
	runArgs = append(runArgs, "-e", "CORNUS_DATA="+containerDataDir)
	if dataDir {
		// rshared so a mount the server makes inside propagates out to the
		// daemon; without it the daemon binds the still-empty directory.
		runArgs = append(runArgs, "-v", hostData+":"+containerDataDir+":rshared")
	}
	for _, kv := range h.target.ServeEnv() {
		// DOCKER_HOST would point the containerized server at the harness's own
		// notion of the daemon; inside, it reaches it through the bind-mounted
		// socket instead.
		if strings.HasPrefix(kv, "DOCKER_HOST=") {
			continue
		}
		runArgs = append(runArgs, "-e", kv)
	}
	for k, v := range extraEnv {
		runArgs = append(runArgs, "-e", k+"="+v)
	}
	runArgs = append(runArgs, image, "serve", "--addr", ":"+port)

	if out, err := h.capture("docker", nil, runArgs...); err != nil {
		return nil, fmt.Errorf("serve_container: docker run: %w: %s", err, out)
	}
	h.serverContainer = name
	h.registryHost = addr
	h.client = client.New("http://" + addr)
	if err := h.waitHealthy("http://" + addr + "/healthz"); err != nil {
		logs, _ := h.capture("docker", nil, "logs", name)
		h.stopServer()
		return nil, fmt.Errorf("serve_container: %w\ncontainer logs:\n%s", err, logs)
	}
	h.logf("✓ serving on %s from container %s (image %s, data dir bind %v)", addr, name, image, dataDir)
	return starlark.String(addr), nil
}

// bServeInstance starts the cornus server AS AN INCUS INSTANCE on the incusd it
// manages, and points the harness at it — so every other builtin (deploy, status,
// exec_tty, port_forward, ...) exercises that server unchanged.
//
// This is the second half of in-container mode on this backend, and the half that
// serve_container cannot reach: that one shells out to `docker run`, and the incus
// E2E leg starts no dockerd at all (entrypoint.sh sets need_dockerd only for the
// docker and kube targets). On an incus host the server's container has to be made
// BY incus, which is also the arrangement worth testing — an instance sits on
// incusd's bridge alongside its workloads, so it reaches them with neither host
// networking nor a per-instance companion, and cornus recognizes that itself.
//
// Three details are load-bearing, all of them measured (see JOURNAL.md):
//
//   - the daemon socket goes in as a PROXY device, not a disk device. A disk device
//     is accepted and visible, but idmap-shifted to nobody:nobody, and cornus fails
//     with "connect: permission denied". The proxy listener is created inside the
//     instance owned by its own root.
//   - the binary under test is bind-mounted in rather than baked into an image.
//     That is not a shortcut: it makes the stale-image hazard structurally
//     impossible, which is exactly what server-in-container.star carries a loud
//     CAUTION about (a stale cornus:e2e once cost a mutation check that "passed"
//     against a server that never had the mutation).
//   - an instance's hostname IS its name, which is how the server identifies itself
//     through incusd; so the instance name is also what the preflight will report.
//
// The harness dials the instance's own bridge address, not a published port: that
// address is reachable from the runner, and it is the same address the sibling
// workloads will use to pull from this server's registry.
func (h *Harness) bServeInstance(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var image, name string
	var envv starlark.Value
	if err := starlark.UnpackArgs("serve_instance", args, kwargs,
		"image", &image, "name?", &name, "env?", &envv); err != nil {
		return nil, err
	}
	if image == "" {
		return nil, fmt.Errorf("serve_instance: image is required (an OCI ref an incus remote can pull, e.g. docker.io/library/alpine:3.20)")
	}
	target, ok := h.target.(*IncusTarget)
	if !ok {
		return nil, fmt.Errorf("serve_instance: only the incus target can host the server as an instance (target is %q)", h.target.Name())
	}
	extraEnv, err := strMap(envv)
	if err != nil {
		return nil, fmt.Errorf("serve_instance: env: %w", err)
	}
	if name == "" {
		name = "cornus-e2e-server"
	}
	// Any port works: the harness dials the instance's own address, so this need
	// not be free on the runner.
	const port = "5000"

	remote, alias, err := incusOCIRemote(image)
	if err != nil {
		return nil, fmt.Errorf("serve_instance: %w", err)
	}

	_ = exec.Command("incus", "delete", "--force", name).Run() // stale from an interrupted run

	create := []string{"create", remote + ":" + alias, name,
		"-c", "environment.CORNUS_DEPLOY_BACKEND=incus",
		"-c", "environment.CORNUS_DATA=" + containerDataDir,
		"-c", "environment.CORNUS_INCUS_SOCKET=" + instanceSocketPath,
		// PID 1 is the server itself, so stopping the instance gives it a normal
		// shutdown rather than killing a shell that happens to own it.
		"-c", `oci.entrypoint=/usr/local/bin/cornus serve --addr :` + port,
	}
	for k, v := range extraEnv {
		create = append(create, "-c", "environment."+k+"="+v)
	}
	if out, err := h.capture("incus", nil, create...); err != nil {
		return nil, fmt.Errorf("serve_instance: incus create: %w: %s", err, out)
	}

	devices := [][]string{
		// The binary under test.
		{"config", "device", "add", name, "cornusbin", "disk",
			"source=" + h.cornusBin, "path=/usr/local/bin/cornus"},
		// The daemon socket, as a proxy device — see the note above.
		{"config", "device", "add", name, "incusd", "proxy",
			"listen=unix:" + instanceSocketPath, "connect=unix:" + target.socket(),
			"bind=instance", "mode=0660"},
	}
	for _, d := range devices {
		if out, err := h.capture("incus", nil, d...); err != nil {
			h.removeInstanceQuietly(name)
			return nil, fmt.Errorf("serve_instance: %v: %w: %s", d[3:5], err, out)
		}
	}
	if out, err := h.capture("incus", nil, "start", name); err != nil {
		logs, _ := h.capture("incus", nil, "info", "--show-log", name)
		h.removeInstanceQuietly(name)
		return nil, fmt.Errorf("serve_instance: incus start: %w: %s\ninstance log:\n%s", err, out, logs)
	}
	h.serverInstance = name

	ip, err := h.waitInstanceIPv4(name)
	if err != nil {
		logs, _ := h.capture("incus", nil, "info", "--show-log", name)
		h.stopServer()
		return nil, fmt.Errorf("serve_instance: %w\ninstance log:\n%s", err, logs)
	}
	addr := net.JoinHostPort(ip, port)
	h.registryHost = addr
	h.client = client.New("http://" + addr)
	if err := h.waitHealthy("http://" + addr + "/healthz"); err != nil {
		logs, _ := h.capture("incus", nil, "console", "--show-log", name)
		h.stopServer()
		return nil, fmt.Errorf("serve_instance: %w\nconsole log:\n%s", err, logs)
	}
	h.logf("✓ serving on %s from incus instance %s (image %s)", addr, name, image)
	return starlark.String(addr), nil
}

// instanceSocketPath is where the proxy device publishes the daemon socket INSIDE
// the instance.
//
// Deliberately not the incus default /var/lib/incus/unix.socket, and this is a
// measured constraint rather than taste: a proxy device's listen side is created at
// start, and it fails outright if the parent directory does not exist in the image —
// `Failed to listen on /var/lib/incus/unix.socket: bind: no such file or directory`.
// Few images carry /var/lib/incus. /tmp always exists, so the server is pointed here
// with CORNUS_INCUS_SOCKET instead.
const instanceSocketPath = "/tmp/incus-daemon.sock"

// incusOCIRemote ensures an OCI-protocol incus remote exists for image's registry
// and returns (remote name, repo:tag).
//
// Localhost registries are addressed over http, matching the incus backend's own
// insecureRegistry rule — which is why a plain-HTTP local registry works as a
// source at all.
func incusOCIRemote(image string) (remote, alias string, err error) {
	registry, rest, found := strings.Cut(image, "/")
	if !found || !strings.ContainsAny(registry, ".:") {
		return "", "", fmt.Errorf("image %q must carry a registry host (e.g. docker.io/library/alpine:3.20)", image)
	}
	scheme := "https://"
	if strings.HasPrefix(registry, "127.0.0.1") || strings.HasPrefix(registry, "localhost") {
		scheme = "http://"
	}
	remote = "e2e-" + strings.NewReplacer(".", "-", ":", "-").Replace(registry)
	// Idempotent: a second add for an existing remote is an error we can ignore,
	// because scenarios may call this more than once in a run.
	_ = exec.Command("incus", "remote", "add", remote, scheme+registry, "--protocol=oci").Run()
	return remote, rest, nil
}

// waitInstanceIPv4 polls for the instance's bridge address, which incusd assigns by
// DHCP a moment after the instance starts.
func (h *Harness) waitInstanceIPv4(name string) (string, error) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		out, err := h.capture("incus", nil, "list", name, "-c", "4", "--format", "csv")
		if err == nil {
			// The column is "10.0.3.99 (eth0)"; take the address.
			if addr, _, _ := strings.Cut(strings.TrimSpace(out), " "); addr != "" {
				return addr, nil
			}
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("instance %s never got an IPv4 address on the incus bridge", name)
}

func (h *Harness) removeInstanceQuietly(name string) {
	_ = exec.Command("incus", "delete", "--force", name).Run()
	h.serverInstance = ""
}

// bStopServer stops the running cornus server without tearing down the rest of
// the harness (data dirs, ssh agent). Paired with a subsequent serve() against
// the SAME --storage dir, it lets a scenario prove persistence across a restart.
func (h *Harness) bStopServer(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("stop_server", args, kwargs); err != nil {
		return nil, err
	}
	h.stopMCPSessions()
	if h.serverContainer != "" {
		h.removeServerContainer()
	}
	if h.serverInstance != "" {
		h.removeServerInstance()
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
	return h.runTunnelCLI(cmdArgs, fmt.Sprintf("tunnel %s:%d", name, port))
}

// bIngressTunnel runs `cornus ingress-tunnel` and returns the public URL it
// prints, leaving the tunnel held for the rest of the scenario (torn down with
// the harness, like bTunnel's). Unlike tunnel() it fronts the server's INGRESS,
// so one URL serves every host and path the scope declares.
func (h *Harness) bIngressTunnel(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var project, deployment, server, hostMode, host, authTokenFile, proto string
	if err := starlark.UnpackArgs("ingress_tunnel", args, kwargs,
		"project?", &project, "deployment?", &deployment, "server?", &server,
		"host_mode?", &hostMode, "host?", &host, "authtoken_file?", &authTokenFile, "proto?", &proto); err != nil {
		return nil, err
	}
	if (project == "") == (deployment == "") {
		return nil, fmt.Errorf("ingress_tunnel: pass exactly one of project= or deployment=")
	}
	if server == "" {
		if h.registryHost == "" {
			return nil, fmt.Errorf("ingress_tunnel: call serve() first (or pass server=)")
		}
		server = "http://" + h.registryHost
	}
	cmdArgs := []string{"ingress-tunnel", "--server", server}
	if project != "" {
		cmdArgs = append(cmdArgs, "--project", project)
	}
	if hostMode != "" {
		cmdArgs = append(cmdArgs, "--host-mode", hostMode)
	}
	if host != "" {
		cmdArgs = append(cmdArgs, "--host", host)
	}
	if authTokenFile != "" {
		cmdArgs = append(cmdArgs, "--authtoken-file", authTokenFile)
	}
	if proto != "" {
		cmdArgs = append(cmdArgs, "--proto", proto)
	}
	if deployment != "" {
		cmdArgs = append(cmdArgs, deployment)
	}
	scope := "project " + project
	if deployment != "" {
		scope = "deployment " + deployment
	}
	return h.runTunnelCLI(cmdArgs, "ingress tunnel for "+scope)
}

// runTunnelCLI starts a long-lived tunnel CLI, returns the public URL it prints
// ("... ready at <URL>"), and leaves it running for the rest of the scenario —
// the tunnel must stay up while the scenario fetches through it. label appears in
// the harness log and in the failure message.
func (h *Harness) runTunnelCLI(cmdArgs []string, label string) (starlark.Value, error) {
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
		return nil, fmt.Errorf("start cornus %s: %w", label, err)
	}
	// Close the parent's copy of the write end; the child holds its own dup. Now
	// the reader's Scan sees EOF exactly when the tunnel process exits and its
	// stdout/stderr close, making the empty-URL early-exit path reachable.
	pw.Close()
	h.portForwards = append(h.portForwards, cmd)

	// The CLI prints "... ready at <URL>" once the tunnel is live, then blocks.
	// Scan for that line, bounded so a broken tunnel fails fast.
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
			return nil, fmt.Errorf("cornus %s exited before printing a public URL", label)
		}
		h.logf("✓ %s live at %s", label, u)
		return starlark.String(u), nil
	case <-time.After(90 * time.Second):
		return nil, fmt.Errorf("cornus %s did not report a public URL within 90s", label)
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
//
// publish=True instead runs `cornus web --publish-in-conduit`: the UI binds NO
// local port and is hosted inside the background agent, published in the SHARED
// SOCKS5 conduit under a conduit name. Nothing is returned to dial directly —
// the return value is that NAME, reachable only through the proxy
// (http_get(url="http://<name>/...", socks5=<proxy addr>)). Pass
// conduit="socks5://127.0.0.1:PORT" to pin the shared proxy's address so the
// scenario knows where to point http_get, and publish_name= to override the
// default (the conduit suffix apex, e.g. "cornus.internal"). Stop it mid-scenario
// with web_stop(<name>) to prove the name is withdrawn when the client exits.
func (h *Harness) bWeb(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var host, composeFile, project, frontend, conduit, publishName string
	mcp := true // co-hosted MCP is on by default, matching `cornus web`
	publish := false
	if err := starlark.UnpackArgs("web", args, kwargs, "host?", &host, "compose_file?", &composeFile, "project?", &project, "frontend?", &frontend, "mcp?", &mcp,
		"publish?", &publish, "conduit?", &conduit, "publish_name?", &publishName); err != nil {
		return nil, err
	}
	if host == "" {
		if h.registryHost == "" {
			return nil, fmt.Errorf("web: call serve() first (or pass host=)")
		}
		host = "http://" + h.registryHost
	}
	if publish {
		return h.webPublish(host, composeFile, project, conduit, publishName, mcp)
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

// bgWeb is a `cornus web --publish-in-conduit` client the scenario can outlive:
// it binds no port, so the only handle on it is the name it published. cmd.Wait
// runs in one goroutine that closes done, so web_stop and teardown can both
// await the exit without racing over Wait.
type bgWeb struct {
	cmd  *exec.Cmd
	buf  *lockedBuffer
	done chan struct{}
	code int
}

// lockedBuffer is an io.Writer a reader may sample WHILE the writer is running —
// which a plain bytes.Buffer is not. Needed because a published `cornus web`
// never exits on its own, so its readiness line has to be read from under it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// webPublish runs `cornus web --publish-in-conduit`, which hosts the UI inside
// the background agent and registers it in the SHARED SOCKS5 conduit instead of
// binding a local port. It returns the published NAME (the scenario reaches it
// only through the proxy), after waiting for the client to announce the
// registration — the name is live only once the agent has accepted the hold.
func (h *Harness) webPublish(host, composeFile, project, conduit, publishName string, mcp bool) (starlark.Value, error) {
	cmdArgs := []string{"web", "--publish-in-conduit", "--host", host}
	if conduit != "" {
		cmdArgs = append(cmdArgs, "--conduit", conduit)
	}
	if publishName != "" {
		cmdArgs = append(cmdArgs, "--publish-name", publishName)
	}
	if !mcp {
		cmdArgs = append(cmdArgs, "--no-mcp")
	}
	if composeFile != "" {
		cmdArgs = append(cmdArgs, "-f", composeFile)
	}
	if project != "" {
		cmdArgs = append(cmdArgs, "-p", project)
	}
	cmd := exec.CommandContext(h.ctx, h.cornusBin, cmdArgs...)
	cmd.Env = append(os.Environ(), h.target.ServeEnv()...)
	buf := &lockedBuffer{}
	w := io.MultiWriter(h.out, buf) // one value for both streams => exec serializes writes
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start cornus web --publish-in-conduit: %w", err)
	}
	bg := &bgWeb{cmd: cmd, buf: buf, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		if ee, ok := err.(*exec.ExitError); ok {
			bg.code = ee.ExitCode()
		} else if err != nil {
			bg.code = -1
		}
		close(bg.done)
	}()

	// The client prints "cornus web UI (in conduit): http://<name>/" once the agent
	// has accepted the registration. Read the name back off that line rather than
	// re-deriving the suffix-apex default here, so the scenario asserts against the
	// name the product actually published.
	const marker = "cornus web UI (in conduit): http://"
	deadline := time.Now().Add(30 * time.Second)
	for {
		out := bg.buf.String()
		if i := strings.Index(out, marker); i >= 0 {
			rest := out[i+len(marker):]
			if j := strings.IndexAny(rest, "/\r\n"); j >= 0 {
				name := strings.TrimSpace(rest[:j])
				if name != "" {
					if h.webPublished == nil {
						h.webPublished = map[string]*bgWeb{}
					}
					h.webPublished[name] = bg
					h.logf("✓ cornus web published in the conduit as %s -> %s", name, host)
					return starlark.String(name), nil
				}
			}
		}
		select {
		case <-bg.done:
			return nil, fmt.Errorf("cornus web --publish-in-conduit exited (code %d) before publishing:\n%s", bg.code, bg.buf.String())
		case <-time.After(100 * time.Millisecond):
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("cornus web --publish-in-conduit did not publish a name within 30s:\n%s", bg.buf.String())
		}
	}
}

// bWebStop interrupts a `cornus web --publish-in-conduit` client (Ctrl-C) and
// waits for it to exit, so a scenario can then assert the published name is
// WITHDRAWN from the conduit. The agent reaps the registration when the held
// connection closes, so the process exiting IS the withdrawal — which makes the
// clean exit part of the contract, not incidental. Returns {"output", "code"}.
func (h *Harness) bWebStop(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var handle, timeout string
	timeout = "30s"
	if err := starlark.UnpackArgs("web_stop", args, kwargs, "handle", &handle, "timeout?", &timeout); err != nil {
		return nil, err
	}
	bg := h.webPublished[handle]
	if bg == nil {
		return nil, fmt.Errorf("web_stop: no published `cornus web` named %q", handle)
	}
	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, err
	}
	if bg.cmd.Process != nil {
		if err := bg.cmd.Process.Signal(syscall.SIGINT); err != nil {
			return nil, fmt.Errorf("web_stop %q: signal: %w", handle, err)
		}
	}
	select {
	case <-bg.done:
	case <-time.After(dur):
		return nil, fmt.Errorf("web_stop %q: `cornus web --publish-in-conduit` still running %s after SIGINT", handle, timeout)
	case <-h.ctx.Done():
		return nil, h.ctx.Err()
	}
	delete(h.webPublished, handle)
	return anyDict(map[string]any{"output": bg.buf.String(), "code": bg.code}), nil
}

// stopWebs kills every background `cornus web` started this scenario, including
// any conduit-published one the scenario did not web_stop itself.
func (h *Harness) stopWebs() {
	for _, cmd := range h.webs {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	h.webs = nil
	for name, bg := range h.webPublished {
		if bg.cmd.Process != nil {
			_ = bg.cmd.Process.Kill()
		}
		<-bg.done
		delete(h.webPublished, name)
	}
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
	var name_, image, restart, hubIdentity, docker string
	var ports, env, command, entrypoint, mounts, volumes, dns, hubExport, hubImport, ingress, knative, telemetry starlark.Value
	var privileged, expectFail, agentForward bool
	replicas := 1
	var memLimit int64
	if err := starlark.UnpackArgs("deploy", args, kwargs,
		"name", &name_, "image", &image, "ports?", &ports, "env?", &env, "replicas?", &replicas,
		"restart?", &restart, "command?", &command, "entrypoint?", &entrypoint, "mounts?", &mounts,
		"privileged?", &privileged, "volumes?", &volumes, "dns?", &dns, "hub_identity?", &hubIdentity,
		"hub_export?", &hubExport, "hub_import?", &hubImport, "docker?", &docker, "ingress?", &ingress,
		"knative?", &knative, "expect_fail?", &expectFail, "agent_forward?", &agentForward,
		"telemetry?", &telemetry, "mem_limit?", &memLimit); err != nil {
		return nil, err
	}
	if h.registryHost == "" {
		return nil, fmt.Errorf("deploy: call serve() first")
	}
	spec := api.DeploySpec{Name: name_, Image: image, Replicas: replicas, Restart: restart}
	spec.Privileged = privileged
	// mem_limit=<bytes> is compose `mem_limit` / `deploy.resources.limits.memory`
	// (api.Resources.MemoryLimit). Bytes rather than a "512m" string: every backend
	// takes a byte count, and a scenario that asserts a RECORDED limit has to name
	// the same number the metric will carry.
	if memLimit > 0 {
		spec.Resources = &api.Resources{MemoryLimit: memLimit}
	}
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
	//
	// telemetry="" is NOT the same as omitting the kwarg: it is the ENDPOINT-LESS
	// block (`x-cornus-telemetry: {}`), the shape that makes the server default the
	// destination to its own OTLP receiver. That is only expressible because the
	// kwarg is unpacked as a Value — a plain string could not tell "absent" from
	// "present and empty", and reading it as absent silently turned
	// observability-telemetry-mux.star's headline deploy into an ordinary one.
	// Enabled is what carries "the workload asked for telemetry" when there is no
	// endpoint to imply it (api.TelemetrySpec.Active).
	if telemetry != nil && telemetry != starlark.None {
		endpoint, ok := starlark.AsString(telemetry)
		if !ok {
			return nil, fmt.Errorf("deploy: telemetry must be a string, got %s", telemetry.Type())
		}
		spec.Telemetry = &api.TelemetrySpec{Endpoint: endpoint, Enabled: true}
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
	h.recordDeployed(name_)
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
// while its local mount dirs are served over 9P. It blocks until THIS session
// announces that the server brought the workload up (sawAttachReady), then reads
// back and returns its status; the process is stopped by attach_stop or on
// scenario teardown.
func (h *Harness) bDeployAttach(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name_, image, restart, timeout, credentialsJSON, egressMode, runAsUser string
	var ports, env, clientEnv, localMounts, specMounts, command, entrypoint starlark.Value
	var privileged bool
	replicas := 1
	timeout = "180s"
	if err := starlark.UnpackArgs("deploy_attach", args, kwargs,
		"name", &name_, "image", &image, "local_mount?", &localMounts, "mounts?", &specMounts,
		"command?", &command,
		"entrypoint?", &entrypoint, "ports?", &ports, "env?", &env, "replicas?", &replicas,
		"restart?", &restart, "timeout?", &timeout, "privileged?", &privileged,
		"credentials_json?", &credentialsJSON, "egress?", &egressMode,
		"client_env?", &clientEnv, "user?", &runAsUser); err != nil {
		return nil, err
	}
	if h.registryHost == "" {
		return nil, fmt.Errorf("deploy_attach: call serve() first")
	}
	spec := api.DeploySpec{Name: name_, Image: image, Replicas: replicas, Restart: restart}
	spec.Privileged = privileged
	// runAsUser runs the workload as a NON-ROOT id, which is what makes a
	// credential file delivery a real test on a runtime that remaps ids: the
	// server must own the file as the host id THIS user maps to, not as the
	// range base, and the two differ for every id except the first.
	spec.User = runAsUser
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
	// client_env is the CLIENT process's environment, NOT the workload's (that is
	// `env` above, which becomes spec.Env inside the container). Credential SOURCE
	// backends run in this `cornus deploy` process for the life of the session, so
	// this is how a scenario points one at a stub binary — e.g. CORNUS_GH_BIN for
	// the github-cli source, which would otherwise need a real `gh auth login` on
	// the runner. Same role as compose_up_bg's `env`.
	clientEnvMap, err := strMap(clientEnv)
	if err != nil {
		return nil, fmt.Errorf("deploy_attach: client_env: %w", err)
	}
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
	// mounts=["src:dst[:ro]"] writes RAW api.Mount entries into the spec FILE the
	// session deploys, exactly as a hand-written `cornus deploy -f spec.json`
	// would. Distinct from local_mount, which becomes `--local-mount` flags the
	// CLI APPENDS after the file's mounts: only this kwarg can control the order
	// of the spec's mount list, and therefore express a NON-client-local source
	// (a named/bare-name volume) sitting BETWEEN two client-local binds. The
	// client serves every client-local source here over 9P just the same — the
	// resulting sparse "m<index>" naming is what deploy-mounts-sparse-index.star
	// exercises.
	specMountList, err := strSlice(specMounts)
	if err != nil {
		return nil, err
	}
	for _, m := range specMountList {
		mt, err := parseMount(m)
		if err != nil {
			return nil, err
		}
		spec.Mounts = append(spec.Mounts, mt)
	}

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
	// Force the deterministic plain renderer for THIS child: the readiness wait
	// below reads its output, and an ambient CORNUS_OUTPUT=json/fancy (or a color
	// setting) in the developer's environment would otherwise change the very line
	// it is looking for. Scenario logs are unaffected — a piped stdout already
	// resolves to plain.
	c.Env = append(os.Environ(), "CORNUS_OUTPUT=plain", "NO_COLOR=1")
	for k, v := range clientEnvMap {
		c.Env = append(c.Env, k+"="+v)
	}
	// Tee the child's output: it still streams into the scenario log, and the copy
	// is what the readiness wait reads. Stdout and Stderr are the SAME writer
	// value, so os/exec gives the child one pipe and never interleaves a line.
	var out lockedBuf
	sink := io.MultiWriter(h.out, &out)
	c.Stdout, c.Stderr = sink, sink
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("start deploy_attach %s: %w", name_, err)
	}
	at := &attach{cmd: c, done: make(chan struct{})}
	go func() { _ = c.Wait(); close(at.done) }()
	if h.attaches == nil {
		h.attaches = map[string]*attach{}
	}
	h.attaches[name_] = at
	// Deliberately NOT recorded for the reap. A deploy-attach workload already has
	// an owner: stopServer kills the attach process first thing, and dropping the
	// caller connection is what makes the SERVER remove the workload and unwind its
	// mounts. Reaping it a moment earlier would only race that.

	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, err
	}
	// Readiness is THIS session's own announcement, not "a workload with that name
	// is running" — see sawAttachReady. Polling Status(name) instead was satisfied
	// by any container carrying the deployment's label, including one a previous
	// FAILED run left behind on the same daemon: the wait returned instantly,
	// before this run's deploy had done anything, and the scenario then asserted
	// against a half-empty world.
	deadline := time.Now().Add(dur)
	for {
		if sawAttachReady(out.String(), name_) {
			return h.attachStatus(name_, replicas)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("deploy_attach %s: timed out after %s waiting for the session to report ready.\noutput:\n%s",
				name_, timeout, out.String())
		}
		select {
		case <-time.After(attachReadyPoll):
		case <-at.done:
			// Wait() returns only after the output copier has drained, so the buffer
			// is complete here: a session that announced ready and then exited (an
			// immediate teardown) still counts as ready.
			if sawAttachReady(out.String(), name_) {
				return h.attachStatus(name_, replicas)
			}
			return nil, fmt.Errorf("deploy_attach %s: `cornus deploy` exited before the workload became ready.\noutput:\n%s",
				name_, out.String())
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		}
	}
}

// attachReadyPoll is how often the deploy-attach readiness wait re-reads the
// child's captured output. Short: the wait is over local memory, not the server.
var attachReadyPoll = 200 * time.Millisecond

// sawAttachReady reports whether the captured output of ONE `cornus deploy`
// child shows that child's own deploy-attach session reaching ready.
//
// The server sends the Ready event (and its "deployed <name>" log line) only
// after IT applied this session's spec and awaited every desired instance, and
// the client prints its "deployed <name>: R/T instances running" result off the
// same event. So the marker cannot be produced by anything except the deploy the
// harness just started — which is exactly the run-scoping the old Status(name)
// poll lacked, since a leftover workload from a previous run answers to the same
// name and label.
//
// The trailing-byte check keeps a longer deployment name from satisfying a
// shorter one's wait ("deployed app2" must not mean "app" is ready).
func sawAttachReady(out, name string) bool {
	marker := "deployed " + name
	for i := 0; ; {
		j := strings.Index(out[i:], marker)
		if j < 0 {
			return false
		}
		rest := out[i+j+len(marker):]
		if rest == "" || !isDeployNameByte(rest[0]) {
			return true
		}
		i += j + len(marker)
	}
}

// isDeployNameByte reports whether b could be part of a deployment name, i.e.
// whether a marker match that continues into it is really a different name.
func isDeployNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-', b == '_', b == '.':
		return true
	}
	return false
}

// attachStatus reads back the status of a deploy-attach that has ALREADY
// announced ready, for the dict the builtin returns. It is a report, not the
// readiness gate (that is sawAttachReady), so it only retries a transient
// server-side hiccup rather than waiting on the workload.
func (h *Harness) attachStatus(name string, replicas int) (starlark.Value, error) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		st, err := h.client.Status(h.ctx, name)
		if err == nil {
			h.logf("✓ deploy_attach %s: session ready, %d/%d running (local mounts served over 9P)",
				name, countRunning(st), replicas)
			return statusDict(st), nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("deploy_attach %s: session reported ready but status read-back failed: %w", name, err)
		}
		select {
		case <-time.After(attachReadyPoll):
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
			h.recordComposedUp(file, project)
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
	var url, socks5Addr, caFile, host string
	var allowError, insecure, retry5xx bool
	var retryUntil int
	retry := "15s"
	if err := starlark.UnpackArgs("http_get", args, kwargs, "url", &url, "retry?", &retry, "socks5?", &socks5Addr, "allow_error?", &allowError, "insecure?", &insecure, "ca_file?", &caFile, "retry_5xx?", &retry5xx, "retry_until?", &retryUntil, "host?", &host); err != nil {
		return nil, err
	}
	dur, err := time.ParseDuration(retry)
	if err != nil {
		return nil, err
	}
	// host="app.example.com" sends that Host header and TLS SNI while still dialling
	// the address in the URL. An ingress routes on Host, so this is how a scenario
	// reaches a declared ingress hostname through a front door bound to 127.0.0.1
	// without touching DNS or /etc/hosts.
	// socks5="127.0.0.1:PORT" routes the GET through a client-side SOCKS5 proxy (a
	// conduit in socks5 mode), so a scenario can prove a workload is reachable by
	// name through the split-tunnel — the host is resolved by the proxy, not DNS.
	// insecure=True skips TLS verification, for reaching an https endpoint that
	// terminates TLS with a locally-generated cert (e.g. the emulated ingress), while
	// ca_file verifies it against a specific PEM certificate or CA bundle.
	// host= also needs its own transport for an https URL: TLS SNI must carry the
	// requested name, not the address being dialled, or a server selecting a
	// certificate by SNI answers with the wrong one.
	httpClient := http.DefaultClient
	if socks5Addr != "" || insecure || caFile != "" || host != "" {
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
		if host != "" {
			// Dial the URL's address, but present and verify the requested name.
			if tr.TLSClientConfig == nil {
				tr.TLSClientConfig = &tls.Config{} //nolint:gosec // ServerName set below; verification stays on
			}
			tr.TLSClientConfig.ServerName = host
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
			// Fill in the EXISTING config rather than replacing it: host= has
			// already set ServerName here, and dropping it would send the dialled
			// address as SNI. A server selecting its certificate by SNI would then
			// answer with the wrong one (or its default), and verification against
			// these roots would fail for a reason that has nothing to do with what
			// the scenario is asserting.
			if tr.TLSClientConfig == nil {
				tr.TLSClientConfig = &tls.Config{} //nolint:gosec // roots set below; verification stays on
			}
			tr.TLSClientConfig.RootCAs = roots
			tr.TLSClientConfig.MinVersion = tls.VersionTLS12
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
	//
	// retry_until=<status> is the strictly wider form, for the case where the
	// transient answer is not a 5xx at all: a real ingress controller that has not
	// yet ingested a freshly created Ingress routes the host to its DEFAULT backend
	// and answers 404, which is indistinguishable in shape from a permanent "no
	// such route" — so retry_5xx cannot absorb it and the request fails on the
	// controller's sync latency rather than on what the scenario asserts. Pass the
	// status the scenario is waiting FOR and every other status is treated as
	// transient until the deadline. It stays honest the same way: the last response
	// is returned as-is, so a route that never appears still fails the assertion,
	// with the real status in the message. Use it only for a POSITIVE assertion —
	// a scenario asserting a 404 must not retry_until=404, or "not yet" and "never"
	// become the same observation.
	deadline := time.Now().Add(dur)
	for {
		req, _ := http.NewRequestWithContext(h.ctx, http.MethodGet, url, nil)
		if host != "" {
			req.Host = host
		}
		resp, err := httpClient.Do(req)
		if err == nil {
			transient := (retry5xx && resp.StatusCode >= 500) || (retryUntil != 0 && resp.StatusCode != retryUntil)
			if transient && time.Now().Before(deadline) {
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

// bCornusStream runs a cornus CLI command that does not exit on its own — a
// `--follow`/`-f` stream — waits for `until` to appear in its output, then sends
// it SIGINT and returns everything it printed plus its exit code.
//
// The plain `cornus` builtin cannot express this: it waits for the process to
// exit, and a follow never does. Interrupting rather than killing is deliberate,
// because "Ctrl-C is a clean exit" is itself part of the contract under test — a
// watch that exited non-zero every time you stopped watching would be unusable.
//
// `trigger` is a second cornus command started AFTER the stream is up, so a
// scenario can prove the stream is live rather than merely replaying history:
// Starlark is single-threaded, so without it there is no way to make something
// happen while a blocking read is in progress. It is deliberately not waited for
// (a deploy-attach session never returns either) and is interrupted along with
// the stream.
//
//	out = cornus_stream("activity", "--follow",
//	                    trigger = ["deploy", "-f", spec, "--server", addr],
//	                    until = "deployment=web", timeout = "120s")
//	assert_contains(out["output"], "9p-mount")
//	assert_eq(out["code"], 0)
func (h *Harness) bCornusStream(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var cmdArgs []string
	for _, a := range args {
		s, ok := starlark.AsString(a)
		if !ok {
			return nil, fmt.Errorf("cornus_stream: arguments must be strings")
		}
		cmdArgs = append(cmdArgs, s)
	}
	until, timeout := "", "60s"
	var trigger []string
	extraEnv := append([]string{}, h.target.ServeEnv()...)
	for _, kv := range kwargs {
		name, _ := starlark.AsString(kv[0])
		switch name {
		case "until":
			until, _ = starlark.AsString(kv[1])
		case "timeout":
			timeout, _ = starlark.AsString(kv[1])
		case "trigger":
			var err error
			trigger, err = strSlice(kv[1])
			if err != nil {
				return nil, fmt.Errorf("cornus_stream: trigger: %w", err)
			}
		case "env":
			envMap, err := strMap(kv[1])
			if err != nil {
				return nil, fmt.Errorf("cornus_stream: env: %w", err)
			}
			for k, v := range envMap {
				extraEnv = append(extraEnv, k+"="+v)
			}
		default:
			return nil, fmt.Errorf("cornus_stream: unexpected keyword argument %q", name)
		}
	}
	if until == "" {
		return nil, fmt.Errorf("cornus_stream: until is required (the text to wait for before interrupting)")
	}
	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, fmt.Errorf("cornus_stream: timeout: %w", err)
	}

	cmd := exec.Command(h.cornusBin, cmdArgs...)
	cmd.Env = append(os.Environ(), extraEnv...)
	var buf lockedBuf
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cornus_stream %s: %w", strings.Join(cmdArgs, " "), err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	// Start the trigger only once the stream is actually reading. Otherwise a fast
	// trigger could complete before the follow connects, and the record would
	// arrive as backlog — which proves nothing about live delivery.
	var trigBuf lockedBuf
	if len(trigger) > 0 {
		if err := h.waitStreamReady(cmd, &buf, waited, trigger); err != nil {
			return nil, err
		}
		trig := exec.Command(h.cornusBin, trigger...)
		trig.Env = append(os.Environ(), extraEnv...)
		trig.Stdout, trig.Stderr = &trigBuf, &trigBuf
		if err := trig.Start(); err != nil {
			_ = cmd.Process.Kill()
			<-waited
			return nil, fmt.Errorf("cornus_stream: trigger %s: %w", strings.Join(trigger, " "), err)
		}
		defer func() {
			_ = trig.Process.Signal(os.Interrupt)
			done := make(chan struct{})
			go func() { defer close(done); _ = trig.Wait() }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				_ = trig.Process.Kill()
				<-done
			}
		}()
	}

	deadline := time.Now().Add(dur)
	found := false
	for time.Now().Before(deadline) && !found {
		if strings.Contains(buf.String(), until) {
			found = true
			break
		}
		select {
		case err := <-waited:
			// It exited on its own, which a stream should not do. Report what it
			// printed: that is where the reason will be.
			return nil, fmt.Errorf("cornus_stream %s: exited on its own before %q appeared (%v).\nstream output:\n%s\ntrigger output:\n%s",
				strings.Join(cmdArgs, " "), until, err, buf.String(), trigBuf.String())
		case <-time.After(100 * time.Millisecond):
		}
	}

	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case err = <-waited:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-waited
		return nil, fmt.Errorf("cornus_stream %s: did not exit within 15s of SIGINT: %s",
			strings.Join(cmdArgs, " "), buf.String())
	}
	if !found {
		return nil, fmt.Errorf("cornus_stream %s: %q never appeared within %s.\nstream output:\n%s\ntrigger output:\n%s",
			strings.Join(cmdArgs, " "), until, timeout, buf.String(), trigBuf.String())
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return nil, fmt.Errorf("cornus_stream %s: %w: %s", strings.Join(cmdArgs, " "), err, buf.String())
	}
	return anyDict(map[string]any{"code": code, "output": strings.TrimSpace(buf.String())}), nil
}

// waitStreamReady blocks until the stream has printed its first byte, which is
// how the harness knows it is connected and reading rather than still starting
// up. A follow prints its backlog first, so for these commands "some output" is
// a reliable readiness signal — and firing the trigger before it would let the
// triggered record arrive as backlog, proving nothing about live delivery.
func (h *Harness) waitStreamReady(cmd *exec.Cmd, buf *lockedBuf, waited chan error, trigger []string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if buf.String() != "" {
			return nil
		}
		select {
		case err := <-waited:
			// A stream that returns on its own is the failure, whether or not it
			// printed anything first — a follow that stops after its backlog has
			// stopped following.
			return fmt.Errorf("cornus_stream: exited on its own instead of streaming (%v); it is not following.\noutput:\n%s",
				err, buf.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	<-waited
	return fmt.Errorf("cornus_stream: printed nothing within 30s, so the trigger %s could not be timed against a live stream",
		strings.Join(trigger, " "))
}

// lockedBuf collects a child's output while the harness reads it concurrently.
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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
	d, err := os.MkdirTemp("", tempDirPrefix)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(d, 0o755); err != nil {
		return nil, fmt.Errorf("temp_dir: %w", err)
	}
	h.tempDirsMu.Lock()
	h.tempDirs = append(h.tempDirs, d)
	h.tempDirsMu.Unlock()
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

// recordDeployed and recordComposedUp note what THIS scenario created, so
// teardown can remove exactly that.
func (h *Harness) recordDeployed(name string) {
	if h.deployed == nil {
		h.deployed = map[string]bool{}
	}
	h.deployed[name] = true
}

func (h *Harness) recordComposedUp(file, project string) {
	if h.composedUp == nil {
		h.composedUp = map[[2]string]bool{}
	}
	h.composedUp[[2]string{file, project}] = true
}

// reapScenarioWorkloads removes what the scenario left running.
//
// Killing the server does NOT remove them: on the host backends the containers
// belong to the daemon, not to the server process, and a detached `compose up -d`
// leaves them running by design. Starlark has no defer, so a scenario that fails
// partway never reaches its own compose_down/remove, and the leftovers outlive it
// still holding whatever they published. One stale assertion in
// compose-conduit-mismatch.star left cornus-e2e-web-0 bound to host port 8080 and
// turned ONE red scenario into NINE in CI, eight of them unrelated.
//
// ⚠️ It reaps ONLY what this harness itself created, tracked at the builtin call
// sites. The obvious implementation — list the server's deployments and delete
// them all — looks equivalent and is not: on the docker backend `List` enumerates
// by the `cornus.managed=true` label across the WHOLE daemon, so on a developer
// machine it deletes the user's own running deployments. That is not
// hypothetical; it happened, and it cost seven of them. Do not replace this with
// a List-and-delete sweep.
//
// Compose projects are torn down with `compose down` rather than by deleting
// names, because the project's own teardown is what knows which workloads,
// networks and sessions belong to it — and it is scoped to the project by
// construction.
func (h *Harness) reapScenarioWorkloads() {
	if h.registryHost == "" {
		return
	}
	for fp := range h.composedUp {
		file, project := fp[0], fp[1]
		// `compose down` is idempotent and exits 0 whether it removed anything or
		// not, so running it says nothing about whether there WAS a leak. Ask first,
		// and stay quiet when the scenario already cleaned up after itself — which
		// is the common case. Without this the teardown logged "reaped leftover
		// compose project" for nearly every scenario in the suite (19 lines in CI run
		// 30359492738), which reads as 19 leaks and is worse than no logging at all.
		if !h.composeProjectHasWorkloads(file, project) {
			continue
		}
		cargs := []string{"compose", "-f", file}
		if project != "" {
			cargs = append(cargs, "-p", project)
		}
		cargs = append(cargs, "down")
		ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), composeCallTimeout)
		out, err := h.captureCtx(ctx, h.cornusBin, []string{"CORNUS_HOST=http://" + h.registryHost}, cargs...)
		cancel()
		if err != nil {
			// Best-effort: this runs on the failure path too, where the scenario's
			// real error is what matters. Log and continue.
			h.logf("… reap: compose down %s (project %q): %v: %s", file, project, err, strings.TrimSpace(out))
			continue
		}
		h.logf("… reaped leftover compose project %q (%s)", project, file)
	}
	if h.client == nil {
		return
	}
	names := make([]string, 0, len(h.deployed))
	for n := range h.deployed {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic teardown output
	ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), 60*time.Second)
	defer cancel()
	for _, n := range names {
		// Status first: a scenario that cleaned up after itself is the normal
		// case, and Delete on an absent name would log a spurious failure on
		// every well-behaved run.
		if st, err := h.client.Status(ctx, n); err != nil || len(st.Instances) == 0 {
			continue
		}
		if err := h.client.Delete(ctx, n); err != nil {
			h.logf("… reap %s: %v", n, err)
			continue
		}
		h.logf("… reaped leftover deployment %s", n)
	}
}

// composeProjectHasWorkloads reports whether a compose project still has
// anything deployed, using the product's own `compose ps`. A failure to ask is
// treated as "yes": the reap is best-effort, and running an idempotent `down` we
// did not need is harmless, where skipping one we did need is the leak this
// exists to prevent.
func (h *Harness) composeProjectHasWorkloads(file, project string) bool {
	cargs := []string{"compose", "-f", file}
	if project != "" {
		cargs = append(cargs, "-p", project)
	}
	// --quiet, not the table: plain `compose ps` prints a row for every service the
	// FILE declares, with status "not created" for the ones that are not deployed,
	// so counting rows measures desired state and always answers "yes". --quiet
	// prints the resource id of the CREATED services only (psQuiet), which is
	// exactly the question, and needs no parsing of human-readable output.
	cargs = append(cargs, "ps", "--quiet")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), composeCallTimeout)
	defer cancel()
	out, err := h.captureCtx(ctx, h.cornusBin, []string{"CORNUS_HOST=http://" + h.registryHost}, cargs...)
	if err != nil {
		return true
	}
	return strings.TrimSpace(out) != ""
}

func (h *Harness) stopServer() {
	// MCP stdio clients are child processes whose tool calls may still need the
	// server. Close their stdin and let them exit cleanly before taking the
	// upstream server away.
	h.stopMCPSessions()

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
	if h.serverContainer != "" {
		h.removeServerContainer()
	}
	if h.serverInstance != "" {
		h.removeServerInstance()
	}
	if h.server != nil && h.server.Process != nil {
		// Same hazard as the containerized server (see removeServerContainer): a
		// host-run server holds the kernel 9P mounts it made, and SIGKILL leaves
		// them stranded in the host's mount table, where clearing them needs
		// root. Costs nothing when no mount is live — the wait returns at once.
		h.waitMountsUnwound(20 * time.Second)
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

// syncBuffer is a concurrency-safe byte sink: the server's stdout and stderr are
// separate goroutines writing into it while a scenario reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// bServerLog returns everything the running server has written to stdout/stderr
// so far.
//
// For contracts whose ONLY observable is the server's own log. The motivating
// case is a TCP port-forward setup failure: the tunnel is a raw passthrough with
// no error channel back to the client, so the backend's diagnostic — including
// the podman rootless refusal that names CORNUS_PODMAN_REMOTE — is logged
// server-side and never reaches the CLI. A scenario that watched only the client
// would see an empty stream and could not tell a refusal from a hang.
//
// Prefer a client-visible assertion when one exists; this is for when none does.
func (h *Harness) bServerLog(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("server_log", args, kwargs); err != nil {
		return nil, err
	}
	if h.serverLog == nil {
		return nil, fmt.Errorf("server_log: call serve() first")
	}
	return starlark.String(h.serverLog.String()), nil
}
