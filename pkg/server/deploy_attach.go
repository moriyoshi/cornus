package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"cornus/pkg/activity"
	"cornus/pkg/api"
	"cornus/pkg/credential"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/hostcheck"
	"cornus/pkg/hostenv"
	"cornus/pkg/logging"
	"cornus/pkg/wire"
)

// readyPollInterval and readyTimeout bound the deploy-attach readiness wait: how
// often the backend is polled for the workload to come up, and how long to wait
// before giving up on a wedged bring-up. The timeout is generous so a slow image
// pull is not cut short; a crash loop is reported (streamed) long before it. They
// are vars, not consts, so tests can shrink them.
var (
	readyPollInterval = time.Second
	readyTimeout      = 5 * time.Minute
)

// finalStatusTimeout bounds the last backend status read taken on teardown, the
// one that resolves the workload's exit code for the terminal event. It runs on
// a background context (the caller has usually already disconnected by then) and
// must never delay the delete behind a wedged backend, so it is short: a missed
// read just means the event reports "unknown", which is a legitimate answer.
var finalStatusTimeout = 5 * time.Second

// finalExitCode reads the workload's settled exit status straight before it is
// deleted. Nil when the backend errors, cannot report a code, or the workload is
// still running (an explicit teardown of a healthy workload — nobody knows what
// it "would have" exited with, and inventing 0 is the bug this exists to avoid).
func finalExitCode(backend deploy.Backend, name string) *int {
	ctx, cancel := context.WithTimeout(context.Background(), finalStatusTimeout)
	defer cancel()
	st, err := backend.Status(ctx, name)
	if err != nil {
		return nil
	}
	if code, ok := st.TerminalExitCode(); ok {
		return &code
	}
	return nil
}

// exitCodeOf resolves an already-held status to a terminal exit code pointer
// (nil = unknown), for the paths that have just polled the backend themselves
// and must not poll again.
func exitCodeOf(st api.DeployStatus) *int {
	if code, ok := st.TerminalExitCode(); ok {
		return &code
	}
	return nil
}

