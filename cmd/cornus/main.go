// Command cornus brings the Docker workflow (docker compose, the docker CLI,
// devcontainers) to a Kubernetes cluster or a plain Docker host, from a single
// binary that bundles the registry, build engine, and deploy engine it runs on:
// a tiny OCI registry, an in-process BuildKit-based build engine, and a small
// imperative deploy engine.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/alecthomas/kong"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/cmd/cornus/internal/composecli"
	"cornus/pkg/config"
	"cornus/pkg/logging"
	"cornus/pkg/observability"

	// Register the tunnel backends so `cornus serve` can host tunnels. pkg/server
	// depends only on the tunnel interface; the concrete backends are wired in
	// here, at the binary, so they can be swapped without touching the server.
	// All four are light: ngrok SDK; ssh reuses x/crypto/ssh; cloudflare and
	// tailscale shell out to their CLIs. tailscale is a subprocess on purpose —
	// importing tailscale.com would drag the shared module graph up (it forces a
	// k8s bump), which cornus pins; the `tailscale funnel` CLI sidesteps that.
	_ "cornus/pkg/tunnel/cloudflare"
	_ "cornus/pkg/tunnel/ngrok"
	_ "cornus/pkg/tunnel/ssh"
	_ "cornus/pkg/tunnel/tailscale"

	// Register the client-side credential SOURCE backends (mint a credential on
	// the caller's machine) and the container-facing DELIVERY providers (serve it
	// to the workload). static/exec and the neutral generic + aws-imds delivery
	// adapters are dependency-light and always compiled; the real AWS STS source
	// is gated behind the `credaws` build tag (a stub registers a clear error
	// otherwise), mirroring the storage cloudblob gate.
	_ "cornus/pkg/creddelivery/awsimds"
	_ "cornus/pkg/creddelivery/generic"
	_ "cornus/pkg/credential/awssts"
	_ "cornus/pkg/credential/exec"
	_ "cornus/pkg/credential/static"

	// Local-AI-credential support. SOURCES read the developer's own local LLM
	// credential from wherever their tool keeps it: `env` (ANTHROPIC_API_KEY /
	// OPENAI_API_KEY), `anthropic` (the `ant auth login` store), and the per-tool
	// stores that do NOT share those — `claude-code` (~/.claude/.credentials.json)
	// and `codex` (~/.codex/auth.json). The anthropic-proxy / openai-proxy DELIVERY
	// providers inject that credential into the workload's LLM API calls (base-URL
	// override) so the raw secret never enters the container.
	_ "cornus/pkg/creddelivery/anthropicproxy"
	_ "cornus/pkg/creddelivery/openaiproxy"
	_ "cornus/pkg/credential/anthropic"
	_ "cornus/pkg/credential/claudecode"
	_ "cornus/pkg/credential/codex"
	_ "cornus/pkg/credential/env"
)

