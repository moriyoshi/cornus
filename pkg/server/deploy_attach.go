package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/credential"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
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
		_ = sess.Event(deploywire.Event{Err: "spec requires name and image", Done: true})
		return
	}

	backend, err := s.getBackend()
	if err != nil {
		_ = sess.Event(deploywire.Event{Err: "deploy backend unavailable: " + err.Error(), Done: true})
		return
	}

	hasLocal := len(sess.Spec.LocalMounts) > 0
	hasCreds := len(sess.Spec.CredentialSources) > 0
	hasEgress := needsEgressRelay(spec.Egress)
	var (
		status  api.DeployStatus
		cleanup func()
	)
	switch {
	case !hasLocal && !hasCreds && !hasEgress:
		status, err = backend.Apply(r.Context(), spec)
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
			what := "client-sourced credentials"
			if hasEgress {
				what = "client-side egress"
			}
			_ = sess.Event(deploywire.Event{Err: what + " is not yet supported by the " + backend.Name() + " backend", Done: true})
			return
		}
	default: // mounts only
		if mb, ok := backend.(deploy.MountingBackend); ok && useSidecarMounts(backend) {
			status, cleanup, err = s.applyWithSidecarMounts(r, sess, mb)
		} else if backend.Name() == "dockerhost" || backend.Name() == "bare" {
			// Co-located host backends whose runtime shares the server's mount
			// namespace: the server kernel-9P-mounts each client export under
			// <DataDir>/mounts and rewrites Mount.Source, and the backend binds the
			// mountpoint like any host path (bare runs runc as the server's own
			// child, so the mount is directly visible to the container).
			status, cleanup, err = s.applyWithHostMounts(r, sess, backend)
		} else {
			_ = sess.Event(deploywire.Event{Err: "client-local mounts are not supported by the " + backend.Name() + " backend", Done: true})
			return
		}
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	if err != nil {
		cleanup()
		_ = sess.Event(deploywire.Event{Err: err.Error(), Done: true})
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
		s.tunnels.stop(spec.Name)
		_ = backend.Delete(context.Background(), spec.Name)
		cleanup()
		_ = sess.Event(deploywire.Event{Err: err.Error(), Done: true})
		return
	}
	_ = sess.Event(deploywire.Event{Status: &status, Ready: true, Log: "deployed " + spec.Name + "\n"})

	// Block until the caller disconnects or requests teardown.
	sess.Wait()

	// A tunnel opened for this deployment name must be torn down too, mirroring
	// the DELETE handler in handleDeployItem — otherwise it outlives the ephemeral
	// deployment as a leaked serve() goroutine and an open public relay endpoint
	// (and would silently re-expose any later deployment that reuses the name).
	s.tunnels.stop(spec.Name)
	// Remove the workload first (releases any bind), then run the mount cleanup.
	_ = backend.Delete(context.Background(), spec.Name)
	cleanup()
	_ = sess.Event(deploywire.Event{Done: true})
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
// host-mount fallback to prefer instead, so it is always true there. dockerhost/
// containerdhost implement MountingBackend unconditionally (so the type
// assertion always succeeds) but must opt in via RemoteCapable.Remote() before
// the server steals their deploys away from the existing, simpler co-located
// applyWithHostMounts path — daemon co-location can't be detected automatically,
// so this is never inferred.
func useSidecarMounts(backend deploy.Backend) bool {
	if rc, ok := backend.(deploy.RemoteCapable); ok {
		return rc.Remote()
	}
	return true
}

