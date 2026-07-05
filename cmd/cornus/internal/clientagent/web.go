package clientagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/webbff"
	"cornus/pkg/clientconduit"
	"cornus/pkg/memlisten"
	"cornus/pkg/socks5"
	"cornus/pkg/supervisor"
)

// webFrontend is one web UI the agent hosts: the BFF served on an addressless
// in-process listener, published in the shared conduit under name:port. Because
// the listener has no address, the UI is reachable only through the proxy — one
// browser proxy setting reaches both it and the workloads, and no bound port
// exists for the kernel to recycle to a squatter.
type webFrontend struct {
	name   string
	port   int
	lis    *memlisten.Listener
	srv    *http.Server
	bff    *webbff.Server
	conn   *connState
	egKey  conduitKey
	cancel context.CancelFunc // withdraws the published name from the conduit
	token  *supervisor.Token
}

// handleWebServe runs web-serve and then holds conn for the life of the
// registration. It writes the ack, and — if the UI was published — parks reading
// conn until the client goes away (EOF or error), then withdraws it. Holding the
// connection is what makes withdrawal reliable: the agent does not own the
// `cornus web` process, so nothing else could observe its death.
func (a *Agent) handleWebServe(conn net.Conn, req Request) {
	resp, fe := a.doWebServe(req)
	resp.Protocol = ProtocolVersion
	_ = json.NewEncoder(conn).Encode(resp)
	if fe == nil {
		return // publish failed; nothing is held
	}
	// Park until the client closes the connection. The client sends nothing after
	// the ack, so any successful read is unexpected but harmless; a read error
	// (EOF on close, reset on SIGKILL) is the withdrawal signal.
	buf := make([]byte, 1)
	for {
		if _, err := conn.Read(buf); err != nil {
			break
		}
	}
	a.reapWeb(fe.name)
}