// handleDeployAttach serves GET /.cornus/v1/deploy/attach: it upgrades to a WebSocket
// and runs a long-lived deployment whose caller-local bind mounts are served
// over 9P. How those mounts are realized depends on the backend:
//
//   - kubernetes (a deploy.MountingBackend): each mount becomes a live 9P mount
//     inside the pod via a privileged caretaker sidecar that relays back through
//     this server (an 'M' stream on GET /.cornus/v1/caretaker/attach). Nothing is mounted
//     on a node host, so the pod can schedule anywhere.
//   - dockerhost: the mount is kernel-9p-mounted on this host and the source
//     rewritten before Apply (single-host).
//
// Either way the deployment lives exactly as long as the caller stays connected:
// on disconnect (or a "down" command) the workload is removed and mounts unwound.
func (s *Server) handleDeployAttach(w http.ResponseWriter, r *http.Request) {
	// A deploy-attach session applies a DeploySpec, so it is gated on the
	// "deploy" action exactly like POST /.cornus/v1/deploy — otherwise a policy that
	// restricts "deploy" could be bypassed via this WebSocket. Checked before
	// the upgrade so a denied caller gets a real 403.
	if !s.apiPolicy.Allow(Identity(r), "deploy") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to deploy"})
		return
	}
	sess, err := deploywire.Attach(w, r)
	if err != nil {
		// The connection is already hijacked on most failures; nothing useful to
		// write back.
		return
	}
	defer sess.Close()

	// Stamp the authenticated identity onto the session spec before any apply
	// path reads it (the mount/egress/credential helpers below re-read
	// sess.Spec.Spec, so the source must carry it).
	stampOriginSubject(&sess.Spec.Spec, Identity(r))

	spec := sess.Spec.Spec
	if spec.Name == "" || spec.Image == "" {
		sess.Finish(deploywire.Event{Err: "spec requires name and image", Done: true})
		return
	}

	backend, err := s.getBackend()
	if err != nil {
		sess.Finish(deploywire.Event{Err: "deploy backend unavailable: " + err.Error(), Done: true})
		return
	}

	// Default a telemetry spec with no endpoint to this server's own store, and
	// decide the mux question. Edited on the SESSION's spec, not the local copy
	// above, because the mount / credential / egress helpers all re-read
	// sess.Spec.Spec; the local copy is refreshed right after.
	s.normalizeTelemetry(r.Context(), &sess.Spec.Spec, backend.Name())
	spec = sess.Spec.Spec

	// Record the session for the flight recorder. Best-effort (see pkg/activity):
	// an unfinished deploy strands nothing, it just means this session was cut
	// short — which is exactly what an investigator wants to see next to the
	// mounts it had open at the time.
	act := s.activity.Begin(activity.KindDeploy, spec.Name, map[string]string{
		"image": spec.Image,
		"by":    Identity(r),
	})
	defer func() { act.End(err) }()

	hasLocal := len(sess.Spec.LocalMounts) > 0
	hasCreds := len(sess.Spec.CredentialSources) > 0
	hasEgress := needsEgressRelay(spec.Egress)
	// Whether the credentials need anything RUNNING alongside the workload. An
	// env-kind delivery does not: the server fetches it once over the held
	// session and the value is fixed into the container's environment at create.
	// Asking this here — before the backend split — is what keeps an env-only
	// deploy off the companion path, and it is the question the dispatch never
	// used to ask: `hasCreds` alone sent every credential to a caretaker, which
	// on a host backend meant demanding CORNUS_ADVERTISE_URL for a relay to
	// ourselves and then failing anyway.
	//
	// A client that declared credential backings with no matching spec block is
	// unprovable, not proven-free, so it keeps the old routing.
	can := serverDelivers(r.Context(), backend)
	credsNeedRuntime := hasCreds && (spec.Credentials == nil || len(deploy.SpecCaretakerKinds(spec, can)) > 0)
	// Server-materialized FILES are realized exactly like a client-local mount:
	// the server writes them under its own mounts dir and the backend binds the
	// path, which means the co-located path below — the one that translates a
	// server path into the one the RUNTIME resolves — is where they belong.
	//
	// Server-served ENDPOINTS route there too, for a different reason: nothing
	// about them touches the spec's mounts, but they must be bound after Apply
	// and torn down with the session, and that path is the one that owns a
	// per-deploy teardown on a co-located backend.
	credsHaveFiles := can.Files && specHasFileDelivery(spec)
	credsHaveEndpoints := can.Endpoints && specHasEndpointDelivery(spec)
	// A backend with no attachment path of its own still needs somewhere to have
	// its credentials realized. incus is the case: it implements neither
	// AttachingBackend (its companion is a sibling INSTANCE, which cannot carry
	// mounts or egress) nor EgressBackend, so an env-only credential deploy —
	// which needs nothing but a merge into spec.Env and a plain Apply — used to
	// fall all the way through to "not yet supported by the incus backend".
	//
	// Deliberately narrowed to backends that are NOT AttachingBackends, so the
	// three that are keep their existing route and kubernetes keeps materializing
	// a Secret rather than a spec literal. useSidecarMounts already excludes
	// kubernetes below; this makes the intent explicit rather than incidental.
	_, isAttaching := backend.(deploy.AttachingBackend)
	credsServerOnly := hasCreds && !credsNeedRuntime && !isAttaching
	coLocated := hasLocal || credsHaveFiles || credsHaveEndpoints || credsServerOnly
	// Only CLIENT-LOCAL MOUNTS need the 9P fast path, and the predicate means
	// exactly that: does this backend realize them by having the server
	// kernel-9P-mount and the runtime bind the mountpoint (hostcheck.go).
	//
	// Credential files must NOT be gated on it, and gating them here was a
	// category error: a credential directory involves no 9P at all — it is a
	// plain directory this server wrote — so asking a question about 9P excluded
	// containerd and incus from a capability they may well have, for a reason
	// about a different feature. Whether a backend resolves a path this server
	// wrote is what CredentialBinder asks, and can.Files already carries each
	// backend's own answer.
	mountsRealizable := !hasLocal || hostcheck.UsesHostMountFastPath(backend.Name())
	// Refuse a mount the runtime could not see, rather than deploying one that is
	// silently empty. See mountPropagationPrecondition.
	if hasLocal && mountsRealizable {
		if perr := s.mountPropagationPrecondition(r.Context(), backend); perr != nil {
			err = perr
			sess.Finish(deploywire.Event{Err: perr.Error(), Done: true})
			return
		}
	}
	var (
		status  api.DeployStatus
		cleanup func()
	)
	switch {
	case !hasLocal && !hasCreds && !hasEgress:
		status, err = backend.Apply(r.Context(), spec)
	case coLocated && !hasEgress && !credsNeedRuntime && !useSidecarMounts(backend) && mountsRealizable:
		// A co-located host backend with nothing to serve at runtime: the server
		// kernel-9P-mounts each client export under <DataDir>/mounts and rewrites
		// Mount.Source, the backend binds the mountpoint like any host path, and
		// the env credentials are resolved here too — so a single plain Apply
		// realizes the whole deploy and no companion is created.
		//
		// bare shares the server's mount namespace outright (it runs runc as the
		// server's own child), so the mount is directly visible to the container.
		// dockerhost's daemon may instead resolve that path in the HOST's
		// namespace while the server sits in a container of its own — still
		// co-located, but not identical — which is why the mountpoint is
		// translated before Apply rather than passed through.
		//
		// Mounts-only deploys land here too, with an empty credential set, so the
		// co-located path has one implementation and not two that can drift.
		status, cleanup, err = s.applyWithHostAttachments(r, sess, backend, can)
	case hasCreds || hasEgress:
		// Credentials and client-side egress are realized inside the workload by the
		// caretaker (the source / egress terminus runs on the client), so they need an
		// AttachingBackend; the same path also carries any mounts. A host backend that
		// is not an AttachingBackend but IS an EgressBackend realizes egress-only via a
		// companion caretaker container.
		if ab, ok := backend.(deploy.AttachingBackend); ok {
			status, cleanup, err = s.applyWithAttachments(r, sess, ab)
		} else if eb, ok := backend.(deploy.EgressBackend); ok && hasEgress && !hasCreds && !hasLocal {
			status, cleanup, err = s.applyWithEgress(r, sess, eb)
		} else {
			// Name the DELIVERY KINDS, not just "credentials". A backend that
			// realizes two of the three kinds itself and refuses the third is
			// now the normal case, so "client-sourced credentials are not
			// supported" is actively misleading — it sends an operator to the
			// server's configuration when the answer is one line of their spec.
			what := "client-sourced credentials"
			if kinds := deploy.SpecCaretakerKinds(spec, can); len(kinds) > 0 {
				what += " (" + strings.Join(kinds, "/") + " delivery)"
			}
			if hasEgress {
				what = "client-side egress"
			}
			sess.Finish(deploywire.Event{Err: what + " is not yet supported by the " + backend.Name() + " backend", Done: true})
			return
		}
	default:
		// Mounts only, and NOT the co-located fast path — that case is taken
		// above, so what remains is a backend whose mounts need a sidecar
		// (remote mode, or kubernetes) or one that cannot realize them at all.
		if mb, ok := backend.(deploy.MountingBackend); ok && useSidecarMounts(backend) {
			status, cleanup, err = s.applyWithSidecarMounts(r, sess, mb)
		} else {
			sess.Finish(deploywire.Event{Err: clientLocalMountsUnavailable(backend), Done: true})
			return
		}
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	if err != nil {
		// Apply itself failed. A backend can create the workload BEFORE a later
		// dependent step fails (e.g. a Job/Deployment created, then a PVC/Service/
		// Ingress apply errors) — so DELETE the half-created workload before dropping
		// the session, mirroring the not-ready path below. Otherwise the pod is left
		// running against a session the server no longer holds (the stale-mount reset
		// -> app starts on an empty mount -> confusing downstream failure we spent a
		// long time chasing). Delete is a no-op when nothing was created. Log the real
		// error too: it otherwise only reaches the client, so an operator watching the
		// server saw nothing.
		logging.FromContext(r.Context()).WarnContext(r.Context(),
			"deploy-attach: apply failed; removing partial workload and dropping session",
			"deployment", spec.Name, "error", err.Error())
		s.tunnels.stop(spec.Name)
		s.ingress.remove(r.Context(), spec.Name)
		_ = backend.Delete(context.Background(), spec.Name)
		cleanup()
		sess.Finish(deploywire.Event{Err: err.Error(), Done: true})
		return
	}

	// Apply only accepted the spec — the pods are not up yet. Wait for every
	// desired instance to reach Running before declaring the deploy ready, so a
	// workload that crash-loops (a bad image, a wedged sidecar) is reported
	// instead of the session silently claiming success. Diagnostics stream to the
	// caller as they appear.
	status, err = awaitReady(r.Context(), func(e deploywire.Event) { _ = sess.Event(e) }, backend, spec, status)
	if err != nil {
		// Bring-up failed or the caller went away: tear the half-started workload
		// down (mirroring the disconnect path) before reporting the terminal error.
		// Logged because this is a prime cause of the stale-mount-session symptom —
		// a mounted workload whose pod does not reach ready in time (or whose client
		// disconnects mid-bring-up) has its session torn down HERE, before the pod's
		// caretaker finished attaching, leaving the pod presenting a dead session.
		logging.FromContext(r.Context()).WarnContext(r.Context(),
			"deploy-attach: bring-up did not reach ready; deleting workload and dropping session",
			"deployment", spec.Name, "ctx_err", r.Context().Err(), "error", err.Error())
		s.tunnels.stop(spec.Name)
		s.ingress.remove(r.Context(), spec.Name)
		_ = backend.Delete(context.Background(), spec.Name)
		cleanup()
		// status is the last one awaitReady polled, so a workload that failed by
		// EXITING (a bad command, a failed init) reports the code it died with
		// rather than only a timeout message.
		sess.Finish(deploywire.Event{Err: err.Error(), Done: true, ExitCode: exitCodeOf(status)})
		return
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(),
		"deploy-attach: workload ready; holding session until caller disconnects", "deployment", spec.Name)
	s.ingress.apply(r.Context(), spec)
	_ = sess.Event(deploywire.Event{Status: &status, Ready: true, Log: "deployed " + spec.Name + "\n"})

	// Block until the caller disconnects or requests teardown.
	sess.Wait()
	logging.FromContext(r.Context()).InfoContext(r.Context(),
		"deploy-attach: caller disconnected; removing workload", "deployment", spec.Name)

	// A tunnel opened for this deployment name must be torn down too, mirroring
	// the DELETE handler in handleDeployItem — otherwise it outlives the ephemeral
	// deployment as a leaked serve() goroutine and an open public relay endpoint
	// (and would silently re-expose any later deployment that reuses the name).
	s.tunnels.stop(spec.Name)
	s.ingress.remove(r.Context(), spec.Name)
	// Capture how the workload ended BEFORE deleting it. Delete destroys the only
	// record of the exit status, and the caller cannot poll for it afterwards
	// either, so this read is the last chance to answer "did it succeed?" — the
	// question `docker wait` through the Docker API proxy is asking. A workload
	// still running here (an explicit teardown) yields nil = unknown.
	exit := finalExitCode(backend, spec.Name)
	// Remove the workload first (releases any bind), then run the mount cleanup.
	_ = backend.Delete(context.Background(), spec.Name)
	cleanup()
	sess.Finish(deploywire.Event{Done: true, ExitCode: exit})
}

