package clientagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"cornus/cmd/cornus/internal/agentproc"
	"cornus/cmd/cornus/internal/daemonize"
	"cornus/pkg/clientconduit"
	"cornus/pkg/conduithost"
	"cornus/pkg/filewatch"
	"cornus/pkg/logging"
	"cornus/pkg/supervisor"
)

// idleLinger is how long the agent waits, after its last work unit goes away,
// before exiting — long enough to absorb a down-immediately-followed-by-up.
const idleLinger = 3 * time.Second

// requestReadTimeout bounds how long handle waits for a connecting client to
// send its request. A client (Send) writes the request immediately after
// connecting, so this is generous; it only exists to reap a client that opens
// the control socket and never writes, which would otherwise park handle in
// Decode forever and leak the goroutine + fd. It is a var so tests can shrink it.
var requestReadTimeout = 30 * time.Second

// Agent is the single client-side background process: one control socket, one
// supervisor tree, N per-server connections (each with a shared conduit), N
// compose projects, and (Phase 4) N docker frontends.
type Agent struct {
	resolve ResolveFunc
	sup     *supervisor.Supervisor
	ctx     context.Context
	stop    context.CancelFunc

	// conduitRegistry is the conduit rendezvous this agent binds through. Injectable
	// so it is never resolved from the environment by accident: a test that fell back
	// to the real one would write into the user's runtime directory and bind real
	// ports, which is exactly what happened the first time this was a package-level
	// function.
	conduitRegistry *conduithost.Registry

	idleMu    sync.Mutex
	idleTimer *time.Timer

	mu       sync.Mutex
	conns    map[connKey]*connState
	projects map[string]*projectEntry
	dockers  map[string]*dockerFrontend
	inflight int // registering requests in flight (keeps the agent from idle-exiting mid-request)
}

// projectEntry is one live compose project inside the agent.
//
// A project's IDENTITY is its name plus its server connection (conn.key); its
// CONFIGURATION is the conduit the project is exposed through (egKey). See
// ensureProject for why the line is drawn there.
type projectEntry struct {
	project *Project
	conn    *connState
	egKey   conduitKey
	banners []string          // conduit banner (SOCKS5 proxy address) of the CURRENT conduit
	token   *supervisor.Token // liveness token in the supervisor (drives idle)
	active  int               // in-flight `up` handlers referencing this entry (guarded by a.mu)

	// pendingRelease holds the conn/conduit refcount releases owed for conduits this
	// entry has been rebound OFF (see ensureProject). They are deferred to
	// releaseProjectUse — i.e. until after the reconcile that moved every
	// registration into the new conduit — so the old conduit is never closed while
	// registrations still ride it. Guarded by a.mu; each entry runs with a.mu held.
	pendingRelease []func()

	// rebindMu serializes conduit rebinds of THIS entry, so two concurrent `up`s
	// aiming at different conduits cannot interleave their "record the new key" and
	// "swap the project's conduit" halves and leave egKey naming one conduit while
	// the project is exposed through another. Only the rebind path takes it (never
	// the up-to-date fast path), and it is always taken with a.mu NOT held.
	rebindMu sync.Mutex

	// watch state (up --watch): a supervised loop watches watchFiles and re-execs
	// reload when they change. watching pins the entry against removeProject so a
	// watch-only project (no running services) is not reaped. All guarded by a.mu.
	watching   bool
	watchTok   *supervisor.Token // supervised watch loop; Remove cancels + drains it
	watchFiles []string          // absolute compose/env files to watch
	reload     *ReloadSpec       // how to re-exec the CLI on change
}

// New builds an agent. resolve is the connection resolver (nil uses the default
// clientconn-backed one; tests inject a fake).
func New(resolve ResolveFunc) *Agent {
	if resolve == nil {
		resolve = defaultResolve
	}
	return &Agent{
		resolve:  resolve,
		conns:    map[connKey]*connState{},
		projects: map[string]*projectEntry{},
		dockers:  map[string]*dockerFrontend{},
	}
}

