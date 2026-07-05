package clientagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/webbff"
	"cornus/pkg/memlisten"
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
	if spec.Name == "" {
		return Response{OK: false, Error: "web-serve: missing name"}, nil
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return Response{OK: false, Error: fmt.Sprintf("web-serve: port %d out of range", spec.Port)}, nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.webs[spec.Name]; ok {
		// First-writer-wins with a loud error: a second `cornus web` claiming the
		// same name would otherwise leave BOTH UIs unreachable (the conduit cannot
		// resolve an ambiguous name, and the apex has no qualified fallback the way
		// an alias does).
		return Response{OK: false, Error: fmt.Sprintf("the web UI name %q is already published by another cornus web (use --publish-name for a second one)", spec.Name)}, nil
	}

	cs, err := a.ensureConnLocked(req.Conn)
	if err != nil {
		return Response{OK: false, Error: err.Error()}, nil
	}
	session := "web:" + spec.Name
	es, err := a.ensureConduitLocked(cs, req.Conduit.Runtime(), session)
	if err != nil {
		a.releaseConnLocked(cs)
		return Response{OK: false, Error: err.Error()}, nil
	}
	egKey := conduitKeyOf(req.Conduit.Runtime(), session)
	fail := func(err error) (Response, *webFrontend) {
		a.releaseConduitLocked(cs, egKey)
		a.releaseConnLocked(cs)
		return Response{OK: false, Error: err.Error()}, nil
	}

	// Build the BFF over the shared server connection. Its agent view reads this
	// agent's own live state directly (no socket round-trip to itself).
	resolver := &clientconn.Resolver{ConfigFile: req.Conn.ConfigFile, Context: req.Conn.Context}
	bffCfg := webbff.Config{
		Files:         spec.Files,
		EnvFiles:      spec.EnvFiles,
		ProjectName:   spec.ProjectName,
		Frontend:      spec.Frontend,
		ConfigPath:    req.Conn.ConfigFile,
		Context:       req.Conn.Context,
		Host:          req.Conn.Server,
		Version:       spec.Version,
		PublishedName: spec.Name,
		MCP:           spec.MCP,
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
	lis := memlisten.New(spec.Name)
	pubCtx, cancel := context.WithCancel(a.ctx)
	published, err := es.eg.AddLocal(pubCtx, spec.Name, spec.Port, lis)
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
	fe := &webFrontend{name: spec.Name, port: spec.Port, lis: lis, srv: srv, bff: bff, conn: cs, egKey: egKey, cancel: cancel}
	fe.token = a.sup.Add("web:"+spec.Name, supervisor.ServiceFunc(func(ctx context.Context) error {
		go func() { <-ctx.Done(); _ = srv.Close() }()
		err := srv.Serve(lis)
		if ctx.Err() == nil {
			// Unexpected exit (not a web-stop / shutdown): reap so the shared refs,
			// the published name, and the BFF's terminals are released rather than
			// orphaned. The RemoveOnExit child is already forgotten, so reapWeb must
			// not double-remove — it guards on the map entry.
			go a.reapWeb(spec.Name)
		}
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}), supervisor.RemoveOnExit)
	a.webs[spec.Name] = fe
	return Response{OK: true, Banners: es.eg.Banner()}, fe
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
	}
}