// doWebServe builds the BFF for req.Web, publishes it in the shared conduit, and
// serves it on an in-process listener. It returns the frontend so handleWebServe
// can tie the registration to the control connection's lifetime; fe is nil on any
// failure (the Response carries the error).
func (a *Agent) doWebServe(req Request) (Response, *webFrontend) {
	spec := req.Web
	a.beginRequest()
	defer a.endRequest()
	if spec.Port < 1 || spec.Port > 65535 {
		return Response{OK: false, Error: fmt.Sprintf("web-serve: port %d out of range", spec.Port)}, nil
	}

	cfg := req.Conduit.Runtime()
	if cfg.Socks5SessionLocal {
		// A published UI joins the SHARED proxy by construction — a browser has one
		// proxy setting, and a session-local conduit is private on purpose. Refusing
		// here is also what keeps the conduit session string inert below, so the name
		// (which may not be known yet) cannot influence which conduit is keyed.
		return Response{OK: false, Error: "web-serve: a published web UI must join the shared conduit, but the request asks for a session-local one"}, nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	cs, err := a.ensureConnLocked(req.Conn)
	if err != nil {
		return Response{OK: false, Error: err.Error()}, nil
	}
	// The name is not settled yet (an empty spec.Name is derived from whichever
	// conduit we end up in), and it does not need to be: the session string only
	// enters a conduit's identity for a session-local conduit, which is refused
	// above.
	es, egKey, warnings, err := a.resolveWebConduitLocked(cs, cfg, "web:"+spec.Name, spec.JoinConduit)
	if err != nil {
		a.releaseConnLocked(cs)
		return Response{OK: false, Error: err.Error()}, nil
	}
	fail := func(err error) (Response, *webFrontend) {
		a.releaseConduitLocked(cs, egKey)
		a.releaseConnLocked(cs)
		return Response{OK: false, Error: err.Error()}, nil
	}

	// Only now is the published name knowable. Derived from the conduit we are
	// ACTUALLY in, never from the requested config: webbff pins its Host allow-list
	// to this name, so a name carrying the requested suffix while the UI sits in a
	// conduit with a different one would resolve through the proxy (router locals
	// are consulted before the rules) and then answer 421.
	name := spec.Name
	if name == "" {
		name = defaultPublishedName(es.cfg)
	}
	if _, ok := a.webs[name]; ok {
		// First-writer-wins with a loud error: a second `cornus web` claiming the
		// same name would otherwise leave BOTH UIs unreachable (the conduit cannot
		// resolve an ambiguous name, and the apex has no qualified fallback the way
		// an alias does).
		return fail(fmt.Errorf("the web UI name %q is already published by another cornus web (use --publish-name for a second one)", name))
	}

	// Build the BFF over the shared server connection. Its agent view reads this
	// agent's own live state directly (no socket round-trip to itself).
	resolver := &clientconn.Resolver{ConfigFile: req.Conn.ConfigFile, Context: req.Conn.Context, SSHKeyCacheReadOnly: true}
	bffCfg := webbff.Config{
		Files:         spec.Files,
		EnvFiles:      spec.EnvFiles,
		ProjectName:   spec.ProjectName,
		Frontend:      spec.Frontend,
		ConfigPath:    req.Conn.ConfigFile,
		Context:       req.Conn.Context,
		Host:          req.Conn.Server,
		Version:       spec.Version,
		PublishedName: name,
		MCP:           spec.MCP,
		LocalRoots:    webLocalRoots(spec.LocalRoots),
	}
	bff, err := webbff.New(bffCfg, cs.client, cs.conn.Endpoint, resolver, agentSelfView{a})
	if err != nil {
		return fail(err)
	}
	handler, err := bff.Handler()
	if err != nil {
		bff.Close()
		return fail(err)
	}

	// Publish the name -> in-process listener, withdrawn when pubCtx ends.
	lis := memlisten.New(name)
	pubCtx, cancel := context.WithCancel(a.ctx)
	published, err := es.eg.AddLocal(pubCtx, name, spec.Port, lis)
	if err != nil {
		cancel()
		bff.Close()
		_ = lis.Close()
		return fail(err)
	}
	if !published {
		// The conduit resolves no names (port-forward / none). The client forces
		// socks5 and rejects a contradiction first, so this is a defensive guard.
		cancel()
		bff.Close()
		_ = lis.Close()
		return fail(fmt.Errorf("conduit mode %q publishes no names; re-run with --conduit socks5", req.Conduit.Mode))
	}

	srv := &http.Server{Handler: handler}
	fe := &webFrontend{name: name, port: spec.Port, lis: lis, srv: srv, bff: bff, conn: cs, egKey: egKey, cancel: cancel}
	fe.token = a.sup.Add("web:"+name, supervisor.ServiceFunc(func(ctx context.Context) error {
		go func() { <-ctx.Done(); _ = srv.Close() }()
		err := srv.Serve(lis)
		if ctx.Err() == nil {
			// Unexpected exit (not a web-stop / shutdown): reap so the shared refs,
			// the published name, and the BFF's terminals are released rather than
			// orphaned. The RemoveOnExit child is already forgotten, so reapWeb must
			// not double-remove — it guards on the map entry.
			go a.reapWeb(name)
		}
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}), supervisor.RemoveOnExit)
	a.webs[name] = fe
	return Response{OK: true, Banners: es.eg.Banner(), Warnings: warnings, WebName: name}, fe
}

// defaultPublishedName is the host a UI publishes under when the caller named
// none: the apex of the service-host suffix of the conduit it is published in
// (".demo.internal" -> "demo.internal"), so the UI answers next door to the
// workloads rather than in a namespace of its own.
//
// cfg is expected to be the CANONICAL config of a live conduit, where the suffix
// is empty only for a rules-driven conduit — one that has no apex to speak of. The
// socks5 default is the answer there, and it still resolves: a published name is
// registered in the router's locals table, which is consulted before any rule.
func defaultPublishedName(cfg ConduitCfg) string {
	suffix := cfg.Socks5Suffix
	if suffix == "" {
		suffix = socks5.DefaultSuffix
	}
	return strings.TrimPrefix(suffix, ".")
}

// pickSharedConduit chooses which of a connection's live conduits a published web
// UI should join, and reports the runners-up so the caller can say what it passed
// over. want is the key the request's own settings would have taken; ok is false
// when there is nothing adoptable and the caller must start one.
//
// Candidates are the SHARED socks5 conduits. Port-forward and none resolve no
// names at all, and a session-local conduit is private by construction — adopting
// one would publish the UI exactly where the operator asked for isolation.
//
// The order is explicit because Go randomizes map iteration and more than one
// candidate implies genuinely different bind addresses: picking by range order
// would hand the same agent a different answer on every publish. An exact match on
// want wins outright (this makes joining a strict superset of the identity-sharing
// that already happens); otherwise the most-shared conduit wins, because "where the
// most frontends already are" is the best available reading of "where the workloads
// are", with the key's rendering as a deterministic tiebreak.
func pickSharedConduit(conduits map[conduitKey]*conduitState, want conduitKey) (best conduitKey, others []conduitKey, ok bool) {
	var candidates []conduitKey
	for key, es := range conduits {
		if es.cfg.Mode == clientconduit.ModeSocks5 && !es.cfg.Socks5SessionLocal {
			candidates = append(candidates, key)
		}
	}
	if len(candidates) == 0 {
		return conduitKey{}, nil, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if (a == want) != (b == want) {
			return a == want
		}
		if ra, rb := conduits[a].refs, conduits[b].refs; ra != rb {
			return ra > rb
		}
		return fmt.Sprintf("%v", a) < fmt.Sprintf("%v", b)
	})
	return candidates[0], candidates[1:], true
}