// Serve runs the agent until SIGTERM/SIGINT or an idle-exit: it binds the control
// socket (via agentproc), supervises the accept loop, and drains on exit.
func (a *Agent) Serve(spec agentproc.Spec) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.ctx, a.stop = ctx, stop

	ln, cleanup, err := agentproc.Listen(spec)
	if err != nil {
		return err
	}
	defer cleanup()

	a.sup = supervisor.New(ctx, nil)
	a.sup.SetIdleHook(a.armIdle)
	a.sup.AddSystem("control", supervisor.ServiceFunc(func(ctx context.Context) error {
		return a.acceptLoop(ctx, ln)
	}), supervisor.Restart)

	// A freshly spawned agent starts idle (count 0, no transition), so arm the
	// linger now: if the spawning client never registers, we exit rather than leak.
	a.armIdle()

	<-ctx.Done()
	a.closeAllDockers() // stop held container sessions before draining the servers
	a.sup.Wait()
	a.closeAllConns()
	return nil
}

// closeAllDockers stops every docker frontend's container sessions on shutdown.
// The http servers themselves close via their supervised child's ctx cancel.
func (a *Agent) closeAllDockers() {
	a.mu.Lock()
	fes := make([]*dockerFrontend, 0, len(a.dockers))
	for _, fe := range a.dockers {
		fes = append(fes, fe)
	}
	a.mu.Unlock()
	for _, fe := range fes {
		fe.proxy.Close()
		_ = os.Remove(fe.socket)
	}
}

// acceptLoop accepts control connections until ctx is cancelled.
func (a *Agent) acceptLoop(ctx context.Context, ln net.Listener) error {
	go func() { <-ctx.Done(); _ = ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go a.handle(conn)
	}
}

// handle serves one control request, isolated so a panic can't take the agent
// down.
func (a *Agent) handle(conn net.Conn) {
	defer conn.Close()
	defer func() {
		if r := recover(); r != nil {
			logging.FromContext(a.ctx).ErrorContext(a.ctx, "panic handling control request", "component", "agent", "error", r)
		}
	}()
	// Bound the request read so a client that connects but never writes is reaped
	// instead of parking this goroutine (and its fd) in Decode forever.
	_ = conn.SetReadDeadline(time.Now().Add(requestReadTimeout))
	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	// Request read; dispatch (which can be slow — image pull) must not be bounded
	// by the read deadline, so clear it before replying.
	_ = conn.SetReadDeadline(time.Time{})

	resp, exit := a.dispatch(req)
	resp.Protocol = ProtocolVersion
	_ = json.NewEncoder(conn).Encode(resp)
	if exit {
		// Reply first, then stop the agent so the caller isn't cut off.
		conn.Close()
		a.stop()
	}
}

// dispatch handles one request and reports whether the agent should now exit
// (the stop action).
func (a *Agent) dispatch(req Request) (Response, bool) {
	switch req.Action {
	case "ping":
		return Response{OK: true}, false
	case "up":
		return a.doUp(req), false
	case "down":
		return a.doDown(req), false
	case "docker-serve":
		return a.doDockerServe(req), false
	case "docker-stop":
		return a.doDockerStop(req), false
	case "status":
		return Response{OK: true, Inventory: a.inventory()}, false
	case "stop":
		return Response{OK: true}, true
	default:
		return Response{OK: false, Error: "unknown action: " + req.Action}, false
	}
}

// doUp ensures the project exists on its server+conduit and starts each service.
func (a *Agent) doUp(req Request) Response {
	a.beginRequest()
	defer a.endRequest()
	if req.Project == "" {
		return Response{OK: false, Error: "up: missing project"}
	}
	entry, warnings, err := a.ensureProject(req.Project, req.Conn, req.Conduit.Runtime())
	if err != nil {
		return Response{OK: false, Error: err.Error(), Warnings: warnings}
	}
	// Keep the project (and its shared conn/conduit) pinned for the whole handler:
	// a concurrent `down` must not release the conn/conduit out from under the
	// Apply reconcile below, which uses them outside any lock.
	defer a.releaseProjectUse(entry)
	p := entry.project
	// A watched up is the COMPLETE agent-held desired set (ApplyExact prunes held
	// services absent from it, so a reload that dropped a service tears it down); a
	// plain up merges (Apply), preserving partial-`up SERVICE` semantics.
	var statuses map[string]string
	if req.Watch {
		statuses, err = p.ApplyExact(a.ctx, req.Services)
	} else {
		statuses, err = p.Apply(a.ctx, req.Services)
	}
	if err != nil {
		return Response{OK: false, Error: err.Error(), Running: p.Running()}
	}
	if req.Watch {
		a.armWatch(req.Project, req)
	}
	return Response{OK: true, Running: p.Running(), Forwards: p.Forwards(), Statuses: statuses, Banners: a.projectBanners(entry), Warnings: warnings}
}

