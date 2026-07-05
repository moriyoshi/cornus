package server

import (
	"io"
	"net/http"

	"cornus/pkg/build/builder"
	"cornus/pkg/build/buildwire"
)

// handleBuildAttach serves GET /.cornus/v1/build/attach: it upgrades to a WebSocket and
// runs a remote build whose context, named-context directories, and secrets are
// served by the caller over 9P. The build stays BuildKit-native (caches remain
// server-side); only the caller's inputs travel over the wire.
func (s *Server) handleBuildAttach(w http.ResponseWriter, r *http.Request) {
	// A build-attach session runs a full build (engine.Solve, which can push
	// images), so it is gated on the "build" action exactly like POST /.cornus/v1/build —
	// otherwise a policy that restricts "build" could be bypassed via this
	// WebSocket. Checked before the upgrade so a denied caller gets a real 403.
	if !s.apiPolicy.Allow(Identity(r), "build") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to build"})
		return
	}

	// When an upstream builder is configured, this server does not build at all:
	// it splices the caller straight through to the builder (see
	// relayBuildAttach). Done before the upgrade so the raw WebSocket — not a
	// yamux session — is what gets relayed. The build semaphore is deliberately
	// NOT taken here: it bounds OUR BuildKit worker, and we run none.
	upstream, err := s.resolveBuilder(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if upstream != "" {
		s.relayBuildAttach(w, r, upstream)
		return
	}

	sess, err := buildwire.Attach(w, r)
	if err != nil {
		// The connection is already hijacked on most failures; nothing useful to
		// write back.
		return
	}
	defer sess.Close()

	// Bound concurrent builds against the shared engine, sharing the same
	// semaphore as POST /.cornus/v1/build: block (queue) until a slot is free, or bail if
	// the client disconnects while waiting. Without this the attach path bypasses
	// the concurrency limit and can starve the shared BuildKit worker.
	select {
	case s.buildSem <- struct{}{}:
		defer func() { <-s.buildSem }()
	case <-r.Context().Done():
		return
	}

	engine, err := s.getEngine()
	if err != nil {
		_ = sess.Done(nil, err)
		return
	}

	sshAgent, sshCleanup, err := sess.SSH()
	if err != nil {
		_ = sess.Done(nil, err)
		return
	}
	defer sshCleanup()

	lazy, lazyCleanup, err := sess.LazyBackings(s.fileCache)
	if err != nil {
		_ = sess.Done(nil, err)
		return
	}
	defer lazyCleanup()

	// Redirect a push at our own advertised registry to the co-located registry
	// over loopback (the in-pod build engine cannot reach a NodePort's
	// localhost:<nodePort>); the deploy pull ref keeps the advertised host. Both
	// the primary Target and every additional Tag are redirected together — a
	// compose build-group shares one build across several services' tags, and
	// only the group's first member is the Target, so the other members' tags
	// need the same rewrite or they stay pointed at the unreachable advertised
	// host from inside the build pod.
	target, tags := sess.Spec.Target, sess.Spec.Tags
	// docker-daemon host-native has no push-able registry, so the build lands in
	// the daemon via a docker-archive streamed into POST /images/load (Target/Tags
	// verbatim). Every other mode pushes normally (the containerd store imports the
	// push; the CAS stores it), redirecting an advertised-registry push to the
	// co-located loopback registry (see localPushTargets).
	var dockerArchiveOut func(map[string]string) (io.WriteCloser, error)
	var loadWait func() error
	push := sess.Spec.Push
	if s.registrySource == registrySourceDockerDaemon {
		push = false
		dockerArchiveOut, loadWait = s.dockerLoadExport(r.Context())
	} else {
		target, tags = s.localPushTargets(r.Context(), sess.Spec.Target, sess.Spec.Tags, sess.Spec.Push)
	}
	in := builder.SolveInput{
		Target:              target,
		TargetStage:         sess.Spec.TargetStage,
		DockerfileName:      sess.Spec.DockerfileName,
		BuildArgs:           sess.Spec.BuildArgs,
		Mounts:              sess.Mounts(),
		LazyContexts:        lazy,
		Secrets:             sess.Secrets(),
		SSH:                 sshAgent,
		CacheExports:        cacheOpts(sess.Spec.CacheExports),
		CacheImports:        cacheOpts(sess.Spec.CacheImports),
		Push:                push,
		Insecure:            sess.Spec.Insecure,
		NoCache:             sess.Spec.NoCache,
		Pull:                sess.Spec.Pull,
		Labels:              sess.Spec.Labels,
		Platforms:           sess.Spec.Platforms,
		Tags:                tags,
		Network:             sess.Spec.Network,
		ExtraHosts:          sess.Spec.ExtraHosts,
		ShmSize:             sess.Spec.ShmSize,
		DockerArchiveOutput: dockerArchiveOut,
	}
	res, buildErr := engine.Solve(r.Context(), in, sess.Progress())
	if loadWait != nil {
		if lerr := loadWait(); lerr != nil && buildErr == nil {
			buildErr = lerr
		}
	}

	var wireRes *buildwire.Result
	if res != nil {
		wireRes = &buildwire.Result{ImageDigest: res.ImageDigest}
	}
	_ = sess.Done(wireRes, buildErr)
}

// cacheOpts converts wire cache options into the builder's form.
func cacheOpts(opts []buildwire.CacheOption) []builder.CacheOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]builder.CacheOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, builder.CacheOption{Type: o.Type, Attrs: o.Attrs})
	}
	return out
}