// awaitReady blocks until every desired instance of the just-applied deployment
// is running, then returns the settled status. `initial` is the status Apply
// returned (its instance count is the desired replica count). While waiting it
// streams any instance diagnostic — a Waiting reason like CrashLoopBackOff, an
// image-pull error, or a scheduling failure — to the caller as a non-terminal
// error Event, so a wedged workload is surfaced instead of the session hanging
// silently. It returns an error when the caller disconnects (ctx cancelled) or
// the wait exceeds readyTimeout; the caller then tears the workload down.
func awaitReady(ctx context.Context, emit func(deploywire.Event), backend deploy.Backend, spec api.DeploySpec, initial api.DeployStatus) (api.DeployStatus, error) {
	name := spec.Name
	// A one-shot (restart "no"/"on-failure") is expected to exit: readiness is
	// satisfied when every instance is running OR has already completed cleanly, so
	// a fast init that finishes before the first poll is not mistaken for a hang.
	oneShot := deploy.IsOneShot(spec)
	ready := func(st api.DeployStatus) bool { return allReady(st, oneShot) }
	desired := len(initial.Instances)
	// No instances to wait on (a backend that does not enumerate them): preserve
	// the old fire-once behaviour.
	if desired == 0 || ready(initial) {
		return initial, nil
	}
	deadline := time.NewTimer(readyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(readyPollInterval)
	defer tick.Stop()

	st := initial
	var lastMsg string
	// stream emits a diagnostic to the caller once, de-duplicating repeats so a
	// crash loop reported every poll does not spam identical lines.
	stream := func(msg string) {
		if msg != "" && msg != lastMsg {
			lastMsg = msg
			emit(deploywire.Event{Err: msg})
		}
	}
	for {
		stream(firstDiagnostic(st))
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-deadline.C:
			detail := lastMsg
			if detail == "" {
				detail = fmt.Sprintf("%d/%d instances running", countRunning(st), desired)
			}
			return st, fmt.Errorf("deployment %q did not become ready within %s: %s", name, readyTimeout, detail)
		case <-tick.C:
		}
		next, err := backend.Status(ctx, name)
		if err != nil {
			stream("status: " + err.Error())
			continue
		}
		st = next
		if ready(st) {
			return st, nil
		}
	}
}

// allReady reports whether the status has at least one instance and every
// instance has reached its ready condition. For a long-lived workload that means
// every instance is Running. For a one-shot it ALSO accepts an instance that has
// terminated successfully (exit 0) — a run-to-completion init is "ready" once it
// has done its job, not only while still executing. A non-zero exit is never
// ready (a failed init must surface, and an on-failure Job keeps retrying).
func allReady(st api.DeployStatus, oneShot bool) bool {
	if len(st.Instances) == 0 {
		return false
	}
	for _, in := range st.Instances {
		if in.Running {
			continue
		}
		if oneShot && in.ExitCode != nil && *in.ExitCode == 0 {
			continue
		}
		return false
	}
	return true
}

// countRunning returns how many of the status's instances are running.
func countRunning(st api.DeployStatus) int {
	n := 0
	for _, in := range st.Instances {
		if in.Running {
			n++
		}
	}
	return n
}

// firstDiagnostic returns the first non-empty instance Message, or "" when no
// instance is reporting a problem.
func firstDiagnostic(st api.DeployStatus) string {
	for _, in := range st.Instances {
		if in.Message != "" {
			return in.Message
		}
	}
	return ""
}

// useSidecarMounts decides, for a backend that implements MountingBackend,
// whether the caretaker-sidecar path should actually be used. kubernetes has no
// host-mount fallback to prefer instead, so it is always true there. dockerhost,
// containerdhost and barehost implement MountingBackend unconditionally (so the
// type assertion always succeeds) but must opt in via RemoteCapable.Remote() —
// a daemon on a DIFFERENT host cannot be detected, so this is never inferred.
//
// What a false answer means is NOT uniform across those three, and reading it as
// "fall back to the co-located path" is wrong for one of them:
//
//   - dockerhost and bare do have that co-located path (the server kernel-9P
//     mounts and the runtime binds the mountpoint), so false genuinely selects
//     it.
//   - containerd has no such path at all (hostcheck.UsesHostMountFastPath is the
//     single source for which backends do). For it, false means client-local
//     mounts are UNAVAILABLE — which is why the rejection has to name
//     CORNUS_CONTAINERD_REMOTE rather than say the backend cannot do it.
//
// A cornus containerized on the daemon's OWN host is not the remote case and
// must not land here: it can still mount for itself, and does, with only the
// resulting paths translated for the runtime (hostVisibleMountSources).
// pkg/hostenv detects that shape precisely so it does not get mistaken for a
// remote daemon and saddled with a companion per instance.
func useSidecarMounts(backend deploy.Backend) bool {
	if rc, ok := backend.(deploy.RemoteCapable); ok {
		return rc.Remote()
	}
	return true
}

