package composecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"cornus/cmd/cornus/internal/clientagent"
	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
	"cornus/cmd/cornus/internal/egressflags"
	"cornus/cmd/cornus/internal/lineage"
	"cornus/cmd/cornus/internal/telemetryflags"
	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/clientconduit"
	"cornus/pkg/clientproxy"
	"cornus/pkg/compose"
	"cornus/pkg/socks5"

	"golang.org/x/sync/errgroup"
)

// baseContext is the invocation's root-span context, set once by package main
// (SetBaseContext) before dispatch so every compose subcommand's foreground
// client calls hang off the one per-invocation trace (client -> server ->
// caretaker). It mirrors Version: the compose subpackage cannot import package
// main, so main hands it in. nil (a Run method exercised directly in a test)
// falls back to context.Background().
var baseContext context.Context

// SetBaseContext sets the root-span context the compose subcommands derive their
// signal-cancellable contexts from. Called once by package main, like Version.
func SetBaseContext(ctx context.Context) { baseContext = ctx }

// rootContext returns the invocation root context, or context.Background() when
// unset (direct test call).
func rootContext() context.Context {
	if baseContext != nil {
		return baseContext
	}
	return context.Background()
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(rootContext(), os.Interrupt, syscall.SIGTERM)
}

// parseBuildArgs turns --build-arg entries into a map. "KEY=VALUE" sets KEY to
// VALUE; a bare "KEY" takes its value from the process environment (docker
// --build-arg parity). An empty name is rejected.
func parseBuildArgs(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		key, val, has := strings.Cut(e, "=")
		if key == "" {
			return nil, fmt.Errorf("invalid --build-arg %q: empty name", e)
		}
		if !has {
			val = os.Getenv(key)
		}
		out[key] = val
	}
	return out, nil
}

// resolveBuildSSH resolves a service's SSH agent forwarding specs — the compose
// build.ssh entries with the command's --ssh entries merged over them — into an
// id->socket map. Each entry is a bare id ("default") or "id[=socket]"; a missing
// socket falls back to $SSH_AUTH_SOCK. A --ssh entry overrides a compose-file
// entry with the same id. Returns nil when no SSH forwarding is requested.
func resolveBuildSSH(fileSSH, cliSSH []string) (map[string]string, error) {
	var out map[string]string
	add := func(specs []string) error {
		for _, it := range specs {
			id, sock, has := strings.Cut(it, "=")
			if id == "" {
				id = "default"
			}
			if !has || sock == "" {
				sock = os.Getenv("SSH_AUTH_SOCK")
			}
			if sock == "" {
				return fmt.Errorf("ssh %q: no agent socket (set SSH_AUTH_SOCK or id=socket)", it)
			}
			if out == nil {
				out = map[string]string{}
			}
			out[id] = sock
		}
		return nil
	}
	if err := add(fileSSH); err != nil {
		return nil, err
	}
	if err := add(cliSSH); err != nil {
		return nil, err
	}
	return out, nil
}

// UpCmd creates and starts services (build if needed, then deploy).
type UpCmd struct {
	Services       []string             `kong:"arg,optional,help='Services to bring up (default: all).'"`
	Build          bool                 `kong:"name='build',help='Build images before starting (build services are always built).'"`
	SSH            []string             `kong:"name='ssh',sep='none',help='SSH agent forwarding for builds: \"default\" or \"id[=socket]\" (RUN --mount=type=ssh), repeatable. Merges over each service build.ssh.'"`
	Detach         bool                 `kong:"name='detach',short='d',help='Detached mode: deploy, hand any client-local mounts and forwarded ports to a background helper, and return immediately. The default is foreground (stream logs and hold mounts/forwards until Ctrl-C), like docker compose up.'"`
	NoForwardPorts bool                 `kong:"name='no-forward-ports',help='Do not auto-forward published service ports to local listeners.'"`
	NoAttach       bool                 `kong:"name='no-attach',help='Do not stream service logs in the foreground (still holds mounts/forwards until Ctrl-C).'"`
	NoLogPrefix    bool                 `kong:"name='no-log-prefix',help='Do not prefix streamed log lines with the service name.'"`
	Conduit        string               `kong:"name='conduit',default='',help='Session conduit mode: port-forward (per-port local listeners, the default) or socks5 (one split-tunnel proxy reaching services by name). A bare word sets only the mode and keeps the profile SOCKS5 listen/suffix; a socks5://host:port[?suffix=SUFFIX] URL also overrides the bind address and service-host suffix (socks5h:// is a synonym). Takes precedence over CORNUS_CONDUIT and the profile mode. --no-forward-ports disables conduit entirely.'"`
	IngressConduit string               `kong:"name='ingress-conduit',default='',help='Reach a service ingress (x-cornus-ingress) through the SOCKS5 conduit: native (tunnel to the real cluster ingress controller) or emulate (a client-side reverse proxy with a generated cert), or off. Requires --conduit socks5. Takes precedence over CORNUS_INGRESS_CONDUIT and the profile.'"`
	Egress         egressflags.Flags    `kong:"embed"`
	Telemetry      telemetryflags.Flags `kong:"embed"`
}

// conduitCfg resolves the session conduit config for this up, folding
// --no-forward-ports into ModeNone and the --conduit override into the profile
// precedence.
func (c *UpCmd) conduitCfg(rt *runtime) clientconduit.Config {
	mode := c.Conduit
	if c.NoForwardPorts {
		mode = clientconduit.ModeNone
	}
	return rt.conduitConfig(mode)
}

