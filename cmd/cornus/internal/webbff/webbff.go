// Package webbff is the web UI's backend-for-frontend: the embedded SolidJS
// single-page app (pkg/webui) plus the JSON/WebSocket surface under
// /.cornus/web/* — the sibling of the server's /.cornus/v1/* control-plane
// namespace.
//
// The BFF runs from the CLIENT vantage point on purpose: compose projects,
// depends_on edges, mount sources, and the background agent's live sessions only
// exist client-side (the server sees flattened workloads), so it joins the compose
// model, the server's /.cornus/v1/* API (pkg/client), and the client agent's
// inventory into one origin for the browser.
//
// It is a package rather than part of `cornus web` because it has two hosts: the
// foreground CLI, which binds a loopback listener, and the client agent, which
// serves it on an addressless in-process listener so the shared SOCKS5 conduit can
// publish it under a name (one browser proxy setting then reaches both the
// workloads and this UI). Nothing here may import the agent — the agent imports
// this — so its inventory arrives through the AgentView seam.
package webbff

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/client"
	"cornus/pkg/compose"
	"cornus/pkg/webui"
)

// AgentStatus is the client agent's live session inventory, as the BFF needs it.
// It mirrors the agent's own status reply without importing it (that would be an
// import cycle: the agent hosts this package).
type AgentStatus struct {
	// Forwards maps a service to its live local port-forward lines.
	Forwards map[string][]string
	// Projects maps a project name to the services with a live agent session.
	Projects map[string][]string
	// Banners are the conduit banners (e.g. the SOCKS5 proxy listen line).
	Banners []string
}

// AgentView is the BFF's read-only window onto the client agent. The CLI wires an
// implementation that round-trips over the agent's control socket; the agent wires
// one that reads its own state directly.
type AgentView interface {
	// Socket is the agent's control socket path, shown in the UI's config view.
	Socket() string
	// Status returns the agent's live inventory, or nil when no agent answers.
	Status() *AgentStatus
}

// Config is everything the BFF needs that used to come from the `cornus web`
// command's flags and the global CLI flags.
//
// Every path must be ABSOLUTE when the agent is the host: it is env-frozen at
// spawn, so its working directory is not the caller's (see
// clientconn.Resolver.AbsConfigPath for the same discipline).
type Config struct {
	// Files are the compose file(s). Empty triggers discovery in the working
	// directory, which only makes sense for the in-process CLI host.
	Files []string
	// EnvFiles are the env file(s) used for compose interpolation.
	EnvFiles []string
	// ProjectName overrides the compose project name (default: the file's directory).
	ProjectName string
	// Frontend, when set, is a detached frontend dev-server URL: non-BFF requests
	// are reverse-proxied there instead of served from the embedded assets.
	Frontend string

	// ConfigPath, Context, and Host are replayed as flags when the apply endpoint
	// re-execs `cornus compose up -d`.
	ConfigPath string
	Context    string
	Host       string

	// Version is the cornus version string reported to the UI.
	Version string

	// PublishedName, when set, is the conduit name this UI is published under
	// (e.g. "cornus.internal"). It is accepted as a Host header in addition to the
	// loopback spellings.
	PublishedName string

	// MCP co-hosts an MCP (Model Context Protocol) server at /.cornus/mcp on the
	// same mux, behind the same guardHost, backed by the same data joins. It is on
	// by default (opt out with `cornus web --no-mcp`).
	MCP bool
}

// Server is the BFF: the resolved client, the (optional) compose project, and the
// file allow-list the editor endpoints may touch.
type Server struct {
	cfg      Config
	agent    AgentView
	client   *client.Client
	endpoint string
	resolver *clientconn.Resolver

	// project is nil when no compose file was found; every project-shaped
	// endpoint then answers 404/empty rather than erroring.
	project      *compose.Project
	projectName  string
	plans        map[string]compose.ServicePlan
	order        []string
	baseDir      string
	composeFiles []string

	// editable is the exact-path allow-list for the file editor endpoints:
	// compose files, env files, and the client config. Keys are cleaned
	// absolute paths.
	editable map[string]webFile

	// localRoots are the confined browsing roots for the file explorer's local
	// source: the project dir plus each external bind-mount source. localRootByID
	// indexes them by id. Built in loadProject.
	localRoots    []localRoot
	localRootByID map[string]localRoot

	// cfs is the container-filesystem seam the explorer's container source uses;
	// the production value wraps s.client, tests inject a fake.
	cfs containerFS

	// terms holds the persistent terminal sessions backing the tiled workspace
	// (see term.go).
	terms *termManager
}