// armWatch registers (or refreshes) the file watcher for a watched project. The
// watch descriptor (files + reload command) is updated in place every up so a
// reload re-exec's own up carries the latest set; the supervised watch loop is
// started only once (never a second goroutine). Called on the doUp path with the
// entry pinned (active>0), so the project cannot be reaped mid-arm.
func (a *Agent) armWatch(name string, req Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.projects[name]
	if e == nil {
		return
	}
	e.watchFiles = filewatch.Normalize(req.WatchFiles)
	e.reload = req.Reload
	if e.watching {
		return // already watching; descriptor refreshed above
	}
	e.watching = true
	e.watchTok = a.sup.Add("watch:"+name, supervisor.ServiceFunc(func(ctx context.Context) error {
		a.watchLoop(ctx, name)
		return nil
	}), supervisor.RemoveOnExit)
}

// watchLoop watches the project's compose/env files and re-execs the CLI to
// reload on each debounced change, until ctx is cancelled (the project's full
// `down`, or agent shutdown). It rebuilds the watcher each cycle from the entry's
// current watchFiles, so a reload that changed the file set is picked up. The
// re-exec is fire-and-forget: it reconnects to this agent and sends a fresh up,
// which reconciles (and refreshes this loop's descriptor). Because the re-exec
// writes none of the watched files, it never self-triggers.
func (a *Agent) watchLoop(ctx context.Context, name string) {
	for {
		a.mu.Lock()
		e := a.projects[name]
		var files []string
		var reload *ReloadSpec
		if e != nil {
			files = append([]string(nil), e.watchFiles...)
			reload = e.reload
		}
		a.mu.Unlock()
		if e == nil {
			return // project gone
		}
		w := filewatch.New(files, 0, 0)
		got := w.Wait(ctx)
		w.Close()
		if !got {
			return // ctx cancelled
		}
		a.reexecReload(name, reload)
	}
}

// reexecReload re-invokes the compose CLI to reload an edited watched project,
// in the original client's cwd/env (see ReloadSpec). Fire-and-forget: the child
// reconnects to this agent and sends a fresh up. Errors are logged, not fatal —
// the watcher stays armed for the next edit.
func (a *Agent) reexecReload(name string, reload *ReloadSpec) {
	log := logging.FromContext(a.ctx)
	if reload == nil || len(reload.Argv) == 0 {
		log.WarnContext(a.ctx, "watched project has no reload command; ignoring change", "component", "agent", "project", name)
		return
	}
	log.InfoContext(a.ctx, "compose files changed; reloading project", "component", "agent", "project", name)
	if _, err := daemonize.SpawnAt(reload.Argv, logPath(), reload.Dir, reload.Env); err != nil {
		log.ErrorContext(a.ctx, "reload re-exec failed", "component", "agent", "project", name, "error", err)
	}
}

// stopWatch cancels a project's watch loop (if any) and clears its watch state,
// so the entry is no longer pinned by watching and a subsequent removeProject can
// reap it. The supervisor Remove (which cancels the loop's ctx and waits for it)
// runs outside a.mu to avoid reentrancy, mirroring removeProject's token teardown.
func (a *Agent) stopWatch(name string) {
	a.mu.Lock()
	e := a.projects[name]
	var tok *supervisor.Token
	if e != nil {
		tok = e.watchTok
		e.watchTok = nil
		e.watching = false
		e.reload = nil
		e.watchFiles = nil
	}
	a.mu.Unlock()
	if tok != nil {
		a.sup.Remove(tok)
	}
}

