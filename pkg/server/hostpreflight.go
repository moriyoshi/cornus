package server

import (
	"context"
	"fmt"
	"os"

	"cornus/pkg/api"
	"cornus/pkg/config"
	"cornus/pkg/deploy/dockerhost"
	"cornus/pkg/hostcheck"
	"cornus/pkg/hostenv"
	"cornus/pkg/logging"
)

// hostSetup is what the server learns at startup about its own relationship to
// the deploy backend's container runtime: whether their paths diverge, how to
// translate between them, and whether the result is usable at all.
type hostSetup struct {
	env    hostenv.Env
	mapper hostenv.Mapper
	result hostcheck.Result
}

// hostInput detects the host environment and assembles the checks for it.
//
// Only the Docker Engine API can be asked "what are THIS container's mounts?",
// so self-inspection is wired for that runtime alone; for every other backend
// the operator declares the mapping with CORNUS_HOST_PATH_MAP or there is none.
// A failure to build the Docker client is therefore not an error here — it just
// leaves the detection with less to go on.
func hostInput(ctx context.Context, cfg config.Config) (hostcheck.Input, error) {
	backend := os.Getenv("CORNUS_DEPLOY_BACKEND")
	opts := hostenv.Options{Runtime: runtimeForBackend(backend)}
	if opts.Runtime == hostenv.RuntimeDocker {
		if insp, err := dockerhost.SelfInspector(); err == nil {
			opts.Inspect = insp
		}
	}
	env, mapper, err := hostenv.Detect(ctx, opts)
	if err != nil {
		return hostcheck.Input{}, err
	}
	return hostcheck.Input{
		Backend:   backend,
		DataDir:   cfg.DataDir,
		MountsDir: cfg.MountsDir(),
		Env:       env,
		Mapper:    mapper,
	}, nil
}

// detectHost resolves the host environment and evaluates it.
//
// The returned error is reserved for what must stop startup: a malformed
// CORNUS_HOST_PATH_MAP, or a configuration in which every deploy would silently
// do the wrong thing (see hostcheck.Result.Failed). A merely degraded
// environment returns no error — its warnings are logged, and the affected
// capability reports its own absence at the point of use.
func detectHost(ctx context.Context, cfg config.Config) (hostSetup, error) {
	in, err := hostInput(ctx, cfg)
	if err != nil {
		return hostSetup{}, err
	}
	res := in.Run()
	if res.Failed() {
		return hostSetup{}, fmt.Errorf("host environment unusable for the %s deploy backend: %s",
			hostcheck.BackendName(in.Backend), firstFailure(res))
	}
	return hostSetup{env: in.Env, mapper: in.Mapper, result: res}, nil
}

// firstFailure renders the fatal check as one actionable sentence.
func firstFailure(res hostcheck.Result) string {
	for _, c := range res.Problems() {
		if c.Status != hostcheck.StatusFail {
			continue
		}
		if c.Hint != "" {
			return c.Detail + " — " + c.Hint
		}
		return c.Detail
	}
	return "unknown"
}

// logHostSetup emits the startup summary and any warnings. Warnings name the
// remedy, because the whole point of this preflight is that the failures it
// covers are otherwise silent.
func (s *Server) logHostSetup(ctx context.Context) {
	log := logging.FromContext(ctx)
	log.InfoContext(ctx, "host environment: "+s.host.result.Summary())
	for _, c := range s.host.result.Problems() {
		log.WarnContext(ctx, "host environment: "+c.Detail, "check", c.Name, "remedy", c.Hint)
	}
}

// isolatedNetwork reports that this server sits in a container with a network
// namespace of its own, so it has no route to a workload's container IP.
//
// Errs toward true when self-inspection could not settle the question: this
// only ever adds an explanatory clause to an error that has already happened,
// so an unnecessary hint costs a line of text while a missing one leaves an
// operator staring at a bare dial timeout.
func (h hostSetup) isolatedNetwork() bool {
	return h.env.InContainer && !(h.env.HostNetworkKnown && h.env.HostNetwork)
}

// mountBindPrefixes are the bind-source prefixes a host backend's default-deny
// policy must permit for the deploy-attach path to work: the server's own
// mounts dir, plus — when this server is containerized away from the runtime —
// the path that same directory appears at on the HOST.
//
// Both are needed because the two are checked at different moments. The server
// rewrites a client-local mount's source to the host path before calling Apply
// (hostVisibleMountSources), so the policy inside the backend sees the host
// spelling; but nothing else in the system stops referring to the local one.
// Permitting only the local prefix would make the backend reject the server's
// own carve-out the moment translation was in effect.
func mountBindPrefixes(cfg config.Config, mapper hostenv.Mapper) []string {
	local := cfg.MountsDir()
	prefixes := []string{local}
	if mapper != nil {
		if host, ok := mapper.ToHost(local); ok && host != local {
			prefixes = append(prefixes, host)
		}
	}
	return prefixes
}

// advertisedHost reports the deployment-dependent capabilities carried by
// /.cornus/v1/info, so a client can say "this server cannot realize
// client-local mounts" up front instead of discovering it from a failed deploy.
//
// Flags only: that endpoint is auth-exempt, so nothing the preflight learned
// about this host's paths or identity may leave through it (see
// api.HostCapabilities).
func (s *Server) advertisedHost() *api.HostCapabilities {
	return &api.HostCapabilities{
		Containerized:     s.host.env.InContainer,
		ClientLocalMounts: s.host.result.Capability(hostcheck.CheckClientMounts),
	}
}

// runtimeForBackend maps a deploy backend to the container runtime whose view
// of the filesystem cornus's paths must make sense in. The backends with no
// such runtime (bare execs its OCI runtime as cornus's own child; kubernetes
// hands the server's paths to nothing) resolve to unknown, and the path checks
// skip them — see hostcheck.
func runtimeForBackend(backend string) string {
	switch backend {
	case "", "dockerhost":
		return hostenv.RuntimeDocker
	case "containerd":
		return hostenv.RuntimeContainerd
	default:
		return hostenv.RuntimeUnknown
	}
}

// HostPreflight reports whether this process can drive the configured deploy
// backend's container runtime, for `cornus daemon preflight` to print. It runs
// the same detection and the same checks `cornus serve` does at startup, so the
// command answers for the real server rather than an approximation of it.
func HostPreflight(ctx context.Context, cfg config.Config) (hostcheck.Result, error) {
	in, err := hostInput(ctx, cfg)
	if err != nil {
		return hostcheck.Result{}, err
	}
	return in.Run(), nil
}
