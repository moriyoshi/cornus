package main

// The `cornus web` command: a local web UI for cornus. The UI and its
// backend-for-frontend live in cmd/cornus/internal/webbff; this file is the
// command shell that resolves the connection, builds the BFF, and serves it on a
// loopback listener.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cornus/cmd/cornus/internal/clientagent"
	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
	"cornus/cmd/cornus/internal/webbff"
	"cornus/pkg/clientconduit"
	"cornus/pkg/socks5"
	"cornus/pkg/webui"
)

// defaultWebAddr is the loopback listener default; also used to tell an explicit
// --addr from the default when --publish-in-conduit binds no listener.
const defaultWebAddr = "127.0.0.1:0"

// WebCmd serves the embedded web UI and its backend-for-frontend API.
type WebCmd struct {
	Addr        string   `kong:"name='addr',default='127.0.0.1:0',help='Listen address for the web UI. Must be a loopback address (the UI has no auth).'"`
	Host        string   `kong:"name='host',short='H',env='CORNUS_HOST',help='cornus server endpoint. Falls back to the selected connection profile, then http://localhost:5000.'"`
	Files       []string `kong:"name='file',short='f',sep='none',help='Compose file(s). Repeatable. Defaults to compose.yaml / docker-compose.yml in the working directory; without one the project views stay empty.'"`
	EnvFile     []string `kong:"name='env-file',sep='none',help='Env file(s) for variable interpolation, replacing the default .env discovery. Repeatable.'"`
	ProjectName string   `kong:"name='project-name',short='p',help='Project name (default: the Compose file directory name).',env='COMPOSE_PROJECT_NAME'"`
	Open        bool     `kong:"name='open',help='Open the UI in the default browser once listening.'"`
	Frontend    string   `kong:"name='frontend',env='CORNUS_WEB_FRONTEND',help='Detached frontend dev-server URL (e.g. http://localhost:5173). When set, non-BFF requests are reverse-proxied there instead of served from the embedded assets, so the Vite dev server can run separately with hot-reload.'"`
	MCP         bool     `kong:"name='mcp',negatable,default='true',help='Co-host an MCP (Model Context Protocol) server at /.cornus/mcp so agent clients (Zed, Claude Desktop) can drive workloads, files, logs, and exec. Inherits the same loopback/no-auth threat model as the UI; with --publish-in-conduit it is exposed to the conduit alongside the UI. Use --no-mcp to disable.'"`
	MCPStdio    bool     `kong:"name='mcp-stdio',help='Serve only the MCP server, over stdin/stdout, instead of binding an HTTP listener — for agent clients that launch a command rather than dial a URL (Zed context servers, Claude Desktop). Binds no port; diagnostics go to stderr. Mutually exclusive with --publish-in-conduit.'"`

	Conduit     string `kong:"name='conduit',help='SOCKS5 conduit selector for --publish-in-conduit (bare \\'socks5\\', or socks5://host:port[?suffix=SUFFIX]). Defaults to the profile SOCKS5 settings. The settings must match those your workload sessions use, or the two proxies collide on one bind address.'"`
	Publish     bool   `kong:"name='publish-in-conduit',help='Instead of binding a local port, host the UI inside the background agent and publish it in the shared SOCKS5 conduit, so one browser proxy setting reaches both your workloads and this UI. Requires a socks5 conduit.'"`
	PublishName string `kong:"name='publish-name',help='Conduit host name to publish the UI under (default: the service-host suffix apex, e.g. cornus.internal). Implies --publish-in-conduit.'"`
	PublishPort int    `kong:"name='publish-port',default='80',help='Conduit port the published name answers on.'"`
}

// Run resolves the server connection and (optional) compose project, then either
// serves the SPA and BFF on a loopback listener or — with --publish-in-conduit —
// hands the BFF to the background agent to publish in the shared conduit.
func (c *WebCmd) Run(cli *CLI, r *clientconn.Resolver, d *cliout.Driver) error {
	if c.MCPStdio {
		return c.runStdio(cli, r)
	}
	if c.Publish || c.PublishName != "" {
		return c.runPublished(cli, r, d)
	}

	if err := requireLoopback(c.Addr); err != nil {
		return err
	}

	cn, err := r.Resolve(c.Host)
	if err != nil {
		return err
	}
	defer cn.Cleanup()
	if cn.Endpoint == "" {
		cn.Endpoint = "http://localhost:5000"
	}

	bff, err := webbff.New(c.bffConfig(cli), cn.Client(), cn.Endpoint, r, socketAgentView{})
	if err != nil {
		return err
	}
	defer bff.Close()
	handler, err := bff.Handler()
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", c.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", c.Addr, err)
	}
	listenURL := "http://" + ln.Addr().String()

	if c.Frontend != "" {
		d.Info("frontend (detached): proxying / -> %s", c.Frontend)
	} else if !webui.Built() {
		d.Warn("web UI assets are not embedded in this binary; the BFF API works but / serves a build hint (run `make web` and rebuild)")
	}
	d.Info("cornus web UI: %s", listenURL)
	if c.MCP {
		d.Info("cornus MCP endpoint: %s/.cornus/mcp", listenURL)
	}
	if name, files := bff.Project(); name != "" {
		d.Info("compose project: %s (%s)", name, strings.Join(files, ", "))
	}
	if c.Open {
		// Best-effort: a headless host has no opener; the URL is printed anyway.
		_ = exec.Command("xdg-open", listenURL).Start()
	}

	ctx, stop := signal.NotifyContext(cli.rootContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Handler: handler}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// bffConfig projects the command's flags (and the global CLI flags the apply