// doDown tears down the named services (or the whole project) and releases the
// project (and its conn/conduit refs) when it empties.
func (a *Agent) doDown(req Request) Response {
	a.mu.Lock()
	entry := a.projects[req.Project]
	a.mu.Unlock()
	if entry == nil {
		return Response{OK: true} // nothing held for this project
	}
	// A full `down` (no named services) also stops watching, before the teardown
	// below, so a file edit racing the down cannot re-exec a reload against a
	// project being torn down. A partial `down SERVICE` leaves the watcher running.
	if len(req.Names) == 0 {
		a.stopWatch(req.Project)
	}
	entry.project.DownServices(req.Names)
	// removeProject is a no-op unless the project is now empty AND no `up` handler
	// still references it — so a concurrent `up` can't have its conn/conduit torn
	// down mid-StartService.
	a.removeProject(req.Project)
	return Response{OK: true, Running: entry.project.Running()}
}

// ensureProject returns the entry for name, creating it (and ensuring its shared
// conn + conduit) on first use, and marks it in-use for one `up` handler. The
// caller MUST pair the returned entry with releaseProjectUse.
//
// For an entry that already exists, it also reconciles the entry against the
// incoming request, which is where the identity/configuration line is drawn:
//
//   - The SERVER CONNECTION is part of the project's identity. The agent holds
//     deploy-attach sessions that ARE the client-side lifetime of workloads on one
//     specific server, and it can only attach, never migrate: re-attaching against a
//     second server would deploy a parallel copy there while the first copy keeps
//     running on the old server, unreachable through this project name (`compose
//     down` targets whichever server the CLI resolved). So a changed connection is a
//     conflict, refused with an error naming exactly what differs — never silently
//     kept, and never silently swapped.
//   - The CONDUIT CONFIGURATION (mode, listen address, resolution rules,
//     session-local scope, and the whole ingress configuration: mode, suffix, CA,
//     certificates, native controller) is configuration. It is entirely client-local
//     — listeners, aliases, and the ingress emulator — so it reconciles in place:
//     acquire the conduit the new configuration asks for, rebind the live project
//     onto it, and let the reconcile that follows move the exposures across. Putting
//     it in the identity instead would fork a second project entry under one compose
//     project name on every settings tweak, so both entries would hold sessions for
//     the same services and neither `down` nor the idle reaper could name the old
//     one; the whole ingress configuration is therefore deliberately configuration,
//     not identity.
//
// The refcount order is acquire-then-release, and the release of the OLD conduit is
// deferred to releaseProjectUse (after the caller's reconcile). Releasing first
// would close a conduit that another frontend still shares whenever the new
// configuration keys back onto it, and releasing before the reconcile would close
// the old conduit out from under registrations that have not moved yet.
func (a *Agent) ensureProject(name string, spec ConnSpec, egCfg ConduitCfg) (*projectEntry, []string, error) {
	a.mu.Lock()
	if e := a.projects[name]; e != nil {
		if e.conn.key != spec.key() {
			a.mu.Unlock()
			return nil, nil, fmt.Errorf("compose project %q is already held by the background agent against a different server connection (%s); run `cornus compose down` before changing the server or context", name, connDiff(e.conn.key, spec.key()))
		}
		e.active++ // pins the entry against removeProject for the rest of this call
		want := conduitKeyOf(egCfg, name)
		if e.egKey == want {
			a.mu.Unlock()
			return e, nil, nil
		}
		a.mu.Unlock()

		// Serialize this entry's rebinds (a.mu is not held, and nothing takes rebindMu
		// under a.mu, so there is no lock cycle), then re-check under a.mu: a
		// concurrent `up` may have already moved the project onto the same conduit.
		e.rebindMu.Lock()
		defer e.rebindMu.Unlock()
		a.mu.Lock()
		if e.egKey == want {
			a.mu.Unlock()
			return e, nil, nil
		}
		conduit, err := a.rebindProjectLocked(e, name, spec, egCfg, want)
		a.mu.Unlock()
		if err != nil {
			a.releaseProjectUse(e)
			return nil, nil, fmt.Errorf("reconcile compose project %q onto its new conduit settings: %w", name, err)
		}
		// Outside a.mu: swap the project's conduit. It only marks the live exposures
		// dirty (it blocks on nothing but the project's reconcile lock); the actual
		// re-registration is done by the caller's Apply, which is the reconcile that
		// drives every dimension to its desired state.
		e.project.Rebind(conduit)
		logging.FromContext(a.ctx).InfoContext(a.ctx, "conduit settings changed; reconciling project onto the new conduit", "component", "agent", "project", name)
		return e, []string{fmt.Sprintf("compose project %q was held with different conduit settings; the background agent reconciled it onto the new conduit (previous listeners, aliases, and ingress registrations were withdrawn)", name)}, nil
	}
	defer a.mu.Unlock()
	cs, err := a.ensureConnLocked(spec)
	if err != nil {
		return nil, nil, err
	}
	es, err := a.ensureConduitLocked(cs, egCfg, name)
	if err != nil {
		a.releaseConnLocked(cs)
		return nil, nil, err
	}
	p := NewProject(cs.client, es.eg)
	// A liveness token so the supervisor's idle hook fires when the last project
	// (and docker frontend) is gone.
	tok := a.sup.Add("project:"+name, supervisor.ServiceFunc(func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}), supervisor.RemoveOnExit)
	e := &projectEntry{project: p, conn: cs, egKey: conduitKeyOf(egCfg, name), banners: es.eg.Banner(), token: tok, active: 1}
	a.projects[name] = e
	return e, nil, nil
}