// resolveWebConduitLocked returns the conduit to publish a web UI in, with ONE
// reference already taken, plus the key that reference must later be released
// under and any warnings to relay. Caller holds a.mu and MUST pair the returned
// key with releaseConduitLocked — releasing under the REQUESTED key instead would
// find nothing in the map, silently leaking the conduit (see conduitKeyOf).
//
// With join set, cfg is only a fallback: it is used when the connection runs no
// adoptable conduit. That is the whole point of the flag — the settings a
// `cornus web` resolved for itself are the ones most likely to be subtly different
// from the workloads' (its config path never fills in ingress), and a difference
// there used to mean a second proxy that could not bind.
func (a *Agent) resolveWebConduitLocked(cs *connState, cfg ConduitCfg, session string, join bool) (*conduitState, conduitKey, []string, error) {
	want := conduitKeyOf(cfg, session)
	if join {
		if key, others, ok := pickSharedConduit(cs.conduit, want); ok {
			es := cs.conduit[key]
			es.refs++
			var warnings []string
			if key != want {
				warnings = append(warnings, fmt.Sprintf("published in the SOCKS5 conduit this agent already runs for this connection (%s) rather than starting a second one from the requested settings; pass --conduit socks5://ADDR to pin different settings instead", conduitAddr(es)))
			}
			if len(others) > 0 {
				addrs := make([]string, 0, len(others))
				for _, k := range others {
					addrs = append(addrs, conduitAddr(cs.conduit[k]))
				}
				warnings = append(warnings, fmt.Sprintf("this agent runs %d shared SOCKS5 conduits for this connection; joined %s (the most shared) and passed over %s — pass --conduit socks5://ADDR to choose", len(others)+1, conduitAddr(es), strings.Join(addrs, ", ")))
			}
			return es, key, warnings, nil
		}
	}
	es, err := a.ensureConduitLocked(cs, cfg, session)
	if err != nil {
		return nil, conduitKey{}, nil, err
	}
	return es, want, nil, nil
}

// conduitAddr describes a live conduit by the address a browser would point at.
// It asks the running proxy rather than reading the configuration, because an
// ephemeral bind ("127.0.0.1:0") only has an answer once it is bound.
func conduitAddr(es *conduitState) string {
	if p, ok := es.eg.(interface{ Addr() string }); ok {
		if addr := p.Addr(); addr != "" {
			return addr
		}
	}
	if es.cfg.Socks5Listen != "" {
		return es.cfg.Socks5Listen
	}
	return socks5.DefaultListen
}