// New builds a BFF over cl (the resolved cornus server client) and av (the client
// agent view). It loads the compose project described by cfg; a missing project is
// not an error, the UI simply has no project views.
func New(cfg Config, cl *client.Client, endpoint string, resolver *clientconn.Resolver, av AgentView) (*Server, error) {
	s := &Server{
		cfg:      cfg,
		agent:    av,
		client:   cl,
		endpoint: endpoint,
		resolver: resolver,
	}
	s.terms = newTermManager(cl)
	s.cfs = clientContainerFS{c: cl}
	if err := s.loadProject(); err != nil {
		return nil, err
	}
	return s, nil
}

// Project reports the loaded project's name and files, for the host's startup
// banner. Both are zero when no compose project was found.
func (s *Server) Project() (name string, files []string) { return s.projectName, s.composeFiles }

// Close releases what the BFF owns. It must be called when the host stops serving:
// the terminal sessions deliberately outlive their HTTP requests, so nothing else
// would ever reap them — inside the long-lived agent they would leak for the
// process's life.
func (s *Server) Close() {
	if s.terms != nil {
		s.terms.closeAll()
	}
}

// Handler builds the whole origin: the SPA (or a reverse proxy to a detached
// frontend dev server) at /, the BFF under /.cornus/web/*, and the Host allow-list
// guarding both.
func (s *Server) Handler() (http.Handler, error) {
	mux := http.NewServeMux()
	// Root handler: the embedded SPA, or — in detached-frontend mode — a reverse
	// proxy to a separately-run dev server (Vite) so a UI change hot-reloads
	// without rebuilding the binary. The BFF routes are registered after and win
	// by ServeMux pattern specificity (/.cornus/web/... beats /), so only non-BFF
	// requests reach this handler.
	if s.cfg.Frontend != "" {
		feURL, err := url.Parse(s.cfg.Frontend)
		if err != nil {
			return nil, fmt.Errorf("invalid --frontend %q: %w", s.cfg.Frontend, err)
		}
		if feURL.Scheme == "" || feURL.Host == "" {
			return nil, fmt.Errorf("invalid --frontend %q: need a scheme and host (e.g. http://localhost:5173)", s.cfg.Frontend)
		}
		// ReverseProxy forwards Connection: Upgrade, so Vite's HMR WebSocket rides
		// through this same origin.
		mux.Handle("/", httputil.NewSingleHostReverseProxy(feURL))
	} else {
		mux.Handle("/", webui.Handler())
	}
	s.routes(mux)
	// Co-host MCP at /.cornus/mcp (a sibling of /.cornus/web/*). ServeMux pattern
	// specificity keeps it distinct from "/", and the outer guardHost wrap below
	// guards it exactly like the web routes.
	if s.cfg.MCP {
		mux.Handle("/.cornus/mcp", s.MCPHandler())
	}
	return s.guardHost(mux), nil
}

// guardHost rejects requests whose Host is not one this UI is served under.
//
// The BFF has no authentication and exposes exec, persistent terminals, and
// compose-file writes, so an attacker page that can make the browser resolve some
// name to this origin would otherwise reach all of it with the user's credentials
// (DNS rebinding). Pinning the Host to the names we actually answer to removes
// that: a rebound name arrives with an unexpected Host and is refused.
//
// It is deliberately permissive about the port — a loopback host is legitimate on
// whatever ephemeral port was bound — and about a missing Host, which HTTP/2 turns
// into :authority that Go still surfaces as r.Host.
func (s *Server) guardHost(next http.Handler) http.Handler {
	allowed := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	if s.cfg.PublishedName != "" {
		allowed[strings.ToLower(s.cfg.PublishedName)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.Trim(host, "[]"))
		if !allowed[host] && !isLoopbackHost(host) {
			http.Error(w, "unrecognized Host header", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackHost reports whether host is a loopback IP literal. It does NOT
// resolve names: a name that merely resolves to loopback is exactly the DNS
// rebinding case guardHost exists to reject.
func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DiscoverComposeFiles returns the default compose file for the working directory
// (compose.yaml first), or nil when none is present — the same discovery the CLI
// uses. It is exported so the `cornus web --publish-in-conduit` client can resolve
// files in the USER's cwd and send absolute paths, since the agent that hosts the
// BFF has a frozen, different working directory.
func DiscoverComposeFiles() []string {
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if _, err := os.Stat(name); err == nil {
			return []string{name}
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