// Run brings services up in dependency order. Services with client-local bind
// mounts are streamed over 9P via the deploy-attach path, which must stay
// connected for the container's lifetime: without -d, `up` runs those in the
// foreground until Ctrl-C; with -d it re-execs a detached background helper that
// holds the mounts (stopped by `down`). Mount-free services deploy
// fire-and-forget either way, though a foreground `up` removes the ones it
// created when it exits (Ctrl-C), like `docker compose up`; `up -d` leaves them
// running (removed by `down`). Published ports are auto-forwarded to local
// listeners with the same split: foreground `up` holds the forwards until
// Ctrl-C, `up -d` hands them to the background helper. Foreground `up` also
// attaches to the services' container logs and streams them (prefixed by
// service name) until Ctrl-C, like `docker compose up`; --no-attach opts out.
func (c *UpCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	rt, err := cli.load(r, d)
	if err != nil {
		return err
	}
	defer rt.cleanup()
	names, err := rt.selectServices(c.Services)
	if err != nil {
		return err
	}

	// Workload lineage: every service in this project shares one origin — the
	// project name and the client host/user/dir/git the compose command ran in.
	// The server stamps the authenticated Subject.
	projectOrigin := lineage.Collect(rt.baseDir)
	projectOrigin.Project = rt.projectName

	// Client-side egress: apply the --egress flags (overriding any compose `egress:`
	// block) to each selected service, then resolve env-mode proxy settings on the
	// client. Services without egress are unaffected (both calls are no-ops).
	for _, n := range names {
		p := rt.plans[n]
		if err := c.Egress.Apply(&p.Spec); err != nil {
			return fmt.Errorf("service %s: %w", n, err)
		}
		// Workload telemetry: --telemetry-* flags apply project-wide, overriding any
		// per-service compose `x-cornus-telemetry:` block. A no-op when unset.
		if err := c.Telemetry.Apply(&p.Spec); err != nil {
			return fmt.Errorf("service %s: %w", n, err)
		}
		if err := clientproxy.ApplyEgressEnv(&p.Spec); err != nil {
			return fmt.Errorf("service %s: %w", n, err)
		}
		origin := *projectOrigin
		p.Spec.Origin = &origin
		rt.plans[n] = p
	}

	// A devcontainer initializeCommand runs on the host before any container is
	// created (a no-op for plain Compose / when unset).
	if rt.initialize != nil {
		ictx, istop := signalContext()
		err := runInitialize(ictx, rt.baseDir, rt.initialize)
		istop()
		if err != nil {
			return err
		}
	}

	cfg := c.conduitCfg(rt)
	specs := make([]api.DeploySpec, 0, len(names))
	for _, n := range names {
		specs = append(specs, rt.plans[n].Spec)
	}
	if c.Detach && needsBackgroundAgent(specs, cfg.Mode) {
		return c.upDetached(cli, rt, cfg, names)
	}

	ctx, stop := signalContext()
	defer stop()
	return c.runForeground(ctx, rt, names)
}

// shutdownExit decides how a foreground `up` returns when its startup deploy
// loop bails out. When the context was cancelled (a user Ctrl-C / SIGINT), the
// up is a clean shutdown: it returns nil and asks the caller to remove the
// mount-free deployments it created (remove=true), so an interrupt during
// startup exits 0 the same way one during the steady-state hold does — on every
// backend, not depending on whether the cancel raced the loop reaching its
// hold. With the context still live the genuine error propagates unchanged and
// nothing is removed. Kept as a pure function so the exit-code contract is
// unit-testable without a live client.
func shutdownExit(genuine, ctxErr error) (err error, remove bool) {
	if ctxErr != nil {
		return nil, true
	}
	return genuine, false
}