// endpoint replays) onto the BFF's config.
func (c *WebCmd) bffConfig(cli *CLI) webbff.Config {
	return webbff.Config{
		Files:       c.Files,
		EnvFiles:    c.EnvFile,
		ProjectName: c.ProjectName,
		Frontend:    c.Frontend,
		ConfigPath:  cli.Config,
		Context:     cli.Context,
		Host:        c.Host,
		Version:     version,
		MCP:         c.MCP,
	}
}

// runStdio serves the co-hosted MCP server over stdin/stdout instead of binding an
// HTTP listener, for MCP clients that launch a command rather than dial a URL (Zed
// context servers, Claude Desktop). It builds the same webbff.Server `cornus web`
// serves and runs the same mcp.Server — only the transport differs. Diagnostics go
// to stderr so they never corrupt the JSON-RPC stream on stdout, and it runs until
// the client disconnects or the process is interrupted.
func (c *WebCmd) runStdio(cli *CLI, r *clientconn.Resolver) error {
	if c.Publish || c.PublishName != "" {
		return fmt.Errorf("--mcp-stdio and --publish-in-conduit are mutually exclusive: --mcp-stdio binds no listener and speaks MCP over stdin/stdout")
	}
	cn, err := r.Resolve(c.Host)
	if err != nil {
		return err
	}
	defer cn.Cleanup()
	if cn.Endpoint == "" {
		cn.Endpoint = "http://localhost:5000"
	}

	cfg := c.bffConfig(cli)
	cfg.MCP = true // --mcp-stdio serves MCP; --no-mcp does not apply
	bff, err := webbff.New(cfg, cn.Client(), cn.Endpoint, r, socketAgentView{})
	if err != nil {
		return err
	}
	defer bff.Close()

	ctx, stop := signal.NotifyContext(cli.rootContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bff.MCPRun(ctx); err != nil && ctx.Err() == nil {
		return fmt.Errorf("mcp stdio server: %w", err)
	}
	return nil
}

// runPublished hosts the BFF inside the background agent and publishes it in the
// shared SOCKS5 conduit, so one browser proxy setting reaches both the workloads
// and this UI. It binds no local port; the UI is reachable only through the proxy.
// It stays foreground, re-publishing if the agent restarts, until interrupted.
func (c *WebCmd) runPublished(cli *CLI, r *clientconn.Resolver, d *cliout.Driver) error {
	cn, err := r.Resolve(c.Host)
	if err != nil {
		return err
	}
	defer cn.Cleanup()

	req, name, err := c.publishRequest(r, cn)
	if err != nil {
		return err
	}

	display := name
	if c.PublishPort != 80 {
		display = fmt.Sprintf("%s:%d", name, c.PublishPort)
	}

	ctx, stop := signal.NotifyContext(cli.rootContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	first := true
	for {
		socket, err := clientagent.EnsureRunning()
		if err != nil {
			return fmt.Errorf("start background agent: %w", err)
		}
		if info := clientagent.Ping(socket); info != nil && info.Protocol < clientagent.ProtocolVersion {
			return fmt.Errorf("background agent was started by an older cornus build and cannot publish the web UI; run `cornus daemon stop` then retry")
		}
		resp, conn, err := clientagent.SendHold(socket, req)
		if err != nil {
			return fmt.Errorf("publish web UI: %w", err)
		}

		for _, b := range resp.Banners {
			d.Info("%s", b)
		}
		d.Info("cornus web UI (in conduit): http://%s/", display)
		if c.MCP {
			d.Info("cornus MCP endpoint (in conduit): http://%s/.cornus/mcp", display)
		}
		if first {
			d.Done("publishing the UI through the shared SOCKS5 conduit; point your browser's proxy at it and open http://%s/ (Ctrl-C to stop)", display)
			first = false
		}

		// Hold the registration open. The agent parks on this connection and reaps
		// the UI the moment it closes, so the kernel is the liveness authority: a
		// crash of this process withdraws the name deterministically.
		gone := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, conn); close(gone) }()
		select {
		case <-ctx.Done():
			_ = conn.Close()
			d.Done("stopped publishing %s", display)
			return nil
		case <-gone:
			_ = conn.Close()
			d.Warn("background agent went away; re-publishing %s", display)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

// publishRequest builds the web-serve request and the name the UI is published
// under, resolving the conduit config and compose files client-side. It is the
// pure part of runPublished — the whole "what to publish" decision — separated so
// the serve loop stays thin and this can be tested without an agent.
func (c *WebCmd) publishRequest(r *clientconn.Resolver, cn *clientconn.Conn) (clientagent.Request, string, error) {
	if c.Addr != "" && c.Addr != defaultWebAddr {
		return clientagent.Request{}, "", fmt.Errorf("--addr and --publish-in-conduit are mutually exclusive: publishing in the conduit binds no local port")
	}
	if c.PublishPort < 1 || c.PublishPort > 65535 {
		return clientagent.Request{}, "", fmt.Errorf("--publish-port %d out of range (1-65535)", c.PublishPort)
	}

	// Force socks5: --publish-in-conduit needs a name-resolving proxy. Passing
	// ModeSocks5 as the override forces the mode while inheriting the profile's
	// listen/suffix, exactly like `cornus socks5`. An explicit --conduit that is
	// not socks5 is a contradiction, caught here before the agent is contacted.
	override := c.Conduit
	if override == "" {
		override = clientconduit.ModeSocks5
	}
	cfg := cn.ConduitConfig(override)
	if cfg.Mode != clientconduit.ModeSocks5 {
		return clientagent.Request{}, "", fmt.Errorf("--publish-in-conduit requires a socks5 conduit, but --conduit resolves to %q; drop it or pass --conduit socks5", cfg.Mode)
	}
	// The published UI must join the SHARED proxy, never a private session-local one
	// (which CORNUS_CONDUIT could otherwise select).
	cfg.Socks5SessionLocal = false

	name := c.PublishName
	if name == "" {
		suffix := cfg.Socks5Suffix
		if suffix == "" {
			suffix = socks5.DefaultSuffix
		}
		name = strings.TrimPrefix(suffix, ".")
	}

	// Resolve compose files in the USER's cwd (the agent's is frozen elsewhere).
	files := c.Files
	if len(files) == 0 {
		files = webbff.DiscoverComposeFiles()
	}
	files, err := absPaths(files)
	if err != nil {
		return clientagent.Request{}, "", err
	}
	envFiles, err := absPaths(c.EnvFile)
	if err != nil {
		return clientagent.Request{}, "", err
	}
	absCfg, err := r.AbsConfigPath()
	if err != nil {
		return clientagent.Request{}, "", err
	}
	req := clientagent.Request{
		Action: "web-serve",
		Conn: clientagent.ConnSpec{
			ConfigFile: absCfg,
			Context:    r.Context,
			Server:     c.Host, // raw --host; the agent re-resolves (profile, svcforward)
			ViaServer:  cn.ViaServer(nil),
			Token:      os.Getenv("CORNUS_TOKEN"),
		},
		Conduit: cfg,
		Web: clientagent.WebSpec{
			Files:       files,
			EnvFiles:    envFiles,
			ProjectName: c.ProjectName,
			Frontend:    c.Frontend,
			Name:        name,
			Port:        c.PublishPort,
			Version:     version,
			MCP:         c.MCP,
		},
	}
	return req, name, nil
}

// absPaths resolves each path to absolute, so the env-frozen agent reads the same
// files regardless of its spawn-time working directory.
func absPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("resolving %q: %w", p, err)
		}
		out[i] = abs
	}
	return out, nil
}

// socketAgentView is the CLI's webbff.AgentView: it round-trips to the background
// agent over its control socket. It never starts one — `cornus web` only reports
// what a running agent is doing.
type socketAgentView struct{}

func (socketAgentView) Socket() string { return clientagent.Socket() }

func (socketAgentView) Status() *webbff.AgentStatus {
	resp, err := clientagent.Send(clientagent.Socket(), clientagent.Request{Action: "status"})
	if err != nil || !resp.OK {
		return nil
	}
	st := &webbff.AgentStatus{Forwards: resp.Forwards}
	if resp.Inventory != nil {
		st.Projects = resp.Inventory.Projects
		st.Banners = resp.Inventory.Banners
	}
	return st
}

// requireLoopback rejects a listen address that would expose the auth-less UI
// beyond this machine. The host must be empty (net defaults to all interfaces —
// rejected), "localhost", or a loopback IP literal.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("--addr %q is not a loopback address; the web UI has no auth and must stay local", addr)
}