// rebindProjectLocked acquires the conduit egCfg asks for and moves the entry onto
// it, returning the new conduit. The old conn/conduit refs are queued on the entry
// rather than dropped here (see projectEntry.pendingRelease). Caller holds a.mu and
// has already taken the entry's `up` reference.
func (a *Agent) rebindProjectLocked(e *projectEntry, name string, spec ConnSpec, egCfg ConduitCfg, want conduitKey) (clientconduit.Conduit, error) {
	// Same connection key, so this only bumps the shared connState's refcount; it
	// exists to keep every acquire paired with exactly one release below.
	cs, err := a.ensureConnLocked(spec)
	if err != nil {
		return nil, err
	}
	es, err := a.ensureConduitLocked(cs, egCfg, name)
	if err != nil {
		a.releaseConnLocked(cs)
		return nil, err
	}
	oldCs, oldKey := e.conn, e.egKey
	e.conn, e.egKey = cs, want
	e.banners = es.eg.Banner()
	e.pendingRelease = append(e.pendingRelease, func() {
		a.releaseConduitLocked(oldCs, oldKey)
		a.releaseConnLocked(oldCs)
	})
	return es.eg, nil
}

// connDiff names the connection fields that differ, for the identity-conflict
// error. The token is never echoed.
func connDiff(held, want connKey) string {
	var parts []string
	add := func(field, a, b string) {
		if a != b {
			parts = append(parts, fmt.Sprintf("%s %q -> %q", field, a, b))
		}
	}
	add("server", held.Server, want.Server)
	add("context", held.Context, want.Context)
	add("config file", held.ConfigFile, want.ConfigFile)
	if held.ViaServer != want.ViaServer {
		parts = append(parts, fmt.Sprintf("via-server %v -> %v", held.ViaServer, want.ViaServer))
	}
	if held.Token != want.Token {
		parts = append(parts, "token changed")
	}
	if len(parts) == 0 {
		return "connection changed"
	}
	return strings.Join(parts, ", ")
}

// checkConduitBindConflictLocked refuses to start a second SOCKS5 proxy on an
// address this agent already has one on, and says WHO holds it.
//
// Two conduits with different identities and the same bind address cannot both
// exist: clientconduit.Start would fail on the listen. Left to itself that surfaces
// as "bind: address already in use", which names neither the frontend in the way
// nor the settings that made this a second conduit instead of a shared one — and
// the person reading it is usually one command removed from the one that bound it.
// The pre-flight is only possible for holders inside this agent; a foreground
// `cornus socks5` is invisible here, and the raw bind error remains the backstop.
//
// Caller holds a.mu. key is the canonical key cfg would take.
func (a *Agent) checkConduitBindConflictLocked(cs *connState, cfg ConduitCfg, key conduitKey) error {
	if cfg.Mode != clientconduit.ModeSocks5 {
		return nil
	}
	addr := cfg.Socks5Listen
	if addr == "" || strings.HasSuffix(addr, ":0") {
		return nil // an ephemeral bind takes whatever is free; it cannot collide
	}
	for k, es := range cs.conduit {
		if k == key || es.cfg.Mode != clientconduit.ModeSocks5 || es.cfg.Socks5Listen != addr {
			continue
		}
		return fmt.Errorf("the SOCKS5 conduit at %s is already held by %s with different settings (%s); a second proxy cannot bind the same address. Give this session matching settings, a different address (--conduit socks5://.shared:PORT), or its own private proxy (--conduit socks5://127.0.0.1:0)",
			addr, a.conduitHolderLocked(cs, k), conduitDiff(es.cfg, cfg))
	}
	return nil
}