// clientLocalMountsUnavailable is the message for a mounts-only apply that no
// path can realize: the sidecar path was declined (or unavailable) and the
// backend has no co-located host-mount fast path.
//
// It distinguishes two cases that used to share one sentence, "client-local
// mounts are not supported by the X backend":
//
//   - The backend implements MountingBackend, so it CAN realize them — we are
//     only here because its remote mode is off (useSidecarMounts said no) and it
//     has no host fast path to fall back to. That is containerd today. Telling
//     that operator the backend does not support mounts is false, and it is the
//     expensive kind of false: the capability is one environment variable away,
//     and the message hides the variable. So name it.
//   - The backend does not implement MountingBackend at all (incus). Then the
//     original sentence is exactly right.
//
// A MountingBackend with no remoteModeEnvs entry falls back to the second
// sentence rather than inventing a variable name — vague beats wrong here,
// because a named-but-unread variable sends the operator to change something
// that cannot help and reports nothing when it does not.
func clientLocalMountsUnavailable(backend deploy.Backend) string {
	name := backend.Name()
	if _, ok := backend.(deploy.MountingBackend); ok {
		if env := remoteModeEnvs[name]; env != "" {
			return "client-local mounts on the " + name + " backend are realized by a caretaker companion, which is off by default" +
				" (this backend has no co-located host-mount path to fall back on); restart the server with " + env + "=1"
		}
	}
	return "client-local mounts are not supported by the " + name + " backend"
}

// mountSessionID picks the deploy-attach mount session id for an apply. It reuses
// the id already baked into name's running workload when the backend can report one
// (deploy.MountSessionReader) AND no live session currently holds it — so a
// re-apply (a client re-run or reconnect) re-registers under the id the running pod
// already presents, instead of orphaning it with a fresh id (the stale-mount-session
// reset; see mount_relay.go). Otherwise — first apply, a backend without read-back,
// a read-back error, or an id still held by a live session — it mints a new id. The
// id remains an unguessable capability: reuse only ever re-adopts an id already
// baked into a workload the same authenticated deployer is re-applying.
func (s *Server) mountSessionID(ctx context.Context, backend deploy.Backend, name string) string {
	reader, ok := backend.(deploy.MountSessionReader)
	if !ok {
		return newSessionID()
	}
	id, err := reader.ExistingMountSession(ctx, name)
	if err != nil || id == "" || s.mounts.has(id) {
		return newSessionID()
	}
	logging.FromContext(ctx).InfoContext(ctx, "deploy-attach: reusing existing mount session id",
		"deployment", name, "session", sessionDigest(id))
	return id
}

// registerAttachSession registers a deploy-attach session for the mount / egress /
// credential relays (put + hub advertise) and logs it, returning a cleanup that
// tears it down (del + hub withdraw) and logs that too. The register/teardown pair
// carries the deployment name and the session DIGEST — the same digest the
// caretaker-facing mount-reset WARN (logMountReset) logs — so a prematurely-dropped
// session (a pod presenting a session the server no longer holds) is diagnosable
// from the server logs alone: the teardown line shows exactly when, and near which
// bring-up outcome, the session went away relative to the pod's reset.
func (s *Server) registerAttachSession(ctx context.Context, id, name string, sess *deploywire.ServerSession) func() {
	s.mounts.put(id, sess)
	// With a distributed hub store, also advertise which replica holds this session
	// so a caretaker relaying via another replica can be forwarded here (no-op on
	// the single-replica in-memory store).
	// A failed advertisement is logged at ERROR and does NOT fail the attach: mounts
	// for pods landing on this replica work regardless, and the store re-drives the
	// write in the background. Without the line, a pod on a peer replica would just
	// get a mount reset explained as "no replica owns this session" (logMountReset)
	// for a session this replica plainly holds.
	if err := s.registerMountSession(id); err != nil {
		logging.FromContext(ctx).ErrorContext(ctx, "deploy-attach: mount session not advertised to peer replicas; a pod on another replica cannot reach it until the write lands",
			"deployment", name, "session", sessionDigest(id), "error", err)
	}
	logging.FromContext(ctx).InfoContext(ctx, "deploy-attach: mount session registered",
		"deployment", name, "session", sessionDigest(id))
	return func() {
		s.mounts.del(id)
		// WARN, not ERROR: the stale record makes a peer replica forward a mount
		// stream here for a session that is gone, which fails fast and closes (the
		// forward handler resolves locally only) rather than mis-serving anything.
		if err := s.unregisterMountSession(id); err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "deploy-attach: mount session still advertised to peer replicas after teardown; peers may forward to this replica until the withdrawal is retried",
				"deployment", name, "session", sessionDigest(id), "error", err)
		}
		logging.FromContext(ctx).InfoContext(ctx, "deploy-attach: mount session torn down",
			"deployment", name, "session", sessionDigest(id))
	}
}

// applyWithSidecarMounts registers the session for the mount relay and applies
// the spec with per-mount AttachMounts so a MountingBackend (kubernetes) injects
// live 9P sidecars. The returned cleanup unregisters the session.
func (s *Server) applyWithSidecarMounts(r *http.Request, sess *deploywire.ServerSession, backend deploy.MountingBackend) (api.DeployStatus, func(), error) {
	if err := rejectFileMounts(sess.Spec.LocalMounts, backend.Name()); err != nil {
		return api.DeployStatus{}, nil, err
	}
	adv := advertiseURL()
	if adv == "" {
		return api.DeployStatus{}, nil, fmt.Errorf("client-local mounts on the %s backend require CORNUS_ADVERTISE_URL (the in-cluster cornus URL the pod mount-agent dials)", backend.Name())
	}
	id := s.mountSessionID(r.Context(), backend, sess.Spec.Spec.Name)
	cleanup := s.registerAttachSession(r.Context(), id, sess.Spec.Spec.Name, sess)

	spec := sess.Spec.Spec
	// Resolved once, not per mount: every sidecar in one deploy must be told the
	// same companion image.
	image := agentImage()
	mounts := make([]deploy.AttachMount, 0, len(sess.Spec.LocalMounts))
	for _, lm := range sess.Spec.LocalMounts {
		if lm.Index < 0 || lm.Index >= len(spec.Mounts) {
			cleanup()
			return api.DeployStatus{}, nil, fmt.Errorf("local mount index %d out of range (%d mounts)", lm.Index, len(spec.Mounts))
		}
		mounts = append(mounts, deploy.AttachMount{
			Target:     spec.Mounts[lm.Index].Target,
			ReadOnly:   lm.ReadOnly,
			AsyncCache: lm.WritableCacheable(),
			Session:    id,
			Name:       lm.Name,
			RelayURL:   adv,
			AgentImage: image,
		})
	}
	status, err := backend.ApplyWithMounts(r.Context(), spec, mounts)
	if err != nil {
		cleanup()
		return api.DeployStatus{}, nil, err
	}
	return status, cleanup, nil
}