// reapWeb tears down the web frontend named name: withdraw the published name,
// stop serving, reap the BFF's terminal sessions, and release the shared refs.
// Idempotent — the hold-connection EOF and an unexpected server exit can both call
// it, so it guards on the map entry.
func (a *Agent) reapWeb(name string) {
	a.mu.Lock()
	fe := a.webs[name]
	if fe == nil {
		a.mu.Unlock()
		return
	}
	delete(a.webs, name)
	a.releaseConduitLocked(fe.conn, fe.egKey)
	a.releaseConnLocked(fe.conn)
	tok := fe.token
	a.mu.Unlock()

	fe.cancel()       // withdraw the published name from the conduit
	fe.bff.Close()    // reap the BFF's persistent terminals / exec streams
	a.sup.Remove(tok) // cancel the child ctx -> srv.Close -> Serve returns
	_ = fe.lis.Close()
	a.armIdle() // the agent may now be idle
}

// doWebStop withdraws a published web UI by name, for a client that wants an
// explicit teardown. The usual path is simply closing the hold connection.
func (a *Agent) doWebStop(req Request) Response {
	if req.Web.Name == "" {
		return Response{OK: false, Error: "web-stop: missing name"}
	}
	a.reapWeb(req.Web.Name)
	return Response{OK: true}
}

// closeAllWebs reaps every web frontend's terminal sessions on shutdown. The http
// servers themselves close via their supervised child's ctx cancel; here we make
// sure each BFF's Close runs so no exec stream is left held across shutdown.
func (a *Agent) closeAllWebs() {
	a.mu.Lock()
	fes := make([]*webFrontend, 0, len(a.webs))
	for _, fe := range a.webs {
		fes = append(fes, fe)
	}
	a.mu.Unlock()
	for _, fe := range fes {
		fe.cancel()
		fe.bff.Close()
	}
}

// agentSelfView is the webbff.AgentView for a BFF hosted inside the agent: it
// reads the agent's own live inventory directly instead of dialing the control
// socket (which would be this very process).
type agentSelfView struct{ a *Agent }

func (agentSelfView) Socket() string { return Socket() }

func (v agentSelfView) Status() *webbff.AgentStatus {
	inv := v.a.inventory()
	return &webbff.AgentStatus{
		Projects: inv.Projects,
		Banners:  inv.Banners,
		Ingress:  ToBFFIngress(inv.Ingress),
	}
}

// ToBFFIngress restates an ingress inventory in the BFF's own vocabulary. The two
// shapes are field-identical and stay so deliberately: this package HOSTS webbff,
// so webbff cannot import it back and the structs cannot be shared. It is exported
// because the out-of-process AgentView (`cornus web`'s socketAgentView) needs the
// same translation on the far side of the control socket, and two hand-written
// copies would be free to drift.
func ToBFFIngress(in []AgentIngress) []webbff.AgentIngress {
	if len(in) == 0 {
		return nil
	}
	out := make([]webbff.AgentIngress, 0, len(in))
	for _, e := range in {
		w := webbff.AgentIngress{Mode: e.Mode, Domain: e.Domain, Trust: e.Trust}
		if c := e.Controller; c != nil {
			w.Controller = &webbff.AgentIngressController{
				KubeContext: c.KubeContext,
				Namespace:   c.Namespace,
				Service:     c.Service,
				HTTPPort:    c.HTTPPort,
				HTTPSPort:   c.HTTPSPort,
			}
		}
		out = append(out, w)
	}
	return out
}

// webLocalRoots converts the wire form of `--local-root` into the BFF's. Kept as
// a conversion rather than sharing one type because protocol.go is the wire
// contract: a field added to webbff.Config must not silently become part of it.
func webLocalRoots(in []WebLocalRoot) []webbff.LocalRootSpec {
	if len(in) == 0 {
		return nil
	}
	out := make([]webbff.LocalRootSpec, 0, len(in))
	for _, r := range in {
		out = append(out, webbff.LocalRootSpec{Label: r.Label, Path: r.Path, ReadOnly: r.ReadOnly})
	}
	return out
}
