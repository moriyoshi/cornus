package composecli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/build/buildprog"
	"cornus/pkg/client"
	"cornus/pkg/compose"
	"cornus/pkg/imageref"

	"golang.org/x/sync/errgroup"
)

// buildExecutor is the slice of *client.Client that runBuildGroups needs; a
// narrow interface keeps the concurrent build orchestration unit-testable with
// a scripted fake, mirroring statusPoller/deploymentDeleter elsewhere in this
// package.
type buildExecutor interface {
	Build(ctx context.Context, req client.BuildRequest, progress buildprog.Sink) error
}

var _ buildExecutor = (*client.Client)(nil)

// buildOverrides carries CLI build overrides (compose build --no-cache /
// --build-arg) into resolveBuildRequest. The zero value — what `up --build`
// passes — changes nothing.
type buildOverrides struct {
	noCache bool
	args    map[string]string // merged over the compose build.args
}

// resolveBuildRequest resolves a service's build plan into a client.BuildRequest
// — the context dir, additional build contexts, and secret file paths made
// absolute against the project directory; cliSSH (the command's --ssh
// overrides) merged over the compose file's build.ssh; ov's --build-arg /
// --no-cache merged over / OR'd with the compose build.args / build.no_cache —
// plus the service's primary registry tag and any extra compose build.tags,
// qualified against the resolved registry host. It performs no build I/O (the
// one network call, registryHostFor, is memoized after the first resolution).
func (r *runtime) resolveBuildRequest(ctx context.Context, plan compose.ServicePlan, cliSSH []string, ov buildOverrides) (client.BuildRequest, error) {
	bp := plan.Build
	contextDir := bp.Context
	if contextDir == "" {
		contextDir = "."
	}
	if !filepath.IsAbs(contextDir) {
		contextDir = filepath.Join(r.baseDir, contextDir)
	}
	// Resolve additional build contexts and secret file paths relative to the
	// project directory, mirroring how the main context is resolved.
	var buildContexts map[string]string
	for name, dir := range bp.AdditionalContexts {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(r.baseDir, dir)
		}
		if buildContexts == nil {
			buildContexts = map[string]string{}
		}
		buildContexts[name] = dir
	}
	var secrets map[string]string
	for id, path := range bp.Secrets {
		if !filepath.IsAbs(path) {
			path = filepath.Join(r.baseDir, path)
		}
		if secrets == nil {
			secrets = map[string]string{}
		}
		secrets[id] = path
	}
	ssh, err := resolveBuildSSH(bp.SSH, cliSSH)
	if err != nil {
		return client.BuildRequest{}, err
	}
	// --build-arg entries override the compose build.args (copy so the plan's map
	// is never mutated).
	args := bp.Args
	if len(ov.args) > 0 {
		args = make(map[string]string, len(bp.Args)+len(ov.args))
		for k, v := range bp.Args {
			args[k] = v
		}
		for k, v := range ov.args {
			args[k] = v
		}
	}
	host := r.registryHostFor(ctx)
	tag := host + "/" + plan.Resource + ":latest"
	// User-supplied extra build.tags without a registry part belong to the
	// builtin registry (Docker-fidelity: a bare tag names the default registry).
	var extraTags []string
	for _, t := range bp.Tags {
		extraTags = append(extraTags, imageref.QualifyBare(t, host))
	}
	return client.BuildRequest{
		ContextDir:    contextDir,
		Dockerfile:    bp.Dockerfile,
		Tag:           tag,
		Args:          args,
		Target:        bp.Target,
		CacheFrom:     bp.CacheFrom,
		Push:          true,
		BuildContexts: buildContexts,
		Secrets:       secrets,
		SSH:           ssh,
		// --no-cache (ov) OR the compose build.no_cache field disables the cache.
		NoCache:          ov.noCache || bp.NoCache,
		Labels:           bp.Labels,
		Pull:             bp.Pull,
		Platforms:        bp.Platforms,
		Tags:             extraTags,
		Network:          bp.Network,
		CacheTo:          bp.CacheTo,
		ExtraHosts:       bp.ExtraHosts,
		ShmSize:          bp.ShmSize,
		DockerfileInline: bp.DockerfileInline,
	}, nil
}

