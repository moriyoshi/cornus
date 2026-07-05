//go:build linux

package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/containerd/containerd/content"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"

	"cornus/pkg/build/buildprog"
	"cornus/pkg/build/internal/lazyctx"
	"cornus/pkg/wire"
)

// Build runs a single Dockerfile build from local directories. Progress events
// are delivered to progress (a nil Sink is fine). It is a thin wrapper over Solve.
func (e *Engine) Build(ctx context.Context, req Request, progress buildprog.Sink) (*Result, error) {
	if req.ContextDir == "" {
		return nil, fmt.Errorf("builder: empty context dir")
	}
	dfName := req.Dockerfile
	if dfName == "" {
		dfName = "Dockerfile"
	}
	dfDir := filepath.Dir(filepath.Join(req.ContextDir, dfName))

	contextFS, err := fsutil.NewFS(req.ContextDir)
	if err != nil {
		return nil, fmt.Errorf("builder: context fs: %w", err)
	}
	dfFS, err := fsutil.NewFS(dfDir)
	if err != nil {
		return nil, fmt.Errorf("builder: dockerfile fs: %w", err)
	}

	// The lazy path relies on the runc worker's in-process snapshotter plumbing;
	// reject it before preparing any lazy context on the containerd worker.
	if e.workerKind == WorkerContainerd && len(req.NamedContexts) > 0 && (req.Lazy || lazyBuildEnabled()) {
		return nil, fmt.Errorf("builder: lazy build contexts (--lazy / %s) are not supported with the containerd build worker", lazyBuildEnv)
	}

	mounts := map[string]fsutil.FS{"context": contextFS, "dockerfile": dfFS}
	var lazy []*lazyctx.LazyContext
	var servers []*wire.DirServer
	defer func() {
		for _, srv := range servers {
			// Machine-readable marker in the build log; build-lazy-9p.star
			// parses "CORNUS-9P served N bytes" to prove the lazy 9p path ran.
			progress.Log("CORNUS-9P served %d bytes\n", srv.ReadBytes())
			srv.Close()
		}
	}()
	for name, dir := range req.NamedContexts {
		// When lazy builds are on, serve the named context on demand (oci-layout +
		// "stargz" remote snapshotter) instead of eagerly syncing it into a
		// snapshot. Backing is a host-dir bind by default, or a kernel-9p mount of
		// an in-process p9 server when CORNUS_LAZY_9P is set (to measure the pull).
		if req.Lazy || lazyBuildEnabled() {
			backing := dir
			if lazy9pEnabled() {
				srv, err := wire.ServeContextDir(dir)
				if err != nil {
					return nil, fmt.Errorf("builder: 9p serve %q: %w", name, err)
				}
				servers = append(servers, srv)
				backing = "9p:" + srv.Socket()
			}
			ignore, err := dockerignoreFor(dir)
			if err != nil {
				return nil, fmt.Errorf("builder: lazy build-context %q: %w", name, err)
			}
			lc, err := lazyctx.Prepare(name, dir, backing, ignore)
			if err != nil {
				return nil, fmt.Errorf("builder: lazy build-context %q: %w", name, err)
			}
			lazy = append(lazy, lc)
			continue
		}
		fs, err := fsutil.NewFS(dir)
		if err != nil {
			return nil, fmt.Errorf("builder: build-context %q: %w", name, err)
		}
		mounts[name] = fs
	}

	in := SolveInput{
		Target:              req.Target,
		TargetStage:         req.TargetStage,
		DockerfileName:      filepath.Base(dfName),
		BuildArgs:           req.BuildArgs,
		Mounts:              mounts,
		LazyContexts:        lazy,
		Push:                req.Push,
		Insecure:            req.Insecure,
		NoCache:             req.NoCache,
		CacheExports:        req.CacheExports,
		CacheImports:        req.CacheImports,
		DockerArchiveOutput: req.DockerArchiveOutput,
	}
	if len(req.Secrets) > 0 {
		store, err := buildSecretsStore(req.Secrets)
		if err != nil {
			return nil, err
		}
		in.Secrets = store
	}
	if len(req.SSH) > 0 {
		confs := make([]sshprovider.AgentConfig, 0, len(req.SSH))
		for _, s := range req.SSH {
			confs = append(confs, sshprovider.AgentConfig{ID: s.ID, Paths: []string{s.Socket}})
		}
		sp, err := sshprovider.NewSSHAgentProvider(confs)
		if err != nil {
			return nil, fmt.Errorf("builder: ssh provider: %w", err)
		}
		in.SSH = sp
	}

	return e.Solve(ctx, in, progress)
}