// applyWithEgress registers the deploy-attach session for the egress relay and
// applies the spec with an AttachEgress, so an EgressBackend (a host backend)
// realizes client-side egress via a companion caretaker. It is the egress-only host
// analogue of applyWithAttachments (no mounts/credentials). The returned cleanup
// unregisters the session.
func (s *Server) applyWithEgress(r *http.Request, sess *deploywire.ServerSession, backend deploy.EgressBackend) (api.DeployStatus, func(), error) {
	adv := advertiseURL()
	if adv == "" {
		return api.DeployStatus{}, nil, fmt.Errorf("client-side egress on the %s backend requires CORNUS_ADVERTISE_URL (the cornus URL the companion caretaker dials for the relay)", backend.Name())
	}
	id := s.mountSessionID(r.Context(), backend, sess.Spec.Spec.Name)
	cleanup := s.registerAttachSession(r.Context(), id, sess.Spec.Spec.Name, sess)
	spec := sess.Spec.Spec
	egress := &deploy.AttachEgress{
		Session:    id,
		RelayURL:   adv,
		AgentImage: agentImage(),
		Spec:       spec.Egress,
	}
	status, err := backend.ApplyWithEgress(r.Context(), spec, egress)
	if err != nil {
		cleanup()
		return api.DeployStatus{}, nil, err
	}
	return status, cleanup, nil
}

// companionAttachments names the attachments in sess that genuinely need a
// caretaker running alongside the workload — the ones, and only the ones, that
// make CORNUS_ADVERTISE_URL a requirement. It returns "" when the server can
// realize everything itself, which is the signal not to demand the URL at all.
//
// The distinction it draws is the whole point. This path serves kubernetes AND
// the host backends, and on a host backend an env-kind credential needs no
// caretaker: it was resolved at deploy time and is merged into the container
// environment (deploy.WithCredentialEnv). Naming it in this message is how a
// deploy that needed nothing came to fail asking for the address of a relay to
// ourselves — and then, once the operator supplied it, failed a second time on
// the backend's own credential rejection. Listing the runtime delivery KINDS
// rather than "credentials" is the other half: "endpoint delivery" tells an
// operator which line of their spec to change, where "credentials" sends them to
// the wrong file.
func companionAttachments(sess *deploywire.ServerSession, can deploy.ServerDelivers) string {
	var what []string
	if len(sess.Spec.LocalMounts) > 0 {
		// Only reached on the attaching path, where mounts ARE realized by a
		// companion; the co-located 9P fast path never gets here.
		what = append(what, "client-local mounts")
	}
	if kinds := deploy.SpecCaretakerKinds(sess.Spec.Spec, can); len(kinds) > 0 {
		what = append(what, "client-sourced credentials ("+strings.Join(kinds, "/")+" delivery)")
	} else if len(sess.Spec.CredentialSources) > 0 && sess.Spec.Spec.Credentials == nil {
		// Backings with no spec block: unprovable, so treat as needing a relay.
		what = append(what, "client-sourced credentials")
	}
	if needsEgressRelay(sess.Spec.Spec.Egress) {
		what = append(what, "client-side egress")
	}
	return strings.Join(what, " and ")
}

// applyWithAttachments registers the session for the mount AND credential relays
// and applies the spec with both per-mount AttachMounts and per-credential
// AttachCredentials, so an AttachingBackend (kubernetes) injects one caretaker
// carrying every live 9P mount and every credential delivery. The returned
// cleanup unregisters the session.
func (s *Server) applyWithAttachments(r *http.Request, sess *deploywire.ServerSession, backend deploy.AttachingBackend) (api.DeployStatus, func(), error) {
	if err := rejectFileMounts(sess.Spec.LocalMounts, backend.Name()); err != nil {
		return api.DeployStatus{}, nil, err
	}
	can := serverDelivers(r.Context(), backend)
	adv := advertiseURL()
	// Demand the URL only when something here actually dials back on it. A
	// deploy whose every attachment the server realizes itself (env-kind
	// credentials, with no mounts and no egress) still routes through this path
	// so that each backend keeps its own realization — kubernetes materializes a
	// Secret rather than a pod-spec literal — but it must not be gated on the
	// address of a caretaker that will never exist.
	if adv == "" {
		if what := companionAttachments(sess, can); what != "" {
			return api.DeployStatus{}, nil, fmt.Errorf("%s on the %s backend require CORNUS_ADVERTISE_URL (the cornus URL the caretaker dials back on)",
				what, backend.Name())
		}
	}
	id := s.mountSessionID(r.Context(), backend, sess.Spec.Spec.Name)
	cleanup := s.registerAttachSession(r.Context(), id, sess.Spec.Spec.Name, sess)

	spec := sess.Spec.Spec
	image := agentImage()

	mounts := make([]deploy.AttachMount, 0, len(sess.Spec.LocalMounts))
	for _, lm := range sess.Spec.LocalMounts {
		if lm.Index < 0 || lm.Index >= len(spec.Mounts) {
			cleanup()
			return api.DeployStatus{}, nil, fmt.Errorf("local mount index %d out of range (%d mounts)", lm.Index, len(spec.Mounts))
		}
		mounts = append(mounts, deploy.AttachMount{
			Target:     spec.Mounts[lm.Index].Target,
			ReadOnly:   lm.ReadOnly,
			AsyncCache: lm.WritableCacheable(),
			Session:    id,
			Name:       lm.Name,
			RelayURL:   adv,
			AgentImage: image,
		})
	}

	creds, err := buildAttachCredentials(sess, spec, id, adv, image, can)
	if err != nil {
		cleanup()
		return api.DeployStatus{}, nil, err
	}

	var egress *deploy.AttachEgress
	if needsEgressRelay(spec.Egress) {
		egress = &deploy.AttachEgress{
			Session:    id,
			RelayURL:   adv,
			AgentImage: image,
			Spec:       spec.Egress,
		}
	}

	status, err := backend.ApplyWithAttachments(r.Context(), spec, mounts, creds, egress)
	if err != nil {
		cleanup()
		return api.DeployStatus{}, nil, err
	}

	return status, cleanup, nil
}