// buildKey is a canonical, comparable digest of everything about a
// BuildRequest that determines its output image, EXCLUDING the registry
// tag(s) it is pushed as. Two services whose resolved build requests share a
// buildKey build from the same context/Dockerfile/args/etc, so cornus builds
// them once and tags the one result for each — like `docker compose build`
// deduplicating identical build: blocks instead of re-running BuildKit's solve
// (and re-pushing) once per service.
type buildKey string

// buildRequestKey computes req's buildKey. It is a pure function of req's
// content (map field order does not matter: encoding/json sorts map keys), so
// equal requests always produce an identical key regardless of construction
// order.
func buildRequestKey(req client.BuildRequest) (buildKey, error) {
	// A field-for-field copy of BuildRequest minus Tag/Tags/Push (Push is always
	// true for every compose build call site, so it never distinguishes builds
	// here).
	keyed := struct {
		ContextDir       string
		Dockerfile       string
		Args             map[string]string
		Target           string
		CacheFrom        []string
		BuildContexts    map[string]string
		Secrets          map[string]string
		SSH              map[string]string
		NoCache          bool
		Labels           map[string]string
		Pull             bool
		Platforms        []string
		Network          string
		CacheTo          []string
		ExtraHosts       []string
		ShmSize          int64
		DockerfileInline string
	}{
		ContextDir:       req.ContextDir,
		Dockerfile:       req.Dockerfile,
		Args:             req.Args,
		Target:           req.Target,
		CacheFrom:        req.CacheFrom,
		BuildContexts:    req.BuildContexts,
		Secrets:          req.Secrets,
		SSH:              req.SSH,
		NoCache:          req.NoCache,
		Labels:           req.Labels,
		Pull:             req.Pull,
		Platforms:        req.Platforms,
		Network:          req.Network,
		CacheTo:          req.CacheTo,
		ExtraHosts:       req.ExtraHosts,
		ShmSize:          req.ShmSize,
		DockerfileInline: req.DockerfileInline,
	}
	b, err := json.Marshal(keyed)
	if err != nil {
		return "", err
	}
	return buildKey(b), nil
}

// buildGroup is one distinct build to run: the request to issue (its Tag/Tags
// cover every member's registry tag) and every compose service name that
// shares it.
type buildGroup struct {
	members    []string          // compose service names, first-seen order
	memberTags map[string]string // service name -> its own registry tag
	req        client.BuildRequest
}

// label joins the group's member service names for progress/log display, e.g.
// "web" for a single-service build or "web, worker" for a deduplicated one.
func (g *buildGroup) label() string { return strings.Join(g.members, ", ") }

// groupBuildRequests groups per-service build requests that share a buildKey
// (see buildRequestKey) into buildGroups, so a build shared by several
// services runs once. names fixes the grouping's iteration/output order
// (dependency order); reqs must have an entry for every name. Pure and
// order-preserving, so it is unit-testable without a live client.
func groupBuildRequests(names []string, reqs map[string]client.BuildRequest) ([]*buildGroup, error) {
	groups := make(map[buildKey]*buildGroup, len(names))
	var order []*buildGroup
	for _, name := range names {
		req := reqs[name]
		key, err := buildRequestKey(req)
		if err != nil {
			return nil, fmt.Errorf("build %s: %w", name, err)
		}
		if g, ok := groups[key]; ok {
			g.members = append(g.members, name)
			g.memberTags[name] = req.Tag
			g.req.Tags = append(g.req.Tags, req.Tag)
			g.req.Tags = append(g.req.Tags, req.Tags...)
			continue
		}
		g := &buildGroup{
			members:    []string{name},
			memberTags: map[string]string{name: req.Tag},
			req:        req,
		}
		groups[key] = g
		order = append(order, g)
	}
	return order, nil
}