// Solve runs a build from caller-supplied mounts and an optional secret store
// (local directories for the CLI, or 9P-backed sources for a remote build).
func (e *Engine) Solve(ctx context.Context, in SolveInput, progress buildprog.Sink) (*Result, error) {
	if in.Mounts["context"] == nil {
		return nil, fmt.Errorf("builder: missing context mount")
	}
	// Defensive mirror of Build's gate for callers (e.g. the remote build path)
	// that hand LazyContexts to Solve directly.
	if len(in.LazyContexts) > 0 && e.workerKind == WorkerContainerd {
		return nil, fmt.Errorf("builder: lazy build contexts (--lazy / %s) are not supported with the containerd build worker", lazyBuildEnv)
	}

	// Pre-create each lazy context's ref and seed its contenthash before the
	// solve (holding the refs across it so the solve's own GetByBlob returns the
	// same record). This makes BuildKit skip the RUN cache-key scan of the lazy
	// mount — no content pulled. Runs for both local and remote lazy builds.
	if len(in.LazyContexts) > 0 {
		held, err := e.preseedLazyRefs(ctx, in.LazyContexts)
		if err != nil {
			return nil, err
		}
		defer func() {
			for _, r := range held {
				_ = r.Release(context.WithoutCancel(ctx))
			}
		}()
	}

	attrs := frontendAttrs(in)
	// Lazy named contexts resolve to an oci-layout image served over the session,
	// so the "stargz" remote snapshotter mounts them on demand.
	var ociStores map[string]content.Store
	for _, lc := range in.LazyContexts {
		if ociStores == nil {
			ociStores = map[string]content.Store{}
		}
		attrs["context:"+lc.Name] = lc.ContextAttr
		ociStores[lc.StoreID] = lc.Store
	}

	// The image exporter's "name" attr is a comma-separated ref list: the primary
	// Target plus any additional build.tags. All listed refs are tagged (and
	// pushed when Push is set), verbatim — Tags is used as-is here, so any
	// registry-host redirect (e.g. a NodePort's advertised host rewritten to the
	// co-located loopback registry) must already have been applied to every
	// entry by the caller before it reaches SolveInput; see
	// Server.localPushTargets in pkg/server.
	names := in.Target
	for _, t := range in.Tags {
		if t = strings.TrimSpace(t); t != "" {
			names += "," + t
		}
	}
	imgAttrs := map[string]string{"name": names}
	if in.Push {
		imgAttrs["push"] = "true"
	}
	if in.Insecure {
		imgAttrs["registry.insecure"] = "true"
	}

	// Export selection: a docker-archive to a caller writer (docker-daemon
	// re-export mode, loaded into the local daemon via POST /images/load), or the
	// default registry image exporter. The docker exporter carries the same name
	// list so the daemon tags the image exactly as it will be deployed.
	exports := []client.ExportEntry{{Type: client.ExporterImage, Attrs: imgAttrs}}
	if in.DockerArchiveOutput != nil {
		exports = []client.ExportEntry{{
			Type:   client.ExporterDocker,
			Attrs:  map[string]string{"name": names},
			Output: in.DockerArchiveOutput,
		}}
	}

	sessions := []session.Attachable{defaultAuthProvider()}
	if in.Secrets != nil {
		sessions = append(sessions, secretsprovider.NewSecretProvider(in.Secrets))
	}
	if in.SSH != nil {
		sessions = append(sessions, in.SSH)
	}

	// type=local cache dest/src are treated as engine-managed keys (pseudo-paths)
	// resolved under <root>/localcache, so a caller (local or remote) can name a
	// cache without knowing the engine's on-disk layout. Other backends pass through.
	cacheRoot := localCacheRoot(e.root)
	cacheExports, err := resolveLocalCacheOpts(in.CacheExports, cacheRoot, in.Target, "dest")
	if err != nil {
		return nil, err
	}
	cacheImports, err := resolveLocalCacheOpts(in.CacheImports, cacheRoot, in.Target, "src")
	if err != nil {
		return nil, err
	}

	solveOpt := client.SolveOpt{
		Frontend:      "dockerfile.v0",
		FrontendAttrs: attrs,
		LocalMounts:   in.Mounts,
		OCIStores:     ociStores,
		Session:       sessions,
		Exports:       exports,
		CacheExports:  cacheEntries(cacheExports),
		CacheImports:  cacheEntries(cacheImports),
	}

	ch := make(chan *client.SolveStatus)
	var resp *client.SolveResponse

	eg, gctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		var err error
		resp, err = e.client.Solve(gctx, nil, solveOpt, ch)
		return err
	})
	eg.Go(func() error {
		drainProgress(ch, progress)
		return nil
	})
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	res := &Result{}
	if resp != nil {
		if d, ok := resp.ExporterResponse["containerimage.digest"]; ok {
			res.ImageDigest = d
		}
	}
	return res, nil
}