// backendBindsCredentialDir reports whether backend resolves host paths this
// server writes, so a file delivery can be realized as an ordinary read-only
// mount instead of by a caretaker. False for kubernetes and incus, which do not
// implement it, and false for a host backend in remote mode, whose daemon may be
// on another machine where the server's path names nothing.
func backendBindsCredentialDir(ctx context.Context, backend deploy.Backend) bool {
	b, ok := backend.(deploy.CredentialBinder)
	return ok && b.BindsCredentialDir(ctx)
}

// realizeCoLocatedCredentials finishes the credential half of a co-located
// deploy: split the sources under this backend's capability, assert nothing is
// left that would need a caretaker, merge the env deliveries, and drop the spec's
// credential block.
//
// It is a function rather than four statements inline so it can be tested
// against the composition that actually failed. The guard reads the split's
// output, and the split has to be told the SAME capability the rest of the path
// acted on; when it was told a hardcoded `false` instead, a file delivery the
// server had already materialized was re-classified as caretaker-bound and every
// such deploy died on the "which serves none" error — with the credential
// correctly written and mounted. Nothing caught it because no test called the
// enclosing function at all.
func realizeCoLocatedCredentials(sess *deploywire.ServerSession, spec api.DeploySpec, can deploy.ServerDelivers) (api.DeploySpec, error) {
	// Session/relay/image are empty on purpose: the dispatch only routes here
	// when no delivery needs a companion, so the runtime coordinates would have
	// nothing to address.
	creds, err := buildAttachCredentials(sess, spec, "", "", "", can)
	if err != nil {
		return spec, err
	}
	if kinds := deploy.CredentialRuntimeKinds(creds); len(kinds) > 0 {
		// Unreachable via the dispatch, which tests the same predicate on the
		// spec. Kept because the two read different shapes of the same data, and
		// a drift between them must not silently drop a delivery on the floor.
		return spec, fmt.Errorf(
			"internal: %s credential delivery reached the co-located path, which serves none", strings.Join(kinds, "/"))
	}
	spec, err = deploy.WithCredentialEnv(spec, creds)
	if err != nil {
		return spec, err
	}
	// Realized — drop the block so the backends' "this backend ignores
	// credentials" warning cannot fire for credentials that were delivered.
	spec.Credentials = nil
	return spec, nil
}

// serverDelivers collects, in ONE place, what this backend lets the server
// realize without a companion. Every routing decision reads it from here rather
// than re-deriving its own half, so the dispatch, the advertise gate and the
// delivery split cannot come to different conclusions about the same deploy —
// which is the drift that produced the original bug.
func serverDelivers(ctx context.Context, backend deploy.Backend) deploy.ServerDelivers {
	return deploy.ServerDelivers{
		Files:     backendBindsCredentialDir(ctx, backend),
		Endpoints: backendBindsCredentialEndpoints(ctx, backend),
	}
}

// buildAttachCredentials turns spec's credential sources into the per-source
// attachments a backend realizes, splitting each source's deliveries in the one
// place that split is allowed to happen. Three ways out:
//
//   - env is resolved HERE and once, because container environment is fixed at
//     create and there is nothing to serve afterwards;
//   - file, when serverFiles says the server materializes it, leaves NO trace
//     here — it becomes a read-only mount on the spec instead;
//   - everything else stays in Deliveries for a caretaker, which on a host
//     backend means the backend will refuse it.
//
// serverFiles is the backend's CredentialFileWriter capability, threaded in
// rather than re-derived, so kubernetes — which does not implement it — keeps
// file deliveries on the caretaker exactly as before.
//
// Every path that needs the split calls this, so they agree by construction
// rather than by matching `Kind ==` comparisons in several places.
func buildAttachCredentials(sess *deploywire.ServerSession, spec api.DeploySpec, session, relayURL, agentImage string, can deploy.ServerDelivers) ([]deploy.AttachCredential, error) {
	if spec.Credentials == nil {
		return nil, nil
	}
	creds := make([]deploy.AttachCredential, 0, len(spec.Credentials.Sources))
	for _, src := range spec.Credentials.Sources {
		var runtime, envs []api.CredentialDelivery
		for _, d := range src.Deliveries {
			switch {
			case deploy.NeedsCaretaker(d, can):
				runtime = append(runtime, d)
			case d.Kind == "env":
				envs = append(envs, d)
			default:
				// A file delivery this server materializes itself. It leaves no
				// trace on the attachment: prepareCredentialFiles writes it under
				// the mounts dir and adds an ordinary read-only entry to
				// spec.Mounts, so the backend realizes it as it would any bind.
			}
		}
		ac := deploy.AttachCredential{
			Name: src.Name, Session: session, RelayURL: relayURL, AgentImage: agentImage,
			TTL: src.TTL, Deliveries: runtime,
		}
		if len(envs) > 0 {
			// One fetch serves both: a source declaring an env var and a file
			// should hand the workload the same value, not two independently
			// minted ones that a rotating backend could make disagree.
			cred, err := fetchCredentialValue(sess, src.Name)
			if err != nil {
				return nil, fmt.Errorf("fetch credential %q: %w", src.Name, err)
			}
			for _, d := range envs {
				val := pickCredValue(cred, d.ValueKey)
				if val == "" {
					return nil, fmt.Errorf("credential %q has no value for env var %s", src.Name, d.EnvVar)
				}
				ac.EnvVars = append(ac.EnvVars, deploy.CredentialEnvVar{Var: d.EnvVar, Value: val})
			}
		}
		creds = append(creds, ac)
	}
	return creds, nil
}

// fetchCredentialValue opens one credential backing to the client over the held
// deploy-attach session and performs a single fetch — the deploy-time resolution
// for an env-kind delivery. The name is one the spec declared, so it is served by
// the client's backing handler (the same path the caretaker relay uses).
func fetchCredentialValue(sess *deploywire.ServerSession, name string) (credential.Credential, error) {
	backing, err := wire.OpenCredBacking(sess.Mux(), name)
	if err != nil {
		return credential.Credential{}, err
	}
	defer backing.Close()
	return deploywire.FetchCredential(backing, nil)
}

// pickCredValue selects the env value from a credential: the named ValueKey, else
// "value" then "token".
func pickCredValue(cred credential.Credential, key string) string {
	if key != "" {
		return cred.Values[key]
	}
	if v := cred.Values["value"]; v != "" {
		return v
	}
	return cred.Values["token"]
}