// runForeground deploys the selected services, holding open a deploy-attach
// session for each service with client-local bind mounts — and a local
// port-forward group for each service with published ports — until ctx is
// cancelled, then tearing those down. Mount-free services deploy
// fire-and-forget, but a foreground exit (Ctrl-C, or an external `down` removing
// them) then removes them too, so terminating `up` stops everything it brought
// up like `docker compose up`.
func (c *UpCmd) runForeground(ctx context.Context, rt *runtime, names []string) error {
	cfg := c.conduitCfg(rt)
	rt.resolveIngress(ctx, &cfg, c.IngressConduit)
	conduit, err := clientconduit.Start(ctx, rt.forwardDialer, cfg)
	if err != nil {
		return err
	}
	d := rt.driver()
	for _, line := range conduit.Banner() {
		d.Info("%s", line)
	}
	// The reusable client-side session engine — the same Project the background
	// agent runs, in-process here. It holds each mounted service's 9P deploy-attach
	// session and every service's conduit exposure; the conduit stays owned by this
	// function (the engine only registers/withdraws through it), so teardown closes
	// the engine's held resources and then the conduit. Mount-free deployments are
	// created fire-and-forget above; the engine only withdraws their exposure, so a
	// foreground exit removes them separately via removeDeployments below.
	project := clientagent.NewProject(rt.client, conduit)
	teardown := func() {
		project.Close()
		conduit.Close()
	}
	// Compose service names of the mount-free deployments this foreground `up`
	// created. They deploy fire-and-forget (the engine never holds them), so on a
	// genuine foreground exit (Ctrl-C, or an external `down` removing them) they
	// are deleted explicitly below — otherwise terminating `up` would leave them
	// running. Mounted services need no tracking: the engine removes them
	// server-side when project.Close drops their deploy-attach hold. Populated
	// once, after the deploy loop below, in dependency (`names`) order — not
	// completion order — so removeDeployments' reverse-selection-order teardown
	// contract is unaffected by the loop now running concurrently.
	var mountFree []string
	mountedCount := 0
	// finish tears down the held sessions and computes this foreground up's exit
	// for a startup-loop return. A user Ctrl-C (ctx cancelled) is a clean
	// shutdown, not a failure: it removes the mount-free deployments this up has
	// created so far — exactly what the steady-state hold-exit path below does —
	// and returns nil, so a SIGINT during the startup deploy loop exits 0 just
	// like one during the hold. Doing this deterministically (rather than
	// returning ctx.Err()) fixes an exit-code race that surfaced only on the
	// slower kubernetes reconcile: there the client could still be in this loop
	// when the interactive Ctrl-C arrived, so it returned bare context.Canceled
	// (exit 1) where docker/containerd had already reached the hold (exit 0). A
	// genuine failure (err set with ctx still live) propagates as-is after
	// teardown.
	finish := func(genuine error) error {
		teardown()
		err, remove := shutdownExit(genuine, ctx.Err())
		if remove {
			removeDeployments(rt.client, rt.plans, mountFree, d)
		}
		return err
	}
	// expose applies a ready service to the engine and prints its bound local
	// forwards. name is the compose service name (e.g. "web") registered as a short
	// alias for the deployment (spec.Name, e.g. "demo-web"). forwardOnly marks a
	// mount-free service already deployed above (the engine only exposes it); a
	// mounted service opens its deploy-attach hold here, blocking until ready (a
	// Ctrl-C during a slow pre-ready attach aborts via ctx). project.Apply itself
	// fully serializes concurrent calls internally (one project-wide reconcile
	// mutex), so concurrent services calling expose gain independence in their
	// depends_on wait and reconcile-report phases below, but still queue here one
	// at a time — a client-side-engine constraint this change does not lift.
	expose := func(ctx context.Context, name string, spec api.DeploySpec, forwardOnly bool) error {
		svc := clientagent.Service{Name: name, Spec: spec, ForwardPorts: len(spec.Ports) > 0, ForwardOnly: forwardOnly}
		if _, err := project.Apply(ctx, []clientagent.Service{svc}); err != nil {
			return err
		}
		for _, f := range project.Forwards()[name] {
			d.Event(svcEvent(name, "forwarding", f))
		}
		return nil
	}

	// The set of services this up is bringing up, so a dependency wait ignores
	// depends_on targets outside the selection (they are never deployed here).
	selected := make(map[string]bool, len(names))
	for _, n := range names {
		selected[n] = true
	}
	// One-shot services depended on with service_completed_successfully never
	// reach the Running gate; deploy them with a completion-aware wait instead so
	// their own iteration doesn't burn the full reconcile timeout (see
	// completionServices / reportCompletion).
	completion := completionServices(rt, selected)

	// Build every selected service with a build section up front, concurrently
	// and deduplicated (see buildServices), instead of one at a time inside the
	// deploy loop below. A build has no runtime dependency on another service
	// being up, so gating it behind the deploy loop's depends_on waits only
	// delayed unrelated services' images — and, with each iteration also
	// blocking on the previous service's build, made a later service's very
	// first appearance in `cornus compose ps` (its POST /.cornus/v1/deploy call)
	// wait on every earlier service's build finishing first.
	builtTags, err := rt.buildServices(ctx, buildPlans(rt, names), c.SSH, buildOverrides{})
	if err != nil {
		return finish(err)
	}

	// Deploy + reconcile every selected service CONCURRENTLY instead of one at a
	// time: each service's own goroutine still calls waitForDependencies first,
	// which polls its depends_on targets' LIVE status independently of any other
	// service's loop iteration — so launching every service at once and letting
	// each block on its own dependency condition (started/healthy/completed) IS
	// how the depends_on topology gets resolved here, without a separate
	// up-front graph/level computation: an independent service's wait returns
	// immediately (nothing to wait for) and it proceeds at once, while a
	// dependent's wait naturally keeps polling until its dependency (running
	// concurrently in its own goroutine) actually satisfies the condition.
	// Previously every service's full deploy+reconcile (up to the 120s timeout)
	// blocked the NEXT service's goroutine from even starting its own wait, so
	// unrelated services serialized behind each other for no dependency reason —
	// the dominant cause of `cornus compose ps` appearing to lag behind the whole
	// `up` finishing.
	//
	// prog and hookLines are shared across every concurrent service: reportReconcile/
	// reportCompletion would otherwise each start their own live bubbletea program
	// and race for terminal ownership, and hook output would otherwise interleave
	// raw bytes on stdout (see reportReconcile's and runServiceHooks' doc comments).
	prog := d.Progress()
	defer prog.Stop()
	hookLines := d.LineGroup()

	type serviceOutcome struct {
		mountFree bool
		mounted   bool
	}
	outcomes := make([]serviceOutcome, len(names))

	grp, gctx := errgroup.WithContext(ctx)
	for i, name := range names {
		i, name := i, name
		grp.Go(func() error {
			return suppressCascaded(gctx, func() error {
				// Honor depends_on conditions: block until each selected dependency has
				// reached its condition (started/healthy/completed) before starting this
				// service. A required dependency's timeout aborts the up.
				if err := waitForDependencies(gctx, rt, rt.client, name, selected, d, reconcilePollInterval, reconcileWaitTimeout); err != nil {
					return err
				}
				plan := rt.plans[name]
				spec := plan.Spec
				if plan.Build != nil {
					spec.Image = builtTags[name]
				}
				if spec.Image == "" {
					return fmt.Errorf("service %q has neither image nor build", name)
				}

				// Mount-free services deploy fire-and-forget UNLESS they need a live relay
				// session: a client-side-egress relay mode (proxy/transparent) routes the
				// workload's traffic back through this client, so — like a client-local mount
				// — the deploy must hold a deploy-attach session (the session-holding branch
				// below), not a stateless POST.
				if len(spec.Mounts) == 0 && !spec.Egress.NeedsRelay() {
					if _, err := rt.client.Deploy(gctx, spec); err != nil {
						return fmt.Errorf("deploy %s: %w", name, err)
					}
					// Deployed: track it so a foreground exit tears it down (collected into
					// mountFree, in dependency order, after every goroutine finishes below).
					outcomes[i].mountFree = true
					// Deploy returns once the backend has created the objects; poll the
					// cluster-side reconcile and report each container's state changes
					// (like `docker compose up`) until they are running — or, for a one-shot
					// completion service, until it terminates (it never reaches Running).
					var st api.DeployStatus
					if completion[name] {
						st = reportCompletion(gctx, rt.client, name, spec.Name, d, prog, reconcilePollInterval, reconcileWaitTimeout)
					} else {
						st = reportReconcile(gctx, rt.client, name, spec.Name, d, prog, reconcilePollInterval, reconcileWaitTimeout)
					}
					if gctx.Err() != nil {
						return nil
					}
					if len(st.Instances) == 0 {
						// Never came up, or removed out from under us by an external `down`
						// while we waited (reportReconcile stops waiting on that). Don't claim
						// it came up; the foreground hold below will see all workloads gone and
						// exit, so there is nothing to expose or run hooks against.
						d.Event(svcEvent(name, "removed", ""))
						return nil
					}
					d.Event(svcUp(name, st))
					// Exposure of an already-deployed service is best-effort (a local bind
					// conflict shouldn't sink the whole up): log and carry on, as before.
					if err := expose(gctx, name, spec, true); err != nil {
						d.Error("port-forward setup failed for %s: %v", name, err)
					}
					out := hookLines.Writer(d.Out(), name+" | ")
					defer out.Close()
					if err := rt.runServiceHooks(gctx, name, out); err != nil {
						return fmt.Errorf("lifecycle %s: %w", name, err)
					}
					return nil
				}

				// Client-local bind mounts: the engine deploy-attaches, streaming the local
				// dirs over 9P for the container's lifetime, and blocks until ready. A
				// completion service (one-shot depended on with
				// service_completed_successfully) that ALSO has client-local mounts is not
				// specially handled here — it goes through expose (which blocks on ready)
				// rather than reportCompletion; that combination is rare.
				if err := expose(gctx, name, spec, false); err != nil {
					return fmt.Errorf("deploy %s: %w", name, err)
				}
				outcomes[i].mounted = true
				d.Event(svcEvent(name, "up", "mounted; streaming over 9P"))
				// Lifecycle runs via server-side exec, independent of the 9P session, but
				// only once the workload is running (runMountedServiceHooks gates it on the
				// cluster-side reconcile — a mounted deploy-attach hold returns before any
				// pod is scheduled, so the exec would otherwise race the scheduler).
				out := hookLines.Writer(d.Out(), name+" | ")
				defer out.Close()
				if err := rt.runMountedServiceHooks(gctx, name, spec.Name, d, prog, out); err != nil {
					return fmt.Errorf("lifecycle %s: %w", name, err)
				}
				return nil
			}())
		})
	}
	groupErr := grp.Wait()
	for i, name := range names {
		if outcomes[i].mountFree {
			mountFree = append(mountFree, name)
		}
		if outcomes[i].mounted {
			mountedCount++
		}
	}
	if groupErr != nil {
		return finish(groupErr)
	}

	forwardCount := 0
	for _, fwds := range project.Forwards() {
		forwardCount += len(fwds)
	}

	// Foreground `up` mirrors `docker compose up`: stay attached until Ctrl-C (or
	// until an external `down` removes the deployments), holding any client-local
	// 9P mounts and auto-forwarded ports for the session — even when no selected
	// service publishes a port. Return at once only when there is genuinely
	// nothing to attend: `up -d` (deploy done / nothing to hand off), no services
	// selected, or --no-forward-ports with no client-local mounts (the scripting
	// escape hatch, where the SOCKS5 proxy also never applies).
	socksActive := len(conduit.Banner()) > 0
	noForward := c.conduitCfg(rt).Mode == clientconduit.ModeNone
	if !holdForeground(c.Detach, len(names), mountedCount, noForward) {
		teardown()
		return nil
	}
	switch {
	case socksActive:
		d.Info("%d service(s) with client-local mounts held, SOCKS5 proxy running; press Ctrl-C to stop.", mountedCount)
	case mountedCount > 0 || forwardCount > 0:
		d.Info("%d service(s) with client-local mounts, %d with forwarded ports; press Ctrl-C to stop.", mountedCount, forwardCount)
	default:
		d.Info("%d service(s) up; press Ctrl-C to stop.", len(names))
	}
	// Watch our deployments so an external `down` (which deletes them server-side
	// but has no channel to this foreground process) makes us exit instead of
	// sitting idle holding now-defunct mounts/forwards — like `docker compose up`
	// exiting when the containers it attached to disappear.
	resources := make([]string, 0, len(names))
	for _, name := range names {
		resources = append(resources, rt.plans[name].Resource)
	}
	gone := watchGone(ctx, rt.client, resources, reconcilePollInterval)

	// Attach to the services' container logs and stream them, prefixed by service
	// name, until Ctrl-C or an external `down` — the way `docker compose up`
	// (without -d) tails everything it started. --no-attach opts out (still holds
	// mounts/forwards). Runs under a child context so exiting the hold below stops
	// the follow streams promptly.
	logCtx, logCancel := context.WithCancel(ctx)
	defer logCancel()
	var logDone chan struct{}
	if !c.NoAttach {
		logDone = make(chan struct{})
		go func() {
			defer close(logDone)
			opts := api.LogOptions{Follow: true, Tail: "all"}
			if err := rt.streamLogs(logCtx, names, opts, !c.NoLogPrefix, d.Out(), d.Err()); err != nil && logCtx.Err() == nil {
				d.Error("streaming service logs: %v", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		d.Info("stopping...")
	case <-gone:
		d.Info("services removed; exiting.")
	}
	logCancel()
	if logDone != nil {
		<-logDone
	}
	teardown()
	// Terminating a foreground `up` removes the mount-free deployments it created,
	// so Ctrl-C stops everything it brought up — matching `docker compose up`,
	// which stops its containers on exit. (Mounted services were already removed by
	// teardown's project.Close, via the server-side deploy-attach handler.) Only on
	// this real foreground exit — the early non-blocking returns above keep
	// fire-and-forget deployments running, as `up -d` / --no-forward-ports intend.
	removeDeployments(rt.client, rt.plans, mountFree, d)
	return nil
}

// The live *client.Client performs the foreground-exit deletes and the teardown
// wait; assert the contract so a signature change on either method is caught here.
var _ teardownClient = (*client.Client)(nil)

// teardownClient is the slice of *client.Client that removeDeployments needs: it
// deletes a deployment and then polls its status while the workloads drain (via
// reportTeardown), the way `down` does. A narrow interface keeps it unit-testable
// with a scripted fake.
type teardownClient interface {
	deploymentDeleter
	statusPoller
}

// deploymentDeleter deletes a deployment by resource name.
type deploymentDeleter interface {
	Delete(ctx context.Context, name string) error
}

// removeDeployments deletes the given compose services' deployments when a
// foreground `up` exits, then waits for each to fully terminate — the way `down`
// waits (reportTeardown) — so terminating the session stops everything it brought
// up (Ctrl-C) and the user sees the workloads drain instead of an instant
// "removed". It is a harmless no-op when an external `down` already removed them
// (the `gone` exit; reportTeardown reports the zero-instance state at once).
//
// The parent ctx is already cancelled (the Ctrl-C that triggered this exit), so
// the deletes run on a fresh, bounded context that the cancelled parent can't
// abort, mirroring the server-side deploy-attach teardown. A second Ctrl-C, on
// the other hand, aborts the teardown wait (via waitCtx) so a hung reconcile
// stays escapable, exactly as `down` stays interruptible. A delete failure is
// logged but never blocks the others. Removal is in reverse selection order,
// like `down`.
func removeDeployments(cl teardownClient, plans map[string]compose.ServicePlan, names []string, d *cliout.Driver) {
	if len(names) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(rootContext(), teardownWaitTimeout)
	defer cancel()
	waitCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	for i := len(names) - 1; i >= 0; i-- {
		name := names[i]
		if err := cl.Delete(ctx, plans[name].Resource); err != nil {
			d.Warn("removing %s: %v", name, err)
			continue
		}
		// Watch the cluster-side teardown until the workloads are fully gone,
		// reporting each container's state changes and the final "removed" — like
		// `down`, instead of claiming removal the instant the delete is accepted.
		reportTeardown(waitCtx, cl, name, plans[name].Resource, d, reconcilePollInterval, teardownWaitTimeout)
		if waitCtx.Err() != nil {
			return // second Ctrl-C, or the teardown budget elapsed; stop waiting on the rest
		}
	}
}

// needsBackgroundAgent reports whether `up -d` must hand the selected services to
// the persistent background agent instead of deploying statelessly and exiting.
// Any client-local mount, or a client-side-egress relay mode (proxy/transparent,
// whose caretaker tunnels the workload's traffic back through a live deploy-attach
// session), needs a session the process must outlive; a SOCKS5 conduit needs the
// hosted proxy to persist; and port-forward mode needs the local listeners for any
// published port. Counting egress relay is essential: a relay-only service (no
// mounts, no ports) that skipped this would fall through to runForeground with
// Detach set, which deploys the workload on a held session and then tears it down
// the instant the detached up returns (holdForeground is false when detached), so
// the workload would never persist.
func needsBackgroundAgent(specs []api.DeploySpec, mode string) bool {
	hasPorts := false
	for _, s := range specs {
		if len(s.Mounts) > 0 || s.Egress.NeedsRelay() {
			return true
		}
		if len(s.Ports) > 0 {
			hasPorts = true
		}
	}
	switch mode {
	case clientconduit.ModeSocks5:
		return len(specs) > 0 // the proxy must persist even with no mounts/ports
	case clientconduit.ModePortForward:
		return hasPorts
	}
	return false
}

// upDetached deploys mount-free services fire-and-forget, then hands services
// with client-local mounts — and services whose published ports should stay
// forwarded — to the project's background supervisor over its control socket,
// spawning the supervisor only if one is not already running.
func (c *UpCmd) upDetached(cli *Cmd, rt *runtime, cfg clientconduit.Config, names []string) error {
	ctx, stop := signalContext()
	defer stop()

	// Resolve the ingress-via-conduit config client-side (a native controller may be
	// learned from the server's /info) so the agent receives it fully in cfg.Ingress.
	rt.resolveIngress(ctx, &cfg, c.IngressConduit)

	d := rt.driver()
	socks5Mode := cfg.Mode == clientconduit.ModeSocks5
	// The set of services this up is bringing up, so a dependency wait ignores
	// depends_on targets outside the selection (they are never deployed here).
	selected := make(map[string]bool, len(names))
	for _, n := range names {
		selected[n] = true
	}
	// One-shot completion services get a completion-aware wait (see runForeground
	// and completionServices / reportCompletion).
	completion := completionServices(rt, selected)

	// Build every selected service with a build section up front, concurrently
	// and deduplicated (see runForeground's identical pre-build step for why).
	builtTags, err := rt.buildServices(ctx, buildPlans(rt, names), c.SSH, buildOverrides{})
	if err != nil {
		return err
	}

	// Deploy + reconcile every selected service concurrently, mirroring
	// runForeground's identical loop (see its doc comment for why this alone
	// resolves the depends_on topology, with no separate graph/level pass).
	// Unlike runForeground, no per-service call here blocks on the client-side
	// session engine: mounted/relay services are only appended to daemonSvcs
	// (cheap) and handed to the background agent in ONE batch after the loop, so
	// this loop's concurrency is unconstrained by that engine's internal
	// reconcile mutex.
	prog := d.Progress()
	defer prog.Stop()
	hookLines := d.LineGroup()

	daemonSvcResults := make([]*daemonService, len(names))
	grp, gctx := errgroup.WithContext(ctx)
	for i, name := range names {
		i, name := i, name
		grp.Go(func() error {
			return suppressCascaded(gctx, func() error {
				// Honor depends_on conditions before deploying this service (see
				// runForeground). Mounted services are handed to the background agent
				// below; the wait still runs here so it covers both branches.
				if err := waitForDependencies(gctx, rt, rt.client, name, selected, d, reconcilePollInterval, reconcileWaitTimeout); err != nil {
					return err
				}
				plan := rt.plans[name]
				spec := plan.Spec
				if plan.Build != nil {
					spec.Image = builtTags[name]
				}
				if spec.Image == "" {
					return fmt.Errorf("service %q has neither image nor build", name)
				}
				forwardPorts := len(spec.Ports) > 0 && !c.NoForwardPorts
				// A client-side-egress relay mode routes the workload's traffic back through
				// this client, so — like a client-local mount — it needs the agent to hold a
				// live deploy-attach session (the daemonService below), not a stateless POST.
				if len(spec.Mounts) == 0 && !spec.Egress.NeedsRelay() {
					if _, err := rt.client.Deploy(gctx, spec); err != nil {
						return fmt.Errorf("deploy %s: %w", name, err)
					}
					// A one-shot completion service never reaches Running; wait for it to
					// terminate instead of stalling the reconcile timeout (see runForeground).
					var st api.DeployStatus
					if completion[name] {
						st = reportCompletion(gctx, rt.client, name, spec.Name, d, prog, reconcilePollInterval, reconcileWaitTimeout)
					} else {
						st = reportReconcile(gctx, rt.client, name, spec.Name, d, prog, reconcilePollInterval, reconcileWaitTimeout)
					}
					if gctx.Err() != nil {
						return nil
					}
					d.Event(svcUp(name, st))
					out := hookLines.Writer(d.Out(), name+" | ")
					defer out.Close()
					if err := rt.runServiceHooks(gctx, name, out); err != nil {
						return fmt.Errorf("lifecycle %s: %w", name, err)
					}
					// Register the already-deployed service with the agent as ForwardOnly so it
					// holds the session conduit for it. In SOCKS5 mode this records the service's
					// short-name alias (Add binds no listeners there — the proxy already reaches
					// it) so `up -d` reaches it by short/bare name just like a foreground `up`; in
					// port-forward mode it holds just the per-service listeners.
					if socks5Mode || forwardPorts {
						daemonSvcResults[i] = &daemonService{Name: name, Spec: spec, ForwardPorts: forwardPorts, ForwardOnly: true}
					}
					return nil
				}
				daemonSvcResults[i] = &daemonService{Name: name, Spec: spec, ForwardPorts: forwardPorts}
				return nil
			}())
		})
	}
	// unpackGroupErr resolves a concurrent group's error the way this (non-
	// foreground) up path always has: a genuine failure (any selected service)
	// propagates as-is, but a real Ctrl-C/SIGTERM (the ORIGINAL ctx, not the
	// errgroup-derived one) always surfaces as ctx.Err() — unlike runForeground,
	// upDetached has no shutdownExit "clean exit 0" contract, so this must NOT
	// silently become nil just because every in-flight goroutine's own error got
	// suppressed as cascade fallout (see suppressCascaded) by the time Wait
	// returns.
	unpackGroupErr := func(err error) error {
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if err := unpackGroupErr(grp.Wait()); err != nil {
		return err
	}
	var daemonSvcs []daemonService
	for _, r := range daemonSvcResults {
		if r != nil {
			daemonSvcs = append(daemonSvcs, *r)
		}
	}
	// SOCKS5 mode still needs the helper (to host the proxy) even when no service
	// has mounts or a per-service forward.
	if len(daemonSvcs) == 0 && !socks5Mode {
		return nil
	}

	// Hand the work to the single background agent, spawning it if needed.
	socket, err := clientagent.EnsureRunning()
	if err != nil {
		return fmt.Errorf("start background agent: %w", err)
	}
	if info := clientagent.Ping(socket); info != nil && info.Protocol < clientagent.ProtocolVersion {
		// A pre-existing agent from an older cornus build: keep it (killing it drops
		// every project/frontend it holds) but warn — it may lack newer behavior.
		d.Warn("background agent was started by an older cornus build; run `cornus daemon stop` then retry to replace it")
	}
	var banners []string
	if len(daemonSvcs) > 0 {
		resp, err := clientagent.Send(socket, clientagent.Request{
			Action:   "up",
			Project:  rt.projectName,
			Conn:     rt.connSpec,
			Conduit:  cfg,
			Services: daemonSvcs,
		})
		if err != nil {
			return fmt.Errorf("hand mounts to background agent: %w", err)
		}
		if !resp.OK {
			return fmt.Errorf("background up: %s", resp.Error)
		}
		banners = resp.Banners
		var hookSvcs []daemonService
		for _, s := range daemonSvcs {
			if !s.ForwardOnly {
				switch resp.Statuses[s.Name] {
				case svcStatusUpToDate:
					d.Event(svcEvent(s.Name, "up-to-date", "held by background agent"))
				case svcStatusRecreated:
					d.Event(svcEvent(s.Name, "recreated", "configuration changed (mounted; held by background agent)"))
				default:
					d.Event(svcEvent(s.Name, "up", "mounted; held by background agent"))
				}
			}
			for _, f := range resp.Forwards[s.Name] {
				d.Event(svcEvent(s.Name, "forwarding", f+" (held by background agent)"))
			}
			if s.ForwardOnly {
				continue // deployed above; lifecycle already ran
			}
			hookSvcs = append(hookSvcs, s)
		}
		// The agent's `up` reply returns once every deploy-attach hold is ready, but
		// for the kubernetes backend that fires when the objects are created, before
		// any pod is scheduled. runMountedServiceHooks gates each service's
		// container-side lifecycle on the cluster-side reconcile so the server-side
		// exec resolves a running pod instead of racing the scheduler ("no pods for
		// deployment ..."). Run them concurrently (sharing prog/hookLines from
		// above) so one service's slow rollout doesn't block another's hooks from
		// even starting.
		hgrp, hgctx := errgroup.WithContext(ctx)
		for _, s := range hookSvcs {
			s := s
			hgrp.Go(func() error {
				return suppressCascaded(hgctx, func() error {
					out := hookLines.Writer(d.Out(), s.Name+" | ")
					defer out.Close()
					if err := rt.runMountedServiceHooks(hgctx, s.Name, s.Spec.Name, d, prog, out); err != nil {
						return fmt.Errorf("lifecycle %s: %w", s.Name, err)
					}
					return nil
				}())
			})
		}
		if err := unpackGroupErr(hgrp.Wait()); err != nil {
			return err
		}
	} else if socks5Mode {
		// A socks5 project with no mount/forward services still needs the agent up
		// to host the proxy: register an empty project so the agent holds it.
		resp, err := clientagent.Send(socket, clientagent.Request{
			Action: "up", Project: rt.projectName, Conn: rt.connSpec, Conduit: cfg,
		})
		if err != nil {
			return fmt.Errorf("start socks5 proxy on background agent: %w", err)
		}
		if !resp.OK {
			return fmt.Errorf("background up: %s", resp.Error)
		}
		banners = resp.Banners
	}
	if socks5Mode {
		// The agent's banner carries the actually-bound address — essential for a
		// session-local proxy on an ephemeral port, where the client's cfg only holds
		// ":0". Fall back to the configured listen if an older agent sent no banner.
		if len(banners) > 0 {
			for _, line := range banners {
				d.Info("%s (project %q, held by background agent)", line, rt.projectName)
			}
		} else {
			listen := cfg.Socks5Listen
			if listen == "" {
				listen = socks5.DefaultListen
			}
			d.Info("SOCKS5 proxy for project %q running in the background agent on %s", rt.projectName, listen)
		}
	}
	return nil
}

// DownCmd stops and removes services.
type DownCmd struct {
	Services []string `kong:"arg,optional,help='Services to remove (default: all).'"`
	Wait     bool     `kong:"name='wait',default='true',negatable,help='Wait for workloads to terminate before returning (default: true; --no-wait returns as soon as the delete is accepted).'"`
	Volumes  bool     `kong:"name='volumes',short='v',help='Also remove named volumes declared in the Compose file (project-scoped, non-external), like docker compose down --volumes. External volumes are never removed.'"`
}

// volumeRemover deletes a named volume by its resource name. A narrow interface
// over *client.Client so removeProjectVolumes is unit-testable with a fake.
type volumeRemover interface {
	DeleteVolume(ctx context.Context, name string) error
}

var _ volumeRemover = (*client.Client)(nil)

// removeProjectVolumes removes the project's named volumes on `down --volumes`,
// mirroring `docker compose down --volumes`: every top-level `volumes:` entry
// that is not external (cornus never provisioned an external volume, so it is
// left untouched). Anonymous volumes are already reaped with their deployment by
// the backend delete above, so only the shared named volumes remain.
//
// A backend that cannot remove volumes (ErrVolumeRemovalUnsupported / 501) is a
// soft skip with one warning, not a failure. Other per-volume errors are warned
// and the first is returned after attempting them all, so one wedged volume does
// not hide the rest.
func removeProjectVolumes(ctx context.Context, cl volumeRemover, projectName string, defs map[string]compose.VolumeDef, d *cliout.Driver) error {
	sources := make([]string, 0, len(defs))
	for name, def := range defs {
		if def.External {
			continue
		}
		sources = append(sources, name)
	}
	sort.Strings(sources)
	var firstErr error
	for _, source := range sources {
		res := compose.VolumeResourceName(projectName, source, defs)
		switch err := cl.DeleteVolume(ctx, res); {
		case err == nil:
			d.Event(svcEvent(res, "volume removed", ""))
		case errors.Is(err, client.ErrVolumeRemovalUnsupported):
			d.Warn("this server's deploy backend does not support removing volumes; skipping --volumes")
			return firstErr
		default:
			d.Warn("removing volume %s: %v", res, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Run removes services in reverse dependency order.
func (c *DownCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	rt, err := cli.load(r, d)
	if err != nil {
		return err
	}
	defer rt.cleanup()
	ctx, stop := signalContext()
	defer stop()

	names, err := rt.selectServices(c.Services)
	if err != nil {
		return err
	}
	// Tell the background agent to release the selected services of this project
	// (empty = all). An unreachable agent means nothing is held — proceed straight
	// to the server-side deletes below.
	socket := clientagent.Socket()
	if clientagent.Ping(socket) != nil {
		var svc []string
		if len(c.Services) > 0 {
			svc = names
		}
		if _, err := clientagent.Send(socket, clientagent.Request{Action: "down", Project: rt.projectName, Names: svc}); err != nil {
			d.Warn("telling background agent to release the project: %v", err)
		}
	}
	for i := len(names) - 1; i >= 0; i-- {
		name := names[i]
		if err := rt.client.Delete(ctx, rt.plans[name].Resource); err != nil {
			return fmt.Errorf("remove %s: %w", name, err)
		}
		if !c.Wait {
			// Fire-and-forget: the delete has been accepted, don't wait for the
			// workloads to finish terminating.
			d.Event(svcEvent(name, "removed", ""))
			continue
		}
		// Watch the cluster-side teardown and report each container's state
		// changes until the deployment is fully gone (prints the final "removed"),
		// the way `docker compose down` waits for the workloads to terminate.
		reportTeardown(ctx, rt.client, name, rt.plans[name].Resource, d, reconcilePollInterval, teardownWaitTimeout)
		if ctx.Err() != nil {
			return ctx.Err() // interrupted mid-wait (Ctrl-C); stay interruptible
		}
	}
	// --volumes removes the project's named volumes after the workloads are gone
	// (a volume in use cannot be removed), like `docker compose down --volumes`.
	if c.Volumes {
		return removeProjectVolumes(ctx, rt.client, rt.projectName, rt.project.Project().Volumes(), d)
	}
	return nil
}

// PsCmd lists services and their status.
type PsCmd struct {
	Quiet    bool   `kong:"name='quiet',short='q',help='Only print resource identifiers of created services, one per line.'"`
	Services bool   `kong:"name='services',help='Only print service names, one per line, in dependency order.'"`
	Format   string `kong:"name='format',default='table',help='Output format: table (default) or json.'"`
}

// psRow is one line of `ps` output: a compose service, its deployment resource
// name, the effective image, and a status summary ("N/M running" or "not
// created").
type psRow struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	Status  string `json:"status"`
	// created is true when the service has a live deployment (used by --quiet,
	// which lists only created services). Not serialized.
	created bool
}

// psRows builds the `ps` rows for the project's services in dependency order.
// A service without a deployment in byResource is "not created" and falls back
// to its spec image; a created one uses runningSummary and the status image when
// the backend reports one.
func psRows(order []string, plans map[string]compose.ServicePlan, byResource map[string]api.DeployStatus) []psRow {
	rows := make([]psRow, 0, len(order))
	for _, name := range order {
		plan := plans[name]
		st, ok := byResource[plan.Resource]
		status := "not created"
		image := plan.Spec.Image
		if ok {
			status = runningSummary(st)
			if st.Image != "" {
				image = st.Image
			}
		}
		rows = append(rows, psRow{Service: name, Name: plan.Resource, Image: image, Status: status, created: ok})
	}
	return rows
}

// renderPs renders the `ps` rows per the command's flags. Precedence: --quiet
// (resource ids of created services, one per line) wins, then --services
// (service names in order, one per line); both ignore --format. Otherwise
// --format selects "table" (the default aligned table) or "json" (indented
// array). The scripting paths write to d.Out(); the table path uses the driver's
// table renderer.
func renderPs(d *cliout.Driver, rows []psRow, quiet, services bool, format string) error {
	switch {
	case quiet:
		return psQuiet(d.Out(), rows)
	case services:
		return psServices(d.Out(), rows)
	}
	switch format {
	case "table":
		tbl := d.Table("SERVICE", "NAME", "IMAGE", "STATUS")
		for _, row := range rows {
			tbl.Row(row.Service, row.Name, row.Image, row.Status)
		}
		return tbl.Flush()
	case "json":
		return psJSON(d.Out(), rows)
	default:
		return fmt.Errorf("unsupported format %q (want table or json)", format)
	}
}

// psQuiet prints the resource id of each created service, one per line.
func psQuiet(w io.Writer, rows []psRow) error {
	for _, row := range rows {
		if !row.created {
			continue
		}
		if _, err := fmt.Fprintln(w, row.Name); err != nil {
			return err
		}
	}
	return nil
}

// psServices prints every service name, one per line, in dependency order.
func psServices(w io.Writer, rows []psRow) error {
	for _, row := range rows {
		if _, err := fmt.Fprintln(w, row.Service); err != nil {
			return err
		}
	}
	return nil
}

// psJSON writes the rows as an indented JSON array.
func psJSON(w io.Writer, rows []psRow) error {
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	_, err = fmt.Fprintln(w)
	return err
}

// Run prints this project's deployments per the --quiet/--services/--format
// flags. Reports every service in the project regardless of the active
// --profile / COMPOSE_PROFILES set: unlike up/down/build (which act on
// rt.order/rt.plans, the profile-filtered selection), ps is a read-only status
// query, so it reads rt.project.Project()'s complete, unfiltered model — a
// service deployed under a profile active in another terminal must still be
// reported here even without that profile active in this one. It uses the
// failure-tolerant PlanForStatus so a single malformed service (e.g. one
// behind a profile the user isn't using) still lists the rest rather than
// aborting the whole status view.
func (c *PsCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	rt, err := cli.load(r, d)
	if err != nil {
		return err
	}
	defer rt.cleanup()
	ctx, stop := signalContext()
	defer stop()

	order, plans, errs := rt.project.Project().PlanForStatus(rt.projectName)
	// Surface any per-service translation failures as warnings (deterministic
	// order) without failing the status query.
	if len(errs) > 0 {
		names := make([]string, 0, len(errs))
		for name := range errs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			d.Warn("service %s: %v", name, errs[name])
		}
	}

	list, err := rt.client.List(ctx)
	if err != nil {
		return err
	}
	byResource := map[string]api.DeployStatus{}
	for _, st := range list {
		byResource[st.Name] = st
	}

	rows := psRows(order, plans, byResource)
	return renderPs(d, rows, c.Quiet, c.Services, c.Format)
}

// BuildCmd builds service images.
type BuildCmd struct {
	Services []string `kong:"arg,optional,help='Services to build (default: all with a build section).'"`
	SSH      []string `kong:"name='ssh',sep='none',help='SSH agent forwarding: \"default\" or \"id[=socket]\" (RUN --mount=type=ssh), repeatable. Merges over each service build.ssh.'"`
	NoCache  bool     `kong:"name='no-cache',help='Do not use the build cache.'"`
	BuildArg []string `kong:"name='build-arg',sep='none',help='Set a build-time variable KEY=VALUE (repeatable). A bare KEY takes its value from the environment. Overrides the compose build.args.'"`
}

// Run builds (and pushes) images for services that define a build section.
func (c *BuildCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	rt, err := cli.load(r, d)
	if err != nil {
		return err
	}
	defer rt.cleanup()
	ctx, stop := signalContext()
	defer stop()

	names, err := rt.selectServices(c.Services)
	if err != nil {
		return err
	}
	args, err := parseBuildArgs(c.BuildArg)
	if err != nil {
		return err
	}
	ov := buildOverrides{noCache: c.NoCache, args: args}
	plans := buildPlans(rt, names)
	if len(plans) > 0 {
		if _, err := rt.buildServices(ctx, plans, c.SSH, ov); err != nil {
			return err
		}
	}
	if len(plans) == 0 {
		d.Info("no services with a build section")
	}
	return nil
}

// RestartCmd restarts services.
type RestartCmd struct {
	Services []string `kong:"arg,optional,help='Services to restart (default: all).'"`
}

// Run restarts services.
func (c *RestartCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	return runAction(cli, r, d, c.Services, "restart")
}

// StopCmd stops services without removing them.
type StopCmd struct {
	Services []string `kong:"arg,optional,help='Services to stop (default: all).'"`
}

// Run stops services.
func (c *StopCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	return runAction(cli, r, d, c.Services, "stop")
}

// StartCmd starts previously stopped services.
type StartCmd struct {
	Services []string `kong:"arg,optional,help='Services to start (default: all).'"`
}

// Run starts services.
func (c *StartCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	return runAction(cli, r, d, c.Services, "start")
}

// daemonHeldService reports whether a lifecycle action (stop/start/restart) must
// be refused for a service because its client-local mount is held by the running
// background `up -d` helper: a client-served 9P mount can't be acted on
// independently of the helper holding its deploy-attach session without leaving a
// broken state. True only when the service has client-local mounts AND a
// supervisor is alive; mount-free services, and any service when no supervisor is
// running, are unaffected.
func daemonHeldService(mountCount int, daemonAlive bool) bool {
	return mountCount > 0 && daemonAlive
}

// holdForeground reports whether a foreground `up` should stay attached (block
// until Ctrl-C or an external `down`) after deploying, mirroring `docker compose
// up`, which stays attached regardless of whether any service publishes a port.
// It returns false — i.e. the command returns immediately — only when there is
// nothing to attend: detached (`up -d` reaches runForeground only when it had
// nothing to hand to the background helper), no services selected, or
// --no-forward-ports with no client-local mounts (the scripting escape hatch, so
// pipelines can still deploy-and-exit without a port publish). Previously any
// port-free, mount-free compose returned here as "nothing held client-side",
// which made `up` exit immediately instead of staying up.
func holdForeground(detached bool, services, sessions int, noForward bool) bool {
	if detached || services == 0 {
		return false
	}
	if noForward && sessions == 0 {
		return false
	}
	return true
}

// runAction applies a lifecycle action to the selected services. Services with
// client-local mounts held by the project's background `up -d` helper are refused
// (see daemonHeldService): use `down` to stop them, since a client-served mount
// can't be stopped/started/restarted while the helper is still attached.
func runAction(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver, requested []string, action string) error {
	rt, err := cli.load(r, d)
	if err != nil {
		return err
	}
	defer rt.cleanup()
	ctx, stop := signalContext()
	defer stop()

	names, err := rt.selectServices(requested)
	if err != nil {
		return err
	}
	// stop runs in reverse dependency order; start/restart in forward order.
	if action == "stop" {
		for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
			names[i], names[j] = names[j], names[i]
		}
	}
	daemonAlive := agentHoldsProject(rt.projectName)
	past := map[string]string{"stop": "stopped", "start": "started", "restart": "restarted"}
	for _, name := range names {
		if daemonHeldService(len(rt.plans[name].Spec.Mounts), daemonAlive) {
			return fmt.Errorf("service %q has client-local mounts held by the background agent; use `cornus compose down` to stop it (a client-served mount can't be %s-ed while attached)", name, action)
		}
		if err := rt.client.Action(ctx, rt.plans[name].Resource, action); err != nil {
			return fmt.Errorf("%s %s: %w", action, name, err)
		}
		d.Event(svcEvent(name, past[action], ""))
		// Per the Dev Container spec, postStart/postAttach run every time the
		// container starts, so re-run them on start/restart (once-per-create
		// hooks are not re-run). A no-op for plain Compose services.
		if action == "start" || action == "restart" {
			if err := rt.runServiceStartHooks(ctx, name); err != nil {
				return fmt.Errorf("lifecycle %s: %w", name, err)
			}
		}
	}
	return nil
}

// runningSummary formats "N/M running" for a deployment status.
func runningSummary(st api.DeployStatus) string {
	running := 0
	for _, in := range st.Instances {
		if in.Running {
			running++
		}
	}
	return fmt.Sprintf("%d/%d running", running, len(st.Instances))
}