// conduitHolderLocked describes the frontend riding conduit key, for the bind
// conflict above. It reads the existing per-frontend keys rather than keeping a
// reverse index, so there is no second source of truth to drift. Caller holds a.mu.
func (a *Agent) conduitHolderLocked(cs *connState, key conduitKey) string {
	var holders []string
	for name, e := range a.projects {
		if e.conn == cs && e.egKey == key {
			holders = append(holders, fmt.Sprintf("compose project %q", name))
		}
	}
	for sock, d := range a.dockers {
		if d.conn == cs && d.egKey == key {
			holders = append(holders, fmt.Sprintf("the docker frontend on %s", sock))
		}
	}
	if len(holders) == 0 {
		return "another session in this background agent"
	}
	sort.Strings(holders) // map order is not a contract, and this text is asserted on
	return strings.Join(holders, ", ")
}

// conduitDiff names the conduit settings that differ, for the bind-conflict error
// above. It is the conduit counterpart of connDiff, and reports the fields a
// caller can actually act on rather than the whole Identity.
func conduitDiff(held, want ConduitCfg) string {
	var parts []string
	add := func(field, a, b string) {
		if a != b {
			parts = append(parts, fmt.Sprintf("%s %q -> %q", field, a, b))
		}
	}
	add("service-host suffix", held.Socks5Suffix, want.Socks5Suffix)
	add("ingress mode", ingressModeOf(held), ingressModeOf(want))
	add("ingress domain", ingressDomainOf(held), ingressDomainOf(want))
	if len(held.Socks5Resolve) != len(want.Socks5Resolve) {
		parts = append(parts, "resolution rules changed")
	}
	if len(parts) == 0 {
		return "conduit settings differ"
	}
	return strings.Join(parts, ", ")
}

func ingressModeOf(cfg ConduitCfg) string {
	if cfg.Ingress == nil {
		return ""
	}
	return string(cfg.Ingress.Mode)
}

func ingressDomainOf(cfg ConduitCfg) string {
	if cfg.Ingress == nil {
		return ""
	}
	return cfg.Ingress.SuffixDomain
}

// releaseProjectUse drops one in-flight `up` reference taken by ensureProject and
// settles any refcount releases that `up` deferred (a conduit the project was
// rebound off, released only now that the reconcile has moved every registration
// into the new conduit).
func (a *Agent) releaseProjectUse(entry *projectEntry) {
	a.mu.Lock()
	entry.active--
	pending := entry.pendingRelease
	entry.pendingRelease = nil
	for _, release := range pending {
		release()
	}
	a.mu.Unlock()
}

// projectBanners snapshots an entry's conduit banner. A rebind replaces it, so the
// read must be locked.
func (a *Agent) projectBanners(e *projectEntry) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), e.banners...)
}

// removeProject releases the project and its conn/conduit refs, but only when the
// project is empty and no `up` handler still references it. Both conditions are
// checked under a.mu so a concurrent ensureProject (which bumps active under the
// same lock) can never have its conn/conduit closed while StartService is still
// dialing over them.
func (a *Agent) removeProject(name string) {
	a.mu.Lock()
	entry := a.projects[name]
	if entry == nil {
		a.mu.Unlock()
		return
	}
	if entry.active != 0 || len(entry.project.Running()) != 0 || entry.watching {
		a.mu.Unlock() // still in use, non-empty, or watching — keep it
		return
	}
	delete(a.projects, name)
	a.releaseConduitLocked(entry.conn, entry.egKey)
	a.releaseConnLocked(entry.conn)
	tok := entry.token
	a.mu.Unlock()
	// Remove outside the lock: the token's Serve is parked on ctx and returns at
	// once, and Remove waits for it — no reentrancy on a.mu.
	a.sup.Remove(tok)
}