// CLI is the kong-parsed command-line surface.
type CLI struct {
	DataDir string `kong:"name='data-dir',help='Persistent data directory (registry CAS + build cache).',env='CORNUS_DATA'"`
	Context string `kong:"name='context',env='CORNUS_CONTEXT',help='Connection profile to use from the cornus client config (see cornus config). Overrides the config current-context.'"`
	Config  string `kong:"name='config-file',env='CORNUS_CONFIG',help='Path to the cornus client config file (default: the platform user config dir, honoring $XDG_CONFIG_HOME).'"`

	ContextFile      string `kong:"name='context-file',env='CORNUS_CONTEXT_FILE',help='Per-project context override file (a bare Context in JSON/YAML/TOML) whose fields overlay the selected context. Default: discover cornus-context.{json,yaml,toml} walking up from the working directory (bounded at the repo/home root).'"`
	NoContextFile    bool   `kong:"name='no-context-file',help='Disable per-project context override file discovery.'"`
	TrustContextFile bool   `kong:"name='trust-context-file',env='CORNUS_TRUST_CONTEXT_FILE',help='Honor the credential/endpoint/TLS fields of an auto-discovered context file and skip provenance checks. Off by default, so a merely-discovered file cannot silently redirect the connection.'"`

	Output  string `kong:"name='output',env='CORNUS_OUTPUT',default='auto',enum='auto,plain,fancy,json',help='Output rendering: auto (fancy on a terminal, plain otherwise), plain (deterministic, no color), fancy (color + layout), or json (machine-readable NDJSON).'"`
	NoColor bool   `kong:"name='no-color',help='Disable color in fancy output (layout is kept). Also honored via NO_COLOR / CLICOLOR=0.'"`

	// drv is the process output driver. main() sets it from the parsed flags
	// before dispatch; out() builds a plain default lazily so a command's Run
	// method can be exercised directly in a test without wiring.
	drv *cliout.Driver

	// baseCtx is the root-span context for the invocation, set by main() before
	// dispatch so every server call a command makes shares one trace (client ->
	// server -> caretaker). rootContext falls back to context.Background() when it
	// is unset — the case when a command's Run is exercised directly in a test.
	baseCtx context.Context

	Serve       ServeCmd       `kong:"cmd,help='Run the cornus server (registry + build + deploy).'"`
	Config_     ConfigCmd      `kong:"cmd,name='config',help='Manage connection profiles (contexts) for reaching a remote cornus server.'"`
	Setup       SetupCmd       `kong:"cmd,help='Interactive wizard: create and verify a connection profile (context) and print setup guidance.'"`
	Build       BuildCmd       `kong:"cmd,help='Build an image from a context and push it.'"`
	Push        PushCmd        `kong:"cmd,help='Push a local image into the registry.'"`
	Deploy      DeployCmd      `kong:"cmd,help='Apply a deployment spec.'"`
	Exec        ExecCmd        `kong:"cmd,help='Run a command inside a deployment (docker exec) via a cornus server.'"`
	PortForward PortForwardCmd `kong:"cmd,name='port-forward',help='Forward local TCP ports to a deployment container port (kubectl port-forward). Cluster profiles tunnel straight to the pod with the developer kubeconfig, falling back to the server proxy.'"`
	Socks5      Socks5Cmd      `kong:"cmd,name='socks5',help='Run a local SOCKS5 split-tunnel proxy: CONNECT targets under a service-host suffix (default .cornus.internal) tunnel to the matching workload, everything else egresses directly.'"`
	Tunnel      TunnelCmd      `kong:"cmd,help='Expose a deployment port to the public internet through a hosted tunnel (ngrok). Inject an authtoken; the server hosts the tunnel and bridges it to the workload.'"`
	Compose     composecli.Cmd `kong:"cmd,help='Docker Compose-compatible client: build and deploy Compose / devcontainer projects via a cornus server.'"`
	Web         WebCmd         `kong:"cmd,help='Serve the local web UI (workloads, compose projects, dependency graph, mounts, tunnels, logs, exec terminal) and a co-hosted MCP server; --mcp-stdio serves MCP alone over stdin/stdout for agent clients.'"`
	Daemon      DaemonCmd      `kong:"cmd,help='Long-running helper daemons (Docker API proxy, compose mount supervisor, pod sidecars).'"`
	Hub         HubCmd         `kong:"cmd,help='Join the workload-to-workload overlay as a spoke (register/reach services through a cornus hub).'"`
	Token       TokenCmd       `kong:"cmd,help='Mint JWTs for a server with bearer auth (the server is verify-only; this is the issuer).'"`

	// Hidden aliases for the pod-facing sidecar daemons, which now live under
	// `cornus daemon`. The top-level spellings are baked into generated pod
	// specs (pkg/deploy/kubernetes) and must keep working indefinitely — both
	// for pods already running from old specs and for new specs, which are
	// still generated with these spellings.
	Caretaker      CaretakerCmd      `kong:"cmd,hidden,help='Pod sidecar: run the configured roles (9P mounts, ...) until teardown (alias of daemon caretaker).'"`
	CaretakerCheck CaretakerCheckCmd `kong:"cmd,name='caretaker-check',hidden,help='Exit 0 if every caretaker role is live (sidecar readiness probe; alias of daemon caretaker-check).'"`
	NetRedirect    NetRedirectCmd    `kong:"cmd,name='net-redirect',hidden,help='Init container: iptables-redirect app egress into the caretaker proxy (alias of daemon net-redirect).'"`
	// Hidden top-level alias of `daemon containerd-log-shim`. containerd invokes
	// the binary log URI as `cornus containerd-log-shim <path>` (its NewBinaryCmd
	// turns the single query param into a `key value` argv pair — it cannot
	// address the nested `daemon` subcommand), so this spelling must exist at the
	// top level. The bare backend's `bare-shim` has no such alias: cornus spawns
	// it itself as `cornus daemon bare-shim ...`, so it lives only under `daemon`.
	LogShim    LogShimCmd    `kong:"cmd,name='containerd-log-shim',hidden,help='Alias of daemon containerd-log-shim (targeted by the containerd binary log URI).'"`
	MountAgent MountAgentCmd `kong:"cmd,name='mount-agent',help='DEPRECATED single-mount sidecar (alias over caretaker).'"`
	Mountcheck MountcheckCmd `kong:"cmd,help='Exit 0 if the target path is a live mountpoint (deprecated; use caretaker-check).'"`
	Health     HealthCmd     `kong:"cmd,help='Probe a running server health endpoint.'"`
	Version    VersionCmd    `kong:"cmd,help='Print the cornus version.'"`
}