// rejectFileMounts fails fast when any caller-local mount is a single file (its
// LocalMount carries a Subpath). The sidecar mount path (kubernetes) propagates a
// 9P DIRECTORY mount into the app container via a shared emptyDir; it cannot place
// a single file at an arbitrary rootfs target the way a host container-runtime bind
// can, so a file mount would otherwise silently surface as a directory. The
// dockerhost host-mount path DOES support file mounts (the runtime binds the file),
// so this guard is only wired into the sidecar/attachment backends.
func rejectFileMounts(mounts []deploywire.LocalMount, backend string) error {
	for _, lm := range mounts {
		if lm.Subpath != "" {
			return fmt.Errorf("single-file client-local mounts (e.g. Compose file-based configs/secrets) are not supported by the %s backend; only directory bind mounts can be realized over the 9P mount sidecar", backend)
		}
	}
	return nil
}

// hostVisibleMountSources rewrites the mountpoints this server just created so
// they name the path the CONTAINER RUNTIME will resolve, not the path this
// process sees.
//
// Only the sources under the server's own mounts dir are touched: those are the
// ones the MountManager just minted, and the only bind sources in the spec that
// mean a path in cornus's mount namespace. A user's bind source is a host path
// by definition — it is the daemon that opens it — so it passes through
// untouched even when cornus is containerized.
//
// An unmappable mountpoint is a hard error, and that is the entire point. The
// runtime would otherwise accept the path, create it fresh, and start the
// workload against an empty directory: the mount silently does nothing, and the
// first sign of trouble is missing data much later.
//
// On every non-containerized server the mapper is the identity, so this is a
// no-op that cannot fail.
func (s *Server) hostVisibleMountSources(spec api.DeploySpec) (api.DeploySpec, error) {
	mountsDir := s.cfg.MountsDir()
	mounts := append([]api.Mount(nil), spec.Mounts...)
	for i, m := range mounts {
		if !underDir(m.Source, mountsDir) {
			continue
		}
		host, ok := s.host.mapper.ToHost(m.Source)
		if !ok {
			return api.DeploySpec{}, fmt.Errorf(
				"client-local mount for %s cannot be realized: this server is containerized and its mount directory %s is not visible to the deploy runtime; "+
					"bind-mount it from the host with rshared propagation (-v <host-path>:%s:rshared) or declare the mapping with %s=%s=<host-path>",
				m.Target, mountsDir, s.cfg.DataDir, hostenv.HostPathMapEnv, s.cfg.DataDir)
		}
		mounts[i].Source = host
	}
	spec.Mounts = mounts
	return spec, nil
}

// underDir reports whether path is dir or lies beneath it, comparing whole path
// components so a sibling directory sharing a name prefix does not match.
func underDir(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	path, dir = filepath.Clean(path), strings.TrimSuffix(filepath.Clean(dir), "/")
	return path == dir || strings.HasPrefix(path, dir+"/")
}