// ensureConnLocked resolves (or reuses) the connection for spec and increments
// its refcount. Caller holds a.mu.
func (a *Agent) ensureConnLocked(spec ConnSpec) (*connState, error) {
	key := spec.key()
	if cs := a.conns[key]; cs != nil {
		cs.refs++
		return cs, nil
	}
	cn, err := a.resolve(spec)
	if err != nil {
		return nil, err
	}
	cs := &connState{
		key:     key,
		conn:    cn,
		client:  cn.Client(),
		dialer:  cn.Dialer(spec.ViaServer),
		conduit: map[conduitKey]*conduitState{},
		refs:    1,
	}
	a.conns[key] = cs
	return cs, nil
}

func (a *Agent) releaseConnLocked(cs *connState) {
	cs.refs--
	if cs.refs <= 0 {
		delete(a.conns, cs.key)
		if cs.conn != nil && cs.conn.Cleanup != nil {
			cs.conn.Cleanup() // tear down any svcforward tunnel
		}
	}
}

// ensureConduitLocked builds (or reuses) the conduit for cfg within cs and
// increments its refcount. session identifies the requester (project name or
// docker socket); a session-local conduit keys on it so it is not shared. Caller
// holds a.mu.
// registry is the conduit rendezvous, defaulting to the per-user one beside the
// agent's own state.
func (a *Agent) registry() *conduithost.Registry {
	if a.conduitRegistry != nil {
		return a.conduitRegistry
	}
	return ConduitRegistry()
}

func (a *Agent) ensureConduitLocked(cs *connState, cfg ConduitCfg, session string) (*conduitState, error) {
	cfg = canonicalConduitCfg(cfg)
	key := conduitKeyOf(cfg, session)
	if es := cs.conduit[key]; es != nil {
		es.refs++
		return es, nil
	}
	if err := a.checkConduitBindConflictLocked(cs, cfg, key); err != nil {
		return nil, err
	}
	// Bind through the rendezvous, so this conduit is joinable by other processes
	// rather than being a map entry only this agent can see. A second requester for
	// an address it covers joins it instead of forking a proxy that cannot bind.
	eg, err := clientconduit.Start(a.ctx, cs.dialer, cfg, clientconduit.WithRendezvous(a.registry()))
	if err != nil {
		return nil, err
	}
	log := logging.FromContext(a.ctx)
	for _, line := range eg.Banner() {
		log.InfoContext(a.ctx, line)
	}
	es := &conduitState{eg: eg, cfg: cfg, refs: 1}
	cs.conduit[key] = es
	return es, nil
}

func (a *Agent) releaseConduitLocked(cs *connState, key conduitKey) {
	es := cs.conduit[key]
	if es == nil {
		return
	}
	es.refs--
	if es.refs <= 0 {
		delete(cs.conduit, key)
		es.eg.Close()
	}
}

// inventory snapshots the agent's state for the status action.
func (a *Agent) inventory() *Inventory {
	a.mu.Lock()
	defer a.mu.Unlock()
	inv := &Inventory{Projects: map[string][]string{}}
	for name, e := range a.projects {
		inv.Projects[name] = e.project.Running()
	}
	for sock := range a.dockers {
		inv.Dockers = append(inv.Dockers, sock)
	}
	sort.Strings(inv.Dockers)
	seenBanner := map[string]bool{}
	for key := range a.conns {
		s := key.Server
		if s == "" {
			s = "(profile)"
		}
		inv.Servers = append(inv.Servers, s)
	}
	// Conduit banners (the SOCKS5 proxy listen line), deduplicated across shared
	// conduits. Previously declared but never populated, so the UI's conduit panel
	// was always empty.
	//
	// The same walk collects the ingress settings each conduit runs under: they are
	// per-conduit facts with the same sharing and the same duplication, so they
	// deduplicate on the same pass rather than on a second one that could disagree
	// with this one about which conduits are live.
	seenIngress := map[string]bool{}
	for _, cs := range a.conns {
		for _, es := range cs.conduit {
			for _, b := range es.eg.Banner() {
				if !seenBanner[b] {
					seenBanner[b] = true
					inv.Banners = append(inv.Banners, b)
				}
			}
			ing := agentIngressOf(es)
			if ing == nil {
				continue
			}
			// Keyed on the marshalled entry, not on the mode: two conduits can both be
			// emulating and still differ in domain or in the certificates they present,
			// and collapsing those would report one client setting where there are two.
			k, err := json.Marshal(ing)
			if err != nil || seenIngress[string(k)] {
				continue
			}
			seenIngress[string(k)] = true
			inv.Ingress = append(inv.Ingress, *ing)
		}
	}
	sort.Strings(inv.Servers)
	sort.Strings(inv.Banners)
	// Sorted by mode then domain, so a status reply is stable across map iteration
	// order — two consecutive polls of an unchanged agent must not reorder the UI.
	sort.Slice(inv.Ingress, func(i, j int) bool {
		if inv.Ingress[i].Mode != inv.Ingress[j].Mode {
			return inv.Ingress[i].Mode < inv.Ingress[j].Mode
		}
		return inv.Ingress[i].Domain < inv.Ingress[j].Domain
	})
	return inv
}

