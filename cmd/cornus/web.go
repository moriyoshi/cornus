package main

// The `cornus web` command: a local web UI for cornus. The UI and its
// backend-for-frontend live in cmd/cornus/internal/webbff; this file is the
// command shell that resolves the connection, builds the BFF, and serves it on a
// loopback listener.

import (
	"context"
	"fmt"
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
	"cornus/pkg/memlisten"
	"cornus/pkg/socks5"
	"cornus/pkg/webui"
)

// defaultWebAddr is the loopback listener default; also used to tell an explicit
// --addr from the default when --publish-in-conduit binds no listener.
const defaultWebAddr = "127.0.0.1:0"

// WebCmd serves the embedded web UI and its backend-for-frontend API.
type WebCmd struct {
	Addr        string   `kong:"name='addr',default='127.0.0.1:0',help='Listen address for the web UI. Must be a loopback address unless --allow-non-loopback is given (the UI has no auth).'"`
	Host        string   `kong:"name='host',short='H',env='CORNUS_HOST',help='cornus server endpoint. Falls back to the selected connection profile, then http://localhost:5000.'"`
	Files       []string `kong:"name='file',short='f',sep='none',help='Compose file(s). Repeatable. Defaults to compose.yaml / docker-compose.yml in the working directory; without one the project views stay empty.'"`
	EnvFile     []string `kong:"name='env-file',sep='none',help='Env file(s) for variable interpolation, replacing the default .env discovery. Repeatable.'"`
	ProjectName string   `kong:"name='project-name',short='p',help='Project name (default: the Compose file directory name).',env='COMPOSE_PROJECT_NAME'"`
	Open        bool     `kong:"name='open',help='Open the UI in the default browser once listening.'"`
	LocalRoot   []string `kong:"name='local-root',sep='none',help='Extra directory the file explorer can browse, as [LABEL=]DIR[:ro] (repeatable). Without this the explorer sees only the Compose project directory and its bind-mount sources.'"`
	Frontend    string   `kong:"name='frontend',env='CORNUS_WEB_FRONTEND',help='Detached frontend dev-server URL (e.g. http://localhost:5173). When set, non-BFF requests are reverse-proxied there instead of served from the embedded assets, so the Vite dev server can run separately with hot-reload.'"`
	MCP         bool     `kong:"name='mcp',negatable,default='true',help='Co-host an MCP (Model Context Protocol) server at /.cornus/mcp so agent clients (Zed, Claude Desktop) can drive workloads, files, logs, and exec. Inherits the same loopback/no-auth threat model as the UI; with --publish-in-conduit it is exposed to the conduit alongside the UI. Use --no-mcp to disable.'"`
	MCPStdio    bool     `kong:"name='mcp-stdio',help='Serve only the MCP server, over stdin/stdout, instead of binding an HTTP listener — for agent clients that launch a command rather than dial a URL (Zed context servers, Claude Desktop). Binds no port; diagnostics go to stderr. Mutually exclusive with --publish-in-conduit.'"`

	AllowNonLoopback bool     `kong:"name='allow-non-loopback',help='Permit binding --addr to a wildcard or non-loopback address. Refused by default: the UI and its MCP endpoint have no authentication and expose exec, terminals, and file writes, so off-host they hand that to anyone who can reach the port. Unless --allow-host is also given, this drops the DNS-rebinding Host check too.'"`
	AllowHost        []string `kong:"name='allow-host',help='Host header value to serve under, in addition to the loopback spellings (repeatable, e.g. the LAN IP or hostname you browse to). Keeps the DNS-rebinding Host check on for an --allow-non-loopback bind instead of dropping it.'"`

	Conduit     string `kong:"name='conduit',help='SOCKS5 conduit selector for --publish-in-conduit (bare \\'socks5\\', or socks5://host:port[?suffix=SUFFIX]). Without it the UI JOINS whatever shared SOCKS5 conduit the background agent already runs for this connection, so its settings need not be kept in sync with your workload sessions by hand. Naming an address or a suffix PINS those settings instead: they are used exactly, which can deliberately start a second proxy.'"`
	Publish     bool   `kong:"name='publish-in-conduit',help='Instead of binding a local port, host the UI inside the background agent and publish it in the shared SOCKS5 conduit, so one browser proxy setting reaches both your workloads and this UI. Requires a socks5 conduit.'"`
	PublishName string `kong:"name='publish-name',help='Conduit host name to publish the UI under (default: the service-host suffix apex of the conduit it joins, e.g. cornus.internal). Implies --publish-in-conduit.'"`
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

	if err := c.checkListenAddr(); err != nil {
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

	bffCfg, err := c.bffConfig(cli)
	if err != nil {
		return err
	}
	bff, err := webbff.New(bffCfg, cn.Client(), cn.Endpoint, r, socketAgentView{})
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

	// Warn on the EXPOSURE, not on the flag: `--allow-non-loopback` with a loopback
	// --addr (or none) binds nothing new, and a warning there would be the kind that
	// teaches people to ignore warnings.
	if c.offHost() {
		d.Warn("the web UI is bound to %s with no authentication: anyone who can reach that address gets exec, terminals, and file writes on this connection", c.Addr)
		if len(c.AllowHost) == 0 {
			d.Warn("no --allow-host given, so the DNS-rebinding Host check is off; pass --allow-host with the name(s) you browse to in order to keep it on")
		}
	}
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
// endpoint replays) onto the BFF's config. It errors on an unusable --local-root,
// which is worth catching here rather than serving a root that 404s on its first
// listing.
func (c *WebCmd) bffConfig(cli *CLI) (webbff.Config, error) {
	roots, err := parseLocalRoots(c.LocalRoot)
	if err != nil {
		return webbff.Config{}, err
	}
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
		LocalRoots:  roots,
		// --allow-host names are honoured for any bind (a loopback origin can also
		// be reached under an /etc/hosts alias), but the pin is only DROPPED for a
		// bind that is ACTUALLY off-host and left unnamed. Passing the flag while
		// still binding loopback must not cost the guard anything.
		AllowedHosts: c.AllowHost,
		AllowAnyHost: c.offHost() && len(c.AllowHost) == 0,
	}, nil
}

// parseLocalRoots parses the --local-root flags into the BFF's root specs.
func parseLocalRoots(specs []string) ([]webbff.LocalRootSpec, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]webbff.LocalRootSpec, 0, len(specs))
	for _, spec := range specs {
		r, err := parseLocalRoot(spec)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// parseLocalRoot reads one `[LABEL=]DIR[:ro]`.
//
// The LABEL split is on the FIRST "=", and only when what precedes it looks like
// a label rather than a path — no separator in it. Without that rule a perfectly
// ordinary directory containing "=" would be silently cut in half and mounted
// somewhere the caller never named, which is a worse failure than not supporting
// labels at all: it is wrong AND quiet.
//
// The directory is resolved to absolute and required to exist, because the agent
// that may host this BFF has its own working directory, and because a root that
// only reveals itself as missing on the first listing is a bad trade for one
// os.Stat here.
func parseLocalRoot(spec string) (webbff.LocalRootSpec, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return webbff.LocalRootSpec{}, fmt.Errorf("--local-root: empty value")
	}
	var readOnly bool
	if rest, ok := strings.CutSuffix(s, ":ro"); ok {
		s, readOnly = rest, true
	} else if rest, ok := strings.CutSuffix(s, ":rw"); ok {
		// Accepted for symmetry with the mount syntax people already know; it is
		// the default, so it only has to not be an error.
		s = rest
	}
	var label string
	if name, dir, ok := strings.Cut(s, "="); ok && name != "" && !strings.ContainsAny(name, `/\`) {
		label, s = name, dir
	}
	if s == "" {
		return webbff.LocalRootSpec{}, fmt.Errorf("--local-root %q: no directory", spec)
	}
	abs, err := filepath.Abs(s)
	if err != nil {
		return webbff.LocalRootSpec{}, fmt.Errorf("--local-root %q: %w", spec, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return webbff.LocalRootSpec{}, fmt.Errorf("--local-root %q: %w", spec, err)
	}
	if !fi.IsDir() {
		return webbff.LocalRootSpec{}, fmt.Errorf("--local-root %q: %s is not a directory", spec, abs)
	}
	return webbff.LocalRootSpec{Label: label, Path: abs, ReadOnly: readOnly}, nil
}

// offHost reports whether this run really does bind the UI beyond this machine:
// the operator opted in AND the address they gave is one requireLoopback would
// otherwise have refused. It is what the exposure warning and the dropped Host
// pin both key on, so neither can fire for a bind that stayed local.
func (c *WebCmd) offHost() bool {
	return c.AllowNonLoopback && requireLoopback(c.Addr) != nil
}

// checkListenAddr rejects a listen address that would expose the auth-less UI
// beyond this machine, unless the operator opted in with --allow-non-loopback.
// The opt-out drops the loopback policy, not the parse: a host:port that net
// cannot read is still worth naming here rather than at the bind.
func (c *WebCmd) checkListenAddr() error {
	if !c.AllowNonLoopback {
		return requireLoopback(c.Addr)
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return fmt.Errorf("invalid --addr %q: %w", c.Addr, err)
	}
	return nil
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

	cfg, err := c.bffConfig(cli)
	if err != nil {
		return err
	}
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

	ctx, stop := signal.NotifyContext(cli.rootContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, warnings, err := c.publishConduitConfig(ctx, cn)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		d.Warn("%s", w)
	}

	// Join the conduit — or create it — in THIS process. The UI used to be hosted
	// inside the background agent, for one reason only: that was where the conduit
	// was. Now that a conduit is a rendezvous at an address, the UI can live where it
	// belongs, and its lifetime is this process's rather than something the agent has
	// to be told about and reap.
	conduit, err := clientconduit.Start(ctx, cn.Dialer(cn.ViaServer(nil)), cfg,
		clientconduit.WithRendezvous(clientagent.ConduitRegistry()),
		clientconduit.WithLogf(func(format string, args ...any) { d.Warn(format, args...) }))
	if err != nil {
		return err
	}
	defer conduit.Close()

	// Only now is the name knowable, and it must come from the conduit we are
	// ACTUALLY in. webbff pins its Host allow-list to this name, so a name carrying
	// the suffix we REQUESTED while the conduit serves another would resolve through
	// the proxy and then answer 421.
	name := c.PublishName
	if name == "" {
		name = strings.TrimPrefix(clientconduit.SuffixOf(conduit), ".")
	}

	bffCfg, err := c.bffConfig(cli)
	if err != nil {
		return err
	}
	bffCfg.PublishedName = name
	bff, err := webbff.New(bffCfg, cn.Client(), cn.Endpoint, r, socketAgentView{})
	if err != nil {
		return err
	}
	defer bff.Close()
	handler, err := bff.Handler()
	if err != nil {
		return err
	}

	// An addressless listener: the UI is reachable only through the proxy, so no
	// bound port exists for the kernel to recycle to a squatter.
	lis := memlisten.New(name)
	defer lis.Close()
	srv := &http.Server{Handler: handler}
	defer srv.Close()
	go func() { _ = srv.Serve(lis) }()

	published, err := conduit.AddLocal(ctx, name, c.PublishPort, lis)
	if err != nil {
		return err
	}
	if !published {
		return fmt.Errorf("the conduit publishes no names; re-run with --conduit socks5")
	}

	display := name
	if c.PublishPort != 80 {
		display = fmt.Sprintf("%s:%d", display, c.PublishPort)
	}
	for _, b := range conduit.Banner() {
		d.Info("%s", b)
	}
	for _, line := range conduit.CAInfo() {
		d.Info("%s", line)
	}
	d.Info("cornus web UI (in conduit): http://%s/", display)
	if c.MCP {
		d.Info("cornus MCP endpoint (in conduit): http://%s/.cornus/mcp", display)
	}
	d.Done("publishing the UI through the shared SOCKS5 conduit; point your browser's proxy at it and open http://%s/ (Ctrl-C to stop)", display)

	<-ctx.Done()
	d.Done("stopped publishing %s", display)
	return nil
}

// publishConduitConfig resolves the conduit a published UI should be in. It is the
// pure part of runPublished — the whole "where does this belong" decision —
// separated so the serve loop stays thin and this can be tested without binding
// anything.
//
// It does NOT decide the published name. The name is derived from the suffix of the
// conduit we actually land in, which is knowable only once we are in it.
func (c *WebCmd) publishConduitConfig(ctx context.Context, cn *clientconn.Conn) (clientconduit.Config, []string, error) {
	var warnings []string
	if c.Addr != "" && c.Addr != defaultWebAddr {
		return clientconduit.Config{}, nil, fmt.Errorf("--addr and --publish-in-conduit are mutually exclusive: publishing in the conduit binds no local port")
	}
	if len(c.AllowHost) > 0 {
		return clientconduit.Config{}, nil, fmt.Errorf("--allow-host and --publish-in-conduit are mutually exclusive: the published UI has no bound listener whose origin could be widened, and answers to --publish-name")
	}
	if c.PublishPort < 1 || c.PublishPort > 65535 {
		return clientconduit.Config{}, nil, fmt.Errorf("--publish-port %d out of range (1-65535)", c.PublishPort)
	}

	// Force socks5: --publish-in-conduit needs a name-resolving proxy. Passing
	// ModeSocks5 as the override forces the mode while inheriting the profile's
	// listen/suffix, exactly like `cornus socks5`. An explicit --conduit that is not
	// socks5 is a contradiction, caught here before anything is bound.
	override := c.Conduit
	if override == "" {
		override = clientconduit.ModeSocks5
	}
	cfg, err := cn.ConduitConfig(override)
	if err != nil {
		return clientconduit.Config{}, nil, err
	}
	if cfg.Mode != clientconduit.ModeSocks5 {
		return clientconduit.Config{}, nil, fmt.Errorf("--publish-in-conduit requires a socks5 conduit, but --conduit resolves to %q; drop it or pass --conduit socks5", cfg.Mode)
	}
	// --allow-non-loopback authorizes the CONDUIT's bind, and it has to be available
	// here: this command may be the first participant at the address, in which case
	// it binds the proxy itself. The flag used to be refused outright on the grounds
	// that publishing binds no local port — true when the UI could only ever be
	// hosted by the agent, and false since it hosts its own.
	cfg.Socks5AllowNonLoopback = c.AllowNonLoopback
	// A browser has ONE proxy setting, so a published UI belongs in the conduit the
	// workloads are in — never in a private one the profile happens to ask for.
	cfg.Socks5SessionLocal = false

	// Resolve ingress the way `compose up -d` does, so a conduit this call CREATES is
	// one a later compose session can share rather than collide with.
	ingressOverride, err := clientconn.ConfigFromOptions(nil, "", "", "", "", "")
	if err != nil {
		return clientconduit.Config{}, nil, err
	}
	cn.ApplyIngressConfig(ctx, &cfg, ingressOverride)

	// Whether the caller PINNED an address, decided from the raw flag rather than the
	// resolved config: only the flag can tell "the profile happens to say 1080" from
	// "the user asked for 1080". Naming an address or a suffix is a pin. A bare
	// `socks5`, an absent flag, CORNUS_CONDUIT and the profile are ambient defaults,
	// and a browser has no opinion about which port its proxy is on — so those go
	// wherever the workloads already are.
	spec, err := clientconn.ParseConduitSpec(c.Conduit)
	if err != nil {
		return clientconduit.Config{}, nil, err
	}
	if !spec.HasListen && !spec.HasSuffix {
		if addr, ok := discoverLiveConduit(); ok {
			cfg.Socks5Listen = addr
			// The exposure decision belongs to whoever CREATED this conduit, and they
			// already made it. Requiring the joiner to consent again would refuse a
			// caller that is binding nothing, which is how a plain `cornus web
			// --publish-in-conduit` ended up unable to join a conduit already serving
			// 0.0.0.0. Warn rather than refuse, and say what was joined.
			if !cfg.Socks5AllowNonLoopback && socks5.LooksNonLoopback(addr) {
				cfg.Socks5AllowNonLoopback = true
				warnings = append(warnings, fmt.Sprintf("joined the SOCKS5 conduit already running at %s, which is reachable off-host", addr))
			}
		}
	}
	return cfg, warnings, nil
}

// discoverLiveConduit answers "where are the workloads" for a caller with no
// opinion about the address, by asking the rendezvous registry rather than any one
// process's memory. The lowest port wins when there are several, so the answer does
// not depend on readdir order.
func discoverLiveConduit() (string, bool) {
	entries, err := clientagent.ConduitRegistry().LiveAll()
	if err != nil || len(entries) == 0 {
		return "", false
	}
	return entries[0].Bind, true
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
		st.Ingress = clientagent.ToBFFIngress(resp.Inventory.Ingress)
	}
	return st
}

// requireLoopback rejects a listen address that would expose the auth-less UI
// beyond this machine. The host must be empty (net defaults to all interfaces —
// rejected), "localhost", or a loopback IP literal. It is the default policy;
// --allow-non-loopback opts out of it (see checkListenAddr).
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
	return fmt.Errorf("--addr %q is not a loopback address; the web UI has no auth and stays local by default (pass --allow-non-loopback to bind it anyway)", addr)
}