// applyWithHostAttachments realizes, with NO companion caretaker, every
// attachment a co-located server can answer for itself: client-local mounts via
// the kernel 9P fast path, and env-kind credentials fetched once over the held
// session and merged into the container environment. One plain Apply follows.
// The returned cleanup unmounts whatever was mounted.
//
// This is the shape the whole change exists to reach. A caretaker is a relay
// that exists because on kubernetes the server has no other way into the pod;
// here it has one, so injecting a companion per replica would be dialing
// ourselves. Mounts-only deploys route here too with an empty credential set —
// it was applyWithHostMounts before it learned to carry credentials — so the
// co-located path has one implementation rather than two that can disagree.
// can is passed in rather than re-derived. The dispatch already computed it to
// decide this deploy could come here at all, and every step below has to act on
// the SAME answer: the file block, the endpoint block and the final split each
// consult it, and if any one of them disagreed with the dispatch, a delivery the
// server had just realized would be re-classified as caretaker-bound and the
// deploy would fail on an "internal:" guard with the credential correctly in
// place. That is not hypothetical — it is what re-deriving it as `false` did.
func (s *Server) applyWithHostAttachments(r *http.Request, sess *deploywire.ServerSession, backend deploy.Backend, can deploy.ServerDelivers) (api.DeployStatus, func(), error) {
	spec := sess.Spec.Spec
	teardown := func() {}

	// Materialize any file-kind credential under the mounts dir and expose it as
	// an ordinary read-only mount. It goes in BEFORE the 9P block below so the one
	// hostVisibleMountSources call at the end translates client mountpoints and
	// credential directories alike — they are the same kind of thing: a path this
	// server wrote that the RUNTIME has to resolve.
	if can.Files && specHasFileDelivery(spec) {
		id := s.mountSessionID(r.Context(), backend, spec.Name)
		cf, dropFiles, err := s.prepareCredentialFiles(r.Context(), sess, spec, id, backend)
		if err != nil {
			return api.DeployStatus{}, nil, err
		}
		spec.Mounts = append(append([]api.Mount(nil), spec.Mounts...), cf.mounts...)
		// Keep them current for the session's life; a file delivery without
		// refresh is a snapshot, which breaks the short-lived credentials this is
		// most useful for.
		refreshCtx, stopRefresh := context.WithCancel(context.WithoutCancel(r.Context()))
		go s.refreshCredentialFiles(refreshCtx, cf, sess, spec, credentialFileDeadline(spec), backend)
		teardown = func() { stopRefresh(); dropFiles() }
	}

	// Resolve endpoint deliveries to addresses and advertise them through the
	// container's environment. Only the ADDRESSES are settled here: nothing is
	// bound yet, because on dockerhost the namespace to bind in does not exist
	// until the container starts. The env has to be decided now regardless — it
	// is fixed into the create request — which is exactly why assignment and
	// binding are two steps rather than one.
	var endpoints *credentialEndpoints
	if can.Endpoints {
		// Ask BEFORE promising anything, the way client-local mounts ask
		// CanMountLocal. Binding inside the workload's namespace needs
		// CAP_SYS_ADMIN, and a cornus running as an ordinary user on the host
		// does not have it — the daemon's containers are root-owned, so even
		// reading /proc/<pid>/ns/net is refused.
		//
		// Without this the failure lands late and in the wrong place: the deploy
		// succeeds, the workload starts, and the credential endpoint simply never
		// appears while the serve loop retries forever. Refusing here matches
		// what a client-local mount does in the same situation, and says why.
		if specHasEndpointDelivery(spec) {
			if err := canEnterNetns(); err != nil {
				teardown()
				return api.DeployStatus{}, nil, fmt.Errorf(
					"credential endpoint delivery on the %s backend needs the privilege to enter the "+
						"workload's network namespace, which this server does not have: %w; run cornus as root "+
						"(or with CAP_SYS_ADMIN), or use an env or file delivery instead", backend.Name(), err)
			}
		}
		ce, err := prepareCredentialEndpoints(r.Context(), spec)
		if err != nil {
			teardown()
			return api.DeployStatus{}, nil, err
		}
		if spec, err = ce.withEnv(spec); err != nil {
			teardown()
			return api.DeployStatus{}, nil, err
		}
		endpoints = ce
	}

	if len(sess.Spec.LocalMounts) > 0 {
		if err := deploywire.CanMountLocal(); err != nil {
			return api.DeployStatus{}, nil, err
		}
		mm := deploywire.NewMountManager(s.cfg.MountsDir())
		mm.SetMeter(s.mountMeter)
		mm.SetCache(s.fileCache)
		// On a runtime that remaps ids, the caller's ownership is meaningless to
		// the workload: the two are unrelated id spaces, so an untranslated uid
		// lands outside the container's map and every file reads as 65534, the
		// overflow uid, with writes refused. Report the workload's own ids instead.
		//
		// Only when the map actually translates. Where it is the identity (rootful
		// docker, bare, containerd) the caller's real ownership stays visible,
		// which is the long-standing behaviour on those backends and not something
		// this should change.
		if hostUID, hostGID, translated, err := credentialFileHostOwner(r.Context(), spec, backend); err != nil {
			return api.DeployStatus{}, nil, err
		} else if translated {
			mm.SetReportedOwner(uint32(hostUID), uint32(hostGID))
		}
		// Record each mount write-ahead, so one stranded by a crash is
		// recoverable rather than an untraceable mountpoint (see pkg/activity).
		mm.SetRecorder(s.activity)
		rewritten, err := mm.Prepare(sess.Mux(), sess.Spec)
		if err != nil {
			mm.Teardown()
			return api.DeployStatus{}, nil, err
		}
		// mm.Prepare rewrote from the SESSION spec, so re-apply the credential
		// mounts it did not know about, and keep both teardowns. Translation
		// happens BELOW, over the whole spec, so the credential mounts are in the
		// set it sees — appending them after a translation, as this did, meant
		// they were never translated at all.
		creds := spec.Mounts[len(sess.Spec.Spec.Mounts):]
		rewritten.Mounts = append(append([]api.Mount(nil), rewritten.Mounts...), creds...)
		outer := teardown
		spec, teardown = rewritten, func() { mm.Teardown(); outer() }
	}

	// Translate every source this server wrote into the spelling the RUNTIME
	// resolves — client mountpoints and credential directories alike, since they
	// are the same kind of thing. Once, over the final spec, which is what the
	// comment on the credential block above has always claimed happened.
	//
	// It did not. The call used to live inside the 9P branch, so a deploy with no
	// client-local mounts never translated anything, and one WITH them translated
	// a spec that predated the credential mounts. Either way a containerized
	// server with a real CORNUS_HOST_PATH_MAP handed the runtime its own path;
	// the runtime created it fresh and the workload got an EMPTY credential
	// directory — the silent-arrival-empty failure this function exists to make
	// impossible. It survived a green E2E because the runner is co-located and
	// its mapper is the identity.
	//
	// Safe for every caller: only sources under MountsDir are touched, and on a
	// non-containerized server the mapper is the identity.
	spec, err := s.hostVisibleMountSources(spec)
	if err != nil {
		teardown()
		return api.DeployStatus{}, nil, err
	}

	spec, err = realizeCoLocatedCredentials(sess, spec, can)
	if err != nil {
		teardown()
		return api.DeployStatus{}, nil, err
	}

	status, err := backend.Apply(r.Context(), spec)
	if err != nil {
		teardown()
		return api.DeployStatus{}, nil, err
	}

	// Bind and serve the endpoints only now: until Apply returns there is no
	// workload, and on dockerhost no namespace to enter. The serve loops outlive
	// this request and are stopped by the returned teardown when the session ends,
	// which is the same lifetime the file refresh above has.
	if endpoints != nil {
		binder, ok := backend.(deploy.CredentialEndpointBinder)
		if !ok {
			// Unreachable: backendBindsCredentialEndpoints did this type assertion
			// to decide `endpoints` was non-nil. Kept because silently serving
			// nothing would look exactly like a workload that never asked.
			teardown()
			return api.DeployStatus{}, nil, fmt.Errorf(
				"internal: %s backend advertised credential endpoint binding but does not implement it", backend.Name())
		}
		stopEndpoints := s.serveCredentialEndpoints(r.Context(), endpoints, sess, spec, binder)
		outer := teardown
		teardown = func() { stopEndpoints(); outer() }
	}
	return status, teardown, nil
}

// mountPropagationPrecondition refuses a client-local mount the runtime would not
// be able to see.
//
// Without it the failure is SILENT and looks like success: the server makes the
// kernel 9P mount, the runtime binds the underlying directory instead (which
// exists, and is empty precisely because it is a mountpoint), the deploy reports
// running, and the workload reads nothing. Nothing errors anywhere. That cost this
// project a long investigation, and the only reason it was ever noticed is that a
// scenario asserted the mount's FSTYPE rather than its contents.
//
// It is deliberately NOT a propagation check on its own. Rootful docker works with
// private propagation, because its daemon shares this server's mount namespace, so
// refusing on propagation alone would break a working configuration. The question
// is asked only of a backend that says its runtime reaches mounts from another
// namespace (deploy.CrossNamespaceMounter), which is where propagation becomes
// load-bearing.
//
// Only DEFINITIVELY private is refused. "unknown" means this server could not read
// the propagation, which is not evidence of a defect, and hostcheck already warns
// about it at preflight.
func (s *Server) mountPropagationPrecondition(ctx context.Context, backend deploy.Backend) error {
	x, ok := backend.(deploy.CrossNamespaceMounter)
	if !ok || !x.MountsCrossNamespace(ctx) {
		return nil
	}
	dir := s.cfg.MountsDir()
	if s.host.mapper == nil || s.host.mapper.Propagation(dir) != hostenv.PropagationPrivate {
		return nil
	}
	return fmt.Errorf("client-local mounts cannot reach this runtime: it runs containers in a "+
		"different mount namespace than this server, and %s has private propagation, so a mount "+
		"made there is invisible to it — the deploy would come up with an EMPTY mount and no error. "+
		"Give the directory shared propagation (mount --make-rshared / before the runtime starts, "+
		"or bind the data dir with :rshared); a mount namespace joins a peer group only when it is "+
		"created, so this must precede the runtime, not follow it", dir)
}