// out returns the process output driver. main() sets it from the parsed global
// flags; when unset (a command Run called directly in a test) it lazily builds a
// default driver so the call still works.
func (c *CLI) out() *cliout.Driver {
	if c.drv == nil {
		c.drv = cliout.New(cliout.Options{Output: c.Output, NoColor: c.NoColor})
	}
	return c.drv
}

// rootContext returns the invocation's root-span context, or context.Background()
// when unset (a command Run called directly in a test). Server-facing commands
// use it as the base for their signal-cancellable context so their client calls
// hang off the one per-invocation trace.
func (c *CLI) rootContext() context.Context {
	if c.baseCtx != nil {
		return c.baseCtx
	}
	return context.Background()
}

// resolveConfig builds the runtime Config from global flags, applying defaults.
func (c *CLI) resolveConfig() config.Config {
	dataDir := c.DataDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	return config.Config{DataDir: dataDir}
}

func main() {
	logging.Init()
	// Propagate the binary's build version into the compose subpackage so
	// `cornus compose version` reports the real stamped version.
	composecli.Version = version
	var cli CLI
	kctx := kong.Parse(&cli,
		kong.Name("cornus"),
		kong.Description("All-in-one container registry, build engine, and deploy engine."),
		kong.UsageOnError(),
	)
	// Build the output driver from the global flags and bind it alongside the
	// connection resolver, so every command's Run method — including the `cornus
	// compose` subpackage, which cannot import package main — can receive a
	// *cliout.Driver and a *clientconn.Resolver by type.
	cli.drv = cliout.New(cliout.Options{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   os.Stdin,
		Output:  cli.Output,
		NoColor: cli.NoColor,
	})

	// Opt-in OpenTelemetry for the client process, so a CLI invocation is the
	// root of a trace that continues into the server and the caretaker over the
	// W3C headers pkg/client injects. Zero-cost when telemetry is disabled (see
	// pkg/observability): Setup is a no-op, the global propagator stays no-op, and
	// no spans are exported. Telemetry must never break the CLI, so a Setup error
	// is logged and the command runs uninstrumented.
	providers, err := observability.Setup(context.Background(), observability.Options{
		ServiceName:    "cornus-cli",
		ServiceVersion: version,
	})
	if err != nil {
		slog.Warn("telemetry setup failed; continuing without tracing", "err", err)
		providers = nil
	}
	// Carry the invoking command as W3C baggage so the server and caretaker spans
	// this invocation reaches can be attributed to (and filtered by) the command
	// that initiated them — baggage propagates cross-service alongside the trace
	// context. Best-effort: a rejected member never breaks the CLI, and with
	// telemetry off the baggage propagator is a no-op so nothing is emitted.
	// NewMemberRaw (not NewMember) takes the value verbatim — command paths like
	// "compose up" contain spaces, which are percent-encoded on inject.
	traceCtx := context.Background()
	if m, err := baggage.NewMemberRaw("cornus.command", kctx.Command()); err == nil {
		if b, err := baggage.New(m); err == nil {
			traceCtx = baggage.ContextWithBaggage(traceCtx, b)
		}
	}
	// One root span per invocation, named for the resolved command path, so every
	// server call the command makes hangs off a single trace.
	ctx, span := observability.Tracer().Start(traceCtx, "cornus "+kctx.Command())
	cli.baseCtx = ctx
	// The `cornus compose` subcommands run in a sibling package that cannot import
	// package main, so they cannot see cli.baseCtx; hand them the same root context
	// (mirroring composecli.Version) so their foreground client calls join this
	// invocation's trace instead of starting separate roots.
	composecli.SetBaseContext(ctx)

	runErr := kctx.Run(&cli, cli.resolver(), cli.drv)
	if runErr != nil {
		span.RecordError(runErr)
		span.SetStatus(codes.Error, runErr.Error())
	}
	span.End()
	// Flush exported spans before the process exits: FatalIfErrorf may os.Exit,
	// which would skip a deferred shutdown.
	if providers != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = providers.Shutdown(shutdownCtx)
		cancel()
	}
	kctx.FatalIfErrorf(runErr)
}