// buildConcurrency bounds how many build groups run at once: the server itself
// queues concurrent /.cornus/v1/build executions behind a semaphore sized to
// its own CPU count (see server.buildConcurrency), so this is a courtesy cap
// on how many build-attach connections the client opens at once, not a
// correctness requirement (beyond json mode below). It never exceeds the
// number of groups to build.
//
// json mode is the one exception: buildprog.Event carries no field
// identifying which build produced it, so interleaving two builds' NDJSON
// streams would make the output ambiguous to a scripted consumer (which vertex
// belongs to which service?) even though it would not be corrupted. json mode
// therefore stays sequential — one build's complete event stream, then the
// next's — exactly like before this change.
func buildConcurrency(groups int, mode cliout.Mode) int {
	if mode == cliout.ModeJSON {
		return 1
	}
	n := goruntime.NumCPU()
	if n < 1 {
		n = 1
	}
	if groups < n {
		n = groups
	}
	if n < 1 {
		n = 1
	}
	return n
}

// buildPlans collects the compose.ServicePlan for every name in names that has
// a build section, in the given order — the set BuildCmd/UpCmd hand to
// buildServices.
func buildPlans(rt *runtime, names []string) []compose.ServicePlan {
	var plans []compose.ServicePlan
	for _, name := range names {
		if plan := rt.plans[name]; plan.Build != nil {
			plans = append(plans, plan)
		}
	}
	return plans
}

// buildServices resolves and builds every plan's image, deduplicating services
// that share an identical build (see groupBuildRequests) and building distinct
// groups concurrently (see buildConcurrency) — matching `docker compose
// build`'s parallel, deduplicated build graph instead of building one service
// at a time. Returns each plan's resulting registry tag, keyed by compose
// service name.
func (r *runtime) buildServices(ctx context.Context, plans []compose.ServicePlan, cliSSH []string, ov buildOverrides) (map[string]string, error) {
	if len(plans) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(plans))
	reqs := make(map[string]client.BuildRequest, len(plans))
	for _, plan := range plans {
		req, err := r.resolveBuildRequest(ctx, plan, cliSSH, ov)
		if err != nil {
			return nil, fmt.Errorf("build %s: %w", plan.Service, err)
		}
		names = append(names, plan.Service)
		reqs[plan.Service] = req
	}
	groups, err := groupBuildRequests(names, reqs)
	if err != nil {
		return nil, err
	}
	d := r.driver()
	tags, err := runBuildGroups(ctx, r.client, groups, d)
	if err != nil {
		return nil, err
	}
	return tags, nil
}

// runBuildGroups issues one Build call per group, bounded to buildConcurrency
// groups at a time, and reports each group's progress (see buildReporter)
// instead of streaming every group's full log to the terminal at once — the
// way `docker compose build` shows one summary line per service even when
// several build concurrently. Split out from buildServices so it is
// unit-testable against a scripted buildExecutor fake instead of a live
// client.
func runBuildGroups(ctx context.Context, exec buildExecutor, groups []*buildGroup, d *cliout.Driver) (map[string]string, error) {
	tags := make(map[string]string)
	if len(groups) == 0 {
		return tags, nil
	}
	// Announce every group before any build starts: sequential, so it never
	// races the concurrent Build calls below against the driver's unsynchronized
	// notice-writing path (see buildReporter's doc comment for why the build
	// output itself goes through a mutex-guarded target instead).
	for _, g := range groups {
		d.Step("building %s (%s)", g.label(), g.req.Tag)
	}
	br := newBuildReporter(d)
	defer br.prog.Stop()
	grp, gctx := errgroup.WithContext(ctx)
	grp.SetLimit(buildConcurrency(len(groups), d.Mode()))
	results := make([]map[string]string, len(groups))
	for i, g := range groups {
		i, g := i, g
		grp.Go(func() error {
			label := g.label()
			sink, finish := br.sinkFor(label)
			err := exec.Build(gctx, g.req, sink)
			finish(err)
			if err != nil {
				return fmt.Errorf("build %s: %w", label, err)
			}
			results[i] = g.memberTags
			return nil
		})
	}
	if err := grp.Wait(); err != nil {
		return nil, err
	}
	for _, m := range results {
		for name, tag := range m {
			tags[name] = tag
		}
	}
	return tags, nil
}