// applyWithSidecarMounts registers the session for the mount relay and applies
// the spec with per-mount AttachMounts so a MountingBackend (kubernetes) injects
// live 9P sidecars. The returned cleanup unregisters the session.
func (s *Server) applyWithSidecarMounts(r *http.Request, sess *deploywire.ServerSession, backend deploy.MountingBackend) (api.DeployStatus, func(), error) {
	if err := rejectFileMounts(sess.Spec.LocalMounts, backend.Name()); err != nil {
		return api.DeployStatus{}, nil, err
	}
	adv := os.Getenv("CORNUS_ADVERTISE_URL")
	if adv == "" {
		return api.DeployStatus{}, nil, fmt.Errorf("client-local mounts on the %s backend require CORNUS_ADVERTISE_URL (the in-cluster cornus URL the pod mount-agent dials)", backend.Name())
	}
	id := newSessionID()
	s.mounts.put(id, sess)
	// With a distributed hub store, also advertise which replica holds this
	// session so a caretaker relaying via another replica can be forwarded here
	// (no-op on the single-replica in-memory store).
	s.registerMountSession(id)
	cleanup := func() {
		s.mounts.del(id)
		s.unregisterMountSession(id)
	}

	spec := sess.Spec.Spec
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
			AgentImage: os.Getenv("CORNUS_AGENT_IMAGE"),
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
	adv := os.Getenv("CORNUS_ADVERTISE_URL")
	if adv == "" {
		return api.DeployStatus{}, nil, fmt.Errorf("client-side egress on the %s backend requires CORNUS_ADVERTISE_URL (the cornus URL the companion caretaker dials for the relay)", backend.Name())
	}
	id := newSessionID()
	s.mounts.put(id, sess)
	s.registerMountSession(id)
	cleanup := func() {
		s.mounts.del(id)
		s.unregisterMountSession(id)
	}
	spec := sess.Spec.Spec
	egress := &deploy.AttachEgress{
		Session:    id,
		RelayURL:   adv,
		AgentImage: os.Getenv("CORNUS_AGENT_IMAGE"),
		Spec:       spec.Egress,
	}
	status, err := backend.ApplyWithEgress(r.Context(), spec, egress)
	if err != nil {
		cleanup()
		return api.DeployStatus{}, nil, err
	}
	return status, cleanup, nil
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
	adv := os.Getenv("CORNUS_ADVERTISE_URL")
	if adv == "" {
		return api.DeployStatus{}, nil, fmt.Errorf("client-sourced credentials on the %s backend require CORNUS_ADVERTISE_URL (the in-cluster cornus URL the pod caretaker dials)", backend.Name())
	}
	id := newSessionID()
	s.mounts.put(id, sess)
	s.registerMountSession(id)
	cleanup := func() {
		s.mounts.del(id)
		s.unregisterMountSession(id)
	}

	spec := sess.Spec.Spec
	agentImage := os.Getenv("CORNUS_AGENT_IMAGE")

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
			AgentImage: agentImage,
		})
	}

	var creds []deploy.AttachCredential
	if spec.Credentials != nil {
		creds = make([]deploy.AttachCredential, 0, len(spec.Credentials.Sources))
		for _, src := range spec.Credentials.Sources {
			// Split runtime deliveries (served by the caretaker) from env
			// deliveries (fixed at container start → resolved here, once).
			var runtime, envs []api.CredentialDelivery
			for _, d := range src.Deliver {
				if d.Kind == "env" {
					envs = append(envs, d)
				} else {
					runtime = append(runtime, d)
				}
			}
			ac := deploy.AttachCredential{
				Name: src.Name, Session: id, RelayURL: adv, AgentImage: agentImage,
				TTL: src.TTL, Deliver: runtime,
			}
			if len(envs) > 0 {
				cred, err := fetchCredentialValue(sess, src.Name)
				if err != nil {
					cleanup()
					return api.DeployStatus{}, nil, fmt.Errorf("fetch credential %q for env delivery: %w", src.Name, err)
				}
				for _, d := range envs {
					val := pickCredValue(cred, d.ValueKey)
					if val == "" {
						cleanup()
						return api.DeployStatus{}, nil, fmt.Errorf("credential %q has no value for env var %s", src.Name, d.EnvVar)
					}
					ac.EnvVars = append(ac.EnvVars, deploy.CredentialEnvVar{Var: d.EnvVar, Value: val})
				}
			}
			creds = append(creds, ac)
		}
	}

	var egress *deploy.AttachEgress
	if needsEgressRelay(spec.Egress) {
		egress = &deploy.AttachEgress{
			Session:    id,
			RelayURL:   adv,
			AgentImage: agentImage,
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

// applyWithHostMounts kernel-9p-mounts each caller-local mount on this host and
// rewrites the spec before Apply (dockerhost single-host path). The returned
// cleanup unmounts them.
func (s *Server) applyWithHostMounts(r *http.Request, sess *deploywire.ServerSession, backend deploy.Backend) (api.DeployStatus, func(), error) {
	if err := deploywire.CanMountLocal(); err != nil {
		return api.DeployStatus{}, nil, err
	}
	mm := deploywire.NewMountManager(s.cfg.MountsDir())
	mm.SetMeter(s.mountMeter)
	mm.SetCache(s.fileCache)
	rewritten, err := mm.Prepare(sess.Mux(), sess.Spec)
	if err != nil {
		mm.Teardown()
		return api.DeployStatus{}, nil, err
	}
	status, err := backend.Apply(r.Context(), rewritten)
	if err != nil {
		mm.Teardown()
		return api.DeployStatus{}, nil, err
	}
	return status, mm.Teardown, nil
}