// frontendAttrs builds the dockerfile.v0 frontend attributes for a solve:
// the Dockerfile name, the optional multi-stage target stage, build args,
// no-cache, and each eagerly-synced named build context. Lazy named contexts
// (served over an oci-layout image) are added by the caller. Kept as a pure
// function so the option mapping is unit-testable without running a build.
func frontendAttrs(in SolveInput) map[string]string {
	dfName := in.DockerfileName
	if dfName == "" {
		dfName = "Dockerfile"
	}
	attrs := map[string]string{"filename": dfName}
	// TargetStage selects a multi-stage build target (docker build --target /
	// compose build.target). The dockerfile frontend reads it from "target".
	if in.TargetStage != "" {
		attrs["target"] = in.TargetStage
	}
	for k, v := range in.BuildArgs {
		attrs["build-arg:"+k] = v
	}
	if in.NoCache {
		attrs["no-cache"] = ""
	}
	// build.pull -> always resolve the base image from the registry.
	if in.Pull {
		attrs["image-resolve-mode"] = "pull"
	}
	// build.platforms -> the dockerfile frontend's comma-joined "platform" attr.
	// Multiple platforms request a multi-platform image (needs worker emulators).
	if len(in.Platforms) > 0 {
		attrs["platform"] = strings.Join(in.Platforms, ",")
	}
	// build.network -> the RUN sandbox network mode. buildkit accepts
	// none/host/sandbox; map compose's "default" onto "sandbox" and pass the
	// others through (an unrecognised value is left verbatim for buildkit to
	// reject rather than silently dropped).
	if in.Network != "" {
		nm := in.Network
		if nm == "default" {
			nm = "sandbox"
		}
		attrs["force-network-mode"] = nm
	}
	// build.extra_hosts -> the "add-hosts" attr, a csv of host=ip pairs. Compose
	// normalises entries to "host:ip"; rewrite the first ':' to '=' (an IPv6 value
	// keeps its own colons, which buildkit's net.ParseIP handles).
	if len(in.ExtraHosts) > 0 {
		pairs := make([]string, 0, len(in.ExtraHosts))
		for _, h := range in.ExtraHosts {
			host, ip, ok := strings.Cut(h, ":")
			if !ok {
				continue
			}
			pairs = append(pairs, host+"="+ip)
		}
		if len(pairs) > 0 {
			attrs["add-hosts"] = strings.Join(pairs, ",")
		}
	}
	// build.shm_size -> the "shm-size" attr (bytes, decimal).
	if in.ShmSize > 0 {
		attrs["shm-size"] = strconv.FormatInt(in.ShmSize, 10)
	}
	// build.labels -> "label:<k>=<v>" attrs applied to the built image config.
	for k, v := range in.Labels {
		attrs["label:"+k] = v
	}
	// Each extra mount is a named build context (RUN --mount=type=bind,from=<name>).
	for name := range in.Mounts {
		if name == "context" || name == "dockerfile" {
			continue
		}
		attrs["context:"+name] = "local:" + name
	}
	return attrs
}

// cacheEntries maps cornus CacheOptions to BuildKit CacheOptionsEntry.
func cacheEntries(opts []CacheOption) []client.CacheOptionsEntry {
	if len(opts) == 0 {
		return nil
	}
	out := make([]client.CacheOptionsEntry, 0, len(opts))
	for _, o := range opts {
		out = append(out, client.CacheOptionsEntry{Type: o.Type, Attrs: o.Attrs})
	}
	return out
}

// dockerignoreFor loads <dir>/.dockerignore (if present) and returns a
// lazyctx.Ignore that mirrors the remote path's rule exactly (buildwire's
// loadDockerignore + ignoreFunc): the .dockerignore file itself is always kept,
// and every other path matched by the compiled patterns is excluded. Returns
// nil when there is no .dockerignore or it declares no patterns.
func dockerignoreFor(dir string) (lazyctx.Ignore, error) {
	f, err := os.Open(filepath.Join(dir, ".dockerignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	patterns, err := ignorefile.ReadAll(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("builder: parse .dockerignore: %w", err)
	}
	if len(patterns) == 0 {
		return nil, nil
	}
	m, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, err
	}
	return func(rel string) bool {
		if rel == "" || rel == ".dockerignore" {
			return false
		}
		matched, err := m.MatchesOrParentMatches(filepath.FromSlash(rel))
		return err == nil && matched
	}, nil
}

// drainProgress consumes BuildKit's status stream and projects it into neutral
// buildprog.Events for the caller's sink to render (the CLI owns the display;
// this package no longer renders). Each vertex reports a single start and a
// single terminal transition (done/cached/error), deduplicated by digest since
// BuildKit re-sends a vertex on every status tick; every VertexLog is forwarded
// verbatim so a RUN command's stdout survives as a substring (the E2E harness
// greps for it). It always drains ch to completion — even for a nil sink — so
// the solve goroutine never blocks.
func drainProgress(ch chan *client.SolveStatus, sink buildprog.Sink) {
	started := map[string]bool{}
	done := map[string]bool{}
	for st := range ch {
		for _, v := range st.Vertexes {
			key := string(v.Digest)
			if v.Started != nil && !started[key] {
				started[key] = true
				sink.Call(buildprog.Event{Vertex: v.Name, Status: "start"})
			}
			if v.Completed != nil && !done[key] {
				done[key] = true
				switch {
				case v.Error != "":
					sink.Call(buildprog.Event{Vertex: v.Name, Status: "error", Error: v.Error})
				case v.Cached:
					sink.Call(buildprog.Event{Vertex: v.Name, Status: "cached"})
				default:
					sink.Call(buildprog.Event{Vertex: v.Name, Status: "done"})
				}
			}
		}
		for _, l := range st.Logs {
			sink.Call(buildprog.Event{Log: string(l.Data)})
		}
	}
}