// agentIngressOf describes one conduit's ingress settings, or nil when it routes
// no ingress. "Routes none" is reported as an ABSENT entry rather than an entry
// with an empty mode: a reader counting entries then learns exactly what it wants
// to know, and no consumer has to decide what a mode-less entry claims.
func agentIngressOf(es *conduitState) *AgentIngress {
	ing := es.cfg.Ingress
	if ing == nil || ing.Mode == clientconduit.IngressOff {
		return nil
	}
	out := &AgentIngress{
		Mode:   string(ing.Mode),
		Domain: ing.SuffixDomain,
		// Asked of the LIVE conduit rather than derived from the config: what a
		// browser must trust is the certificate the proxy actually presents, and in
		// the common case (no --ingress-emulate-ca) that is a session CA the conduit
		// generated for itself, which appears in no configuration.
		Trust: es.eg.CAInfo(),
	}
	if c := ing.Controller; c != nil {
		out.Controller = &AgentIngressController{
			KubeContext: c.KubeContext,
			Namespace:   c.Namespace,
			Service:     c.Service,
			HTTPPort:    c.HTTPPort,
			HTTPSPort:   c.HTTPSPort,
		}
	}
	return out
}

// closeAllConns tears down every connection's conduit + svcforward on shutdown.
func (a *Agent) closeAllConns() {
	a.mu.Lock()
	conns := make([]*connState, 0, len(a.conns))
	for _, cs := range a.conns {
		conns = append(conns, cs)
	}
	a.conns = map[connKey]*connState{}
	a.projects = map[string]*projectEntry{}
	a.dockers = map[string]*dockerFrontend{} // so a docker-stop racing shutdown finds nil, not a stale ref
	a.mu.Unlock()
	for _, cs := range conns {
		for _, es := range cs.conduit {
			es.eg.Close()
		}
		if cs.conn != nil && cs.conn.Cleanup != nil {
			cs.conn.Cleanup()
		}
	}
}

// beginRequest / endRequest bracket a work-registering request (up, docker-serve)
// so the idle-exit timer cannot fire between "resolved the connection" and
// "registered the counted child" — a window that (with a slow kube resolve /
// svcforward) can exceed the idle linger and kill the agent mid-request.
func (a *Agent) beginRequest() {
	a.mu.Lock()
	a.inflight++
	a.mu.Unlock()
}

func (a *Agent) endRequest() {
	a.mu.Lock()
	a.inflight--
	a.mu.Unlock()
	a.armIdle() // re-check idleness now the request has settled
}

// armIdle (re)starts the idle-exit timer.
func (a *Agent) armIdle() {
	a.idleMu.Lock()
	defer a.idleMu.Unlock()
	if a.idleTimer != nil {
		a.idleTimer.Stop()
	}
	a.idleTimer = time.AfterFunc(idleLinger, a.idleCheck)
}

// idleCheck exits the agent only if it holds no work and none is in flight.
func (a *Agent) idleCheck() {
	a.mu.Lock()
	idle := a.inflight == 0 && len(a.projects) == 0 && len(a.dockers) == 0
	a.mu.Unlock()
	if idle {
		a.stop()
	}
}
