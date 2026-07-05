package dockerproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sync"

	"cornus/pkg/api"
)

// apiVersion is advertised to clients (Docker negotiates down to this).
const apiVersion = "1.43"

// Proxy is a Docker-Engine-API-compatible HTTP handler backed by a cornus
// server. It is a long-running daemon: each started container holds a
// deploy-attach session (keeping its 9P mounts alive) — and, for published
// ports, local port-forward listeners — until stopped or removed.
//
// Deliberately imperative — NOT the reconcile engine. The client-side reconcile
// engine (cmd/cornus/internal/clientagent.Project) converts a DECLARATIVE surface
// (a compose file = a desired service set) into imperative backend operations. The
// Docker Engine API is already imperative: create/start/stop/rm are discrete edge
// events, containers are immutable (a config change is a new create = a new id),
// and the ordering/blocking contracts (concurrent /start races, attach-before-start,
// wait?condition=next-exit) live in the containerRecord state machine (state.go).
// There is no desired-set to reconcile, so the Proxy stays edge-triggered and does
// NOT use Project. What it DOES share with the engine is the one imperative
// primitive beneath both — the per-workload deploy-attach hold (pkg/attachsession) —
// and, via WithConduit, the exposure primitive (clientconduit.Conduit.Add).
type Proxy struct {
	attacher     deployAttacher
	reg          *registry
	networks     *networkStore
	volumes      *volumeStore
	execs        *execRegistry
	hub          *eventHub
	mux          *http.ServeMux
	forwardPorts bool
	// conduitAdd, when set (WithConduit), exposes each started container's ports
	// through the agent's shared per-server conduit instead of the proxy's own
	// per-container portfwd.Group. It exposes name's ports under ctx; cancelling
	// ctx withdraws them. A nil-return means success.
	conduitAdd func(ctx context.Context, name string, ports []api.PortMapping) error
	// imageCfgs caches successful image-inspect bodies per reference (see
	// handleImageInspect); failed lookups are not cached so a registry that
	// comes up later is retried.
	imageCfgs sync.Map
}

// Option configures a Proxy.
type Option func(*Proxy)

// WithoutPortForwards disables publishing container PortBindings on local
// listeners (the docker -p ports are then only reachable wherever the backend
// itself publishes them).
func WithoutPortForwards() Option { return func(p *Proxy) { p.forwardPorts = false } }

// WithConduit routes started containers' published ports through add (the agent's
// shared per-server conduit) instead of the proxy's own per-container listeners.
// In SOCKS5 mode that means one proxy reaches docker containers and compose
// services alike by name; in port-forward mode add still binds local listeners,
// just through the shared plumbing.
func WithConduit(add func(ctx context.Context, name string, ports []api.PortMapping) error) Option {
	return func(p *Proxy) { p.conduitAdd = add }
}

// Close stops every live container session and withdraws its port exposure. The
// agent calls it on docker-stop: the agent process outlives a single `docker`
// invocation, so Ctrl-C no longer implicitly tears held sessions down.
func (p *Proxy) Close() {
	for _, rec := range p.reg.all() {
		if sess := rec.session(); sess != nil {
			sess.Stop()
			rec.setExited(sess)
		}
	}
}

// New builds a Proxy driving the given cornus client (a *client.Client
// satisfies deployAttacher).
func New(attacher deployAttacher, opts ...Option) *Proxy {
	p := &Proxy{
		attacher:     attacher,
		reg:          newRegistry(),
		networks:     newNetworkStore(),
		volumes:      newVolumeStore(),
		execs:        newExecRegistry(),
		hub:          newEventHub(),
		mux:          http.NewServeMux(),
		forwardPorts: true,
	}
	for _, o := range opts {
		o(p)
	}
	p.routes()
	return p
}

// Handler returns the HTTP handler, stripping any leading /vX.Y API-version
// prefix so both versioned and unversioned Docker requests route correctly.
func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = stripVersion(r.URL.Path)
		p.mux.ServeHTTP(w, r)
	})
}

var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

func stripVersion(path string) string {
	return versionPrefix.ReplaceAllString(path, "")
}

func (p *Proxy) routes() {
	p.mux.HandleFunc("/_ping", p.handlePing)
	p.mux.HandleFunc("/version", p.handleVersion)
	p.mux.HandleFunc("/info", p.handleInfo)

	p.mux.HandleFunc("/containers/create", p.handleContainerCreate)
	p.mux.HandleFunc("/containers/json", p.handleContainerList)
	p.mux.HandleFunc("/containers/", p.handleContainerItem)

	// Exec lifecycle: exec-start (hijacked raw tunnel) + exec-inspect.
	p.mux.HandleFunc("/exec/", p.handleExecItem)

	p.mux.HandleFunc("/images/create", p.handleImageCreate)
	p.mux.HandleFunc("/images/json", p.handleImageList)
	p.mux.HandleFunc("/images/", p.handleImageInspect)

	// Compose support: fake networks + volumes, hold-open events.
	p.mux.HandleFunc("/networks", p.handleNetworkList)
	p.mux.HandleFunc("/networks/create", p.handleNetworkCreate)
	p.mux.HandleFunc("/networks/prune", p.handleNetworkPrune)
	p.mux.HandleFunc("/networks/", p.handleNetworkItem)
	p.mux.HandleFunc("/volumes", p.handleVolumeList)
	p.mux.HandleFunc("/volumes/create", p.handleVolumeCreate)
	p.mux.HandleFunc("/volumes/prune", p.handleVolumePrune)
	p.mux.HandleFunc("/volumes/", p.handleVolumeItem)
	p.mux.HandleFunc("/events", p.handleEvents)
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// dockerError writes a Docker-shaped {"message": ...} error.
func dockerError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"message": msg})
}
