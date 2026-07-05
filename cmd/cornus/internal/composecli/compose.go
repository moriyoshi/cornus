// Package composecli implements the `cornus compose` command group: a Docker
// Compose-compatible client that redirects Compose commands to a running
// cornus server over its /.cornus/v1/* endpoints. Alias `cornus compose` as
// `docker-compose` for drop-in use, or drive stock docker/compose through
// `cornus daemon docker` instead.
package composecli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cornus/cmd/cornus/internal/clientagent"
	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
	"cornus/cmd/cornus/internal/reghost"
	"cornus/pkg/client"
	"cornus/pkg/clientconduit"
	"cornus/pkg/compose"
	"cornus/pkg/devcontainer"
	"cornus/pkg/filewatch"
	"cornus/pkg/logging"
	"cornus/pkg/portfwd"
)

// Cmd is the kong-parsed `cornus compose` command group, mirroring
// `docker compose`.
type Cmd struct {
	Files        []string `kong:"name='file',short='f',sep='none',help='Compose file(s). Repeatable. Defaults to compose.yaml / docker-compose.yml in the working directory.'"`
	EnvFile      []string `kong:"name='env-file',sep='none',help='Env file(s) for variable interpolation, replacing the default .env discovery. Repeatable; later files win; the process environment still overrides them.'"`
	Profile      []string `kong:"name='profile',sep='none',help='Activate services with the given profile (compose profiles:). Repeatable; also honors COMPOSE_PROFILES.'"`
	Devcontainer string   `kong:"name='devcontainer',help='Path to a devcontainer.json file or a directory to search for .devcontainer/devcontainer.json. Overrides Compose-file discovery.'"`
	ProjectName  string   `kong:"name='project-name',short='p',help='Project name (default: the Compose file directory name).',env='COMPOSE_PROJECT_NAME'"`
	Host         string   `kong:"name='host',short='H',env='CORNUS_HOST',help='cornus server endpoint. Falls back to the selected connection profile, then http://localhost:5000.'"`
	Registry     string   `kong:"name='registry',env='CORNUS_REGISTRY',help='Registry host[:port] to tag built images with and to bake into deploy pull refs. Overrides the profile and the server-advertised value; empty derives from the server (GET /.cornus/v1/info), then the endpoint host.'"`
	ViaServer    *bool    `kong:"name='via-server',negatable,help='Route logs and auto-forwarded ports through the cornus server proxy instead of connecting to pods directly with your kubeconfig (cluster profiles only). --no-via-server forces the direct path. Overrides CORNUS_VIA_SERVER and the profile.'"`

	Up      UpCmd      `kong:"cmd,help='Create and start services (deploy via cornus).'"`
	Down    DownCmd    `kong:"cmd,help='Stop and remove services.'"`
	Ps      PsCmd      `kong:"cmd,help='List services and their status.'"`
	Exec    ExecCmd    `kong:"cmd,help='Run a command inside a running service container (docker compose exec).'"`
	Logs    LogsCmd    `kong:"cmd,help='View output from services.'"`
	Build   BuildCmd   `kong:"cmd,help='Build service images via the cornus build engine.'"`
	Restart RestartCmd `kong:"cmd,help='Restart services.'"`
	Stop    StopCmd    `kong:"cmd,help='Stop services without removing them.'"`
	Start   StartCmd   `kong:"cmd,help='Start previously stopped services.'"`
	Config  ConfigCmd  `kong:"cmd,help='Parse, resolve, and render the Compose model (cornus parsed view).'"`
	Version VersionCmd `kong:"cmd,help='Show the Compose CLI version.'"`
}

// runtime is the resolved context shared by all commands.
type runtime struct {
	// out is the output driver every command renders through. driver() returns a
	// plain default when it is nil (a runtime built directly in a test).
	out *cliout.Driver
	// project is the active-profile view of the loaded project: project.Services()
	// (== rt.order/rt.plans' scope) is the profile-filtered subset; project.Project()
	// is the complete, unfiltered model, used when a command needs every service
	// regardless of the active profile set (see PsCmd.Run).
	project     *compose.ProjectProfileView
	projectName string
	plans       map[string]compose.ServicePlan
	order       []string // dependency order, restricted to the active profile set
	baseDir     string   // directory relative build/mount paths resolve against
	// watchFiles is the absolute, deduplicated set of compose files and env files
	// that fed this load (compose YAMLs, sibling .env / --env-file, per-service
	// env_file, include/extends targets). `up --watch` watches these for edits.
	watchFiles []string
	client     *client.Client
	// registryOverride is the explicit image-tag / pull-ref host from --registry /
	// CORNUS_REGISTRY / the profile's registry-host. Empty means derive it (server
	// /.cornus/v1/info, then the endpoint host); see registryHostFor.
	registryOverride string
	// registryHostMemo caches the resolved registry host so repeated buildService
	// calls in one run make at most one /.cornus/v1/info request. Guarded by single-
	// goroutine command execution.
	registryHostMemo string
	// cleanup tears down anything the connection resolution started (an automatic
	// port-forward when the profile targets an in-cluster Service). Never nil.
	cleanup func()
	// kubeLogs, when non-nil, fetches service logs straight from the workload pods
	// using the developer's kubeconfig credentials (a cluster profile). The log
	// commands prefer it, falling back to the server proxy (client.Logs) only as a
	// last resort. Nil for non-cluster profiles, which use the proxy directly.
	kubeLogs kubeLogOpener
	// forwardDialer is the port-forward tunnel dialer: a direct-to-pod dialer that
	// falls back to the server proxy for a cluster profile, or the plain proxy
	// client otherwise (see clientconn.Conn.Dialer). Never nil.
	forwardDialer portfwd.Dialer
	// conduitConfig resolves the session conduit config for a per-command mode
	// override ("" defers to CORNUS_CONDUIT/profile/default; clientconduit.ModeNone
	// for --no-forward-ports). Captures the connection profile. Never nil.
	conduitConfig func(cliMode string) clientconduit.Config
	// applyIngress folds the ingress-via-conduit config (mode + native controller, the
	// latter possibly learned from the server's /info) onto a resolved Config. Captures
	// the connection profile + server. Never nil.
	applyIngress func(ctx context.Context, cfg *clientconduit.Config, overrides ...clientconn.Config)
	// connSpec is the resolved connection identity the background agent re-resolves
	// its own Conn from (config file, context, server override, resolved via-server,
	// CORNUS_TOKEN). Used by `compose up -d`; the agent derives the direct-pod
	// dialer itself from the profile, so no kube flags are threaded any more.
	connSpec clientagent.ConnSpec

	// hooks and initialize are populated only when the project came from a
	// devcontainer definition; nil for a plain Compose file (all lifecycle code
	// then no-ops).
	hooks      map[string]*devcontainer.Hooks
	initialize *devcontainer.LifecycleCommand

	// Provider (compose-spec `provider:`) bookkeeping. providerRunner drives the
	// external plugin invocations (zero value uses real os/exec; tests substitute a
	// fake). providers holds the per-`up` mutable state (env reported by each
	// provider plugin, plus a readiness channel per provider service); it lives
	// behind a pointer so runtime stays copyable (reloadAndReconcile shallow-copies
	// it) without a by-value lock. Nil until initProviderState runs, which every
	// provider helper tolerates as "no providers".
	providerRunner providerRunner
	providers      *providerState
}

// load resolves the project source (a devcontainer definition or Compose
// file(s)), parses it, and builds the API client from the connection profile
// (r) with an explicit --host override falling back to http://localhost:5000.
// The parsed project always carries every service (resolveProject/Load never
// filter by profile); load narrows it to a profile-filtered ProjectProfileView
// here, so rt.order/rt.plans (used by up/down/build/...) reflect the active
// --profile / COMPOSE_PROFILES set while rt.project.Project() keeps the
// complete, unfiltered model reachable — see PsCmd.Run, which reports every
// service regardless of which profile session deployed it.
func (c *Cmd) load(r *clientconn.Resolver, d *cliout.Driver) (*runtime, error) {
	rt := &runtime{out: d}
	if err := c.loadProjectInto(rt); err != nil {
		return nil, err
	}

	cn, err := r.Resolve(c.Host)
	if err != nil {
		return nil, err
	}
	if cn.Endpoint == "" {
		cn.Endpoint = "http://localhost:5000"
	}

	registryOverride := c.Registry
	if registryOverride == "" {
		registryOverride = cn.RegistryHost
	}

	// Resolve the via-server toggle once (flag > CORNUS_VIA_SERVER > profile). When
	// it wins — or the profile is not a cluster profile — every workload-stream path
	// (logs, port-forward) routes through the server proxy: kubeLogs stays nil and
	// the dialer is the plain proxy. The agent resolves its own direct-pod dialer
	// from the profile, so it needs no kube flags here.
	viaServer := cn.ViaServer(c.ViaServer)
	direct := cn.KubeCluster != nil && !viaServer

	var kubeLogs kubeLogOpener
	if direct {
		kubeLogs = &kubeLogSource{
			kubeContext: cn.KubeCluster.KubeContext,
			namespace:   cn.KubeCluster.Namespace,
		}
	}

	// The background agent re-resolves the connection itself; hand it an absolute
	// config path so a relative --config-file resolves the same file regardless of
	// the agent's (spawn-frozen) cwd.
	absCfg, err := r.AbsConfigPath()
	if err != nil {
		return nil, err
	}

	rt.client = cn.Client()
	rt.registryOverride = registryOverride
	rt.cleanup = cn.Cleanup
	rt.kubeLogs = kubeLogs
	rt.forwardDialer = cn.Dialer(viaServer)
	rt.conduitConfig = cn.ConduitConfig
	rt.applyIngress = cn.ApplyIngressConfig
	rt.connSpec = clientagent.ConnSpec{
		ConfigFile: absCfg,
		Context:    r.Context,
		Server:     c.Host, // raw --host; the agent re-resolves (profile, svcforward)
		ViaServer:  viaServer,
		Token:      os.Getenv("CORNUS_TOKEN"),
	}
	return rt, nil
}

// loadProjectInto resolves the project source (devcontainer or compose files),
// parses it, and populates the project-derived fields of rt: project,
// projectName, plans, order, baseDir, watchFiles, hooks, initialize. It is the
// half of load that a `--watch` reload re-runs to pick up edited compose/env
// files while keeping the existing connection (client/conduit/dialer). Never
// touches rt's connection fields.
func (c *Cmd) loadProjectInto(rt *runtime) error {
	full, baseDir, watchFiles, hooks, initialize, err := c.resolveProject()
	if err != nil {
		return err
	}
	name := c.ProjectName
	if name == "" {
		name = full.ResolveName(baseDir)
	}
	project := full.View(c.activeProfiles())
	order, plans, err := compose.OrderAndPlan(project, name)
	if err != nil {
		return err
	}
	// Bind-mount sources are relative to the project directory; make them
	// absolute (as Docker Compose does) so the deploy backend binds a real host
	// path rather than a bad named-volume name.
	for _, plan := range plans {
		plan.ResolveMounts(baseDir)
	}
	rt.project = project
	rt.projectName = name
	rt.plans = plans
	rt.order = order
	rt.baseDir = baseDir
	rt.watchFiles = filewatch.Normalize(watchFiles)
	rt.hooks = hooks
	rt.initialize = initialize
	return nil
}

// driver returns the runtime's output driver, lazily building a plain default so
// a runtime constructed directly in a test (with no driver) still renders.
func (r *runtime) driver() *cliout.Driver {
	if r.out == nil {
		r.out = cliout.New(cliout.Options{Output: "plain"})
	}
	return r.out
}

// resolveIngress folds the ingress-via-conduit config onto cfg via the applyIngress
// seam, a no-op for a runtime constructed directly in a test (no seam wired).
func (r *runtime) resolveIngress(ctx context.Context, cfg *clientconduit.Config, cliMode, caFile, caKeyFile string) error {
	override, err := clientconn.ConfigFromOptions(nil, "", cliMode, "", caFile, caKeyFile)
	if err != nil {
		return err
	}
	if r.applyIngress != nil {
		r.applyIngress(ctx, cfg, override)
	}
	return nil
}

// registryHostFor resolves the "host[:port]" that built images are tagged with and
// that deploy pull refs carry, applying the precedence: an explicit override
// (--registry / CORNUS_REGISTRY / profile registry-host) > the server-advertised
// host (GET /.cornus/v1/info) > the client endpoint host (back-compat: the single-node
// quick start, where the endpoint host doubles as the registry). The result is
// memoized so a multi-service up makes at most one /.cornus/v1/info request.
func (r *runtime) registryHostFor(ctx context.Context) string {
	if r.registryHostMemo != "" {
		return r.registryHostMemo
	}
	host := reghost.Resolve(ctx, r.client, r.registryOverride)
	r.registryHostMemo = host
	return host
}

// loadOptions builds the compose.LoadOptions from the group's global flags
// (--env-file). Load never filters by profile — see Cmd.load, which narrows
// the parsed project to a profile-filtered View.
func (c *Cmd) loadOptions() compose.LoadOptions {
	return compose.LoadOptions{EnvFiles: c.EnvFile}
}

// activeProfiles merges the --profile flags with COMPOSE_PROFILES (comma-
// separated), the two Compose profile-activation sources.
func (c *Cmd) activeProfiles() []string {
	out := append([]string(nil), c.Profile...)
	if env := os.Getenv("COMPOSE_PROFILES"); env != "" {
		for _, p := range strings.Split(env, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// resolveProject decides whether the project comes from a devcontainer
// definition or Compose file(s) and loads it. A devcontainer is used when
// --devcontainer is given, when an -f argument points at a devcontainer.json,
// or (auto-detect) when no -f/Compose file is present but a devcontainer
// definition is discoverable. A Compose file always wins in a mixed repo.
func (c *Cmd) resolveProject() (proj *compose.Project, baseDir string, watchFiles []string, hooks map[string]*devcontainer.Hooks, initialize *devcontainer.LifecycleCommand, err error) {
	// Collect every compose/env file the loader touches so `up --watch` can watch
	// them. The compose branches thread OnFileRead through the loader; the
	// devcontainer branch (which does not use compose.LoadWithOptions) records at
	// least the definition path as a best-effort watch target.
	var files []string
	opts := c.loadOptions()
	opts.OnFileRead = func(p string) { files = append(files, p) }

	switch {
	case c.Devcontainer != "":
		proj, baseDir, hooks, initialize, err = loadDevcontainer(c.Devcontainer)
		return proj, baseDir, append(files, c.Devcontainer), hooks, initialize, err
	case len(c.Files) == 1 && isDevcontainerFile(c.Files[0]):
		proj, baseDir, hooks, initialize, err = loadDevcontainer(c.Files[0])
		return proj, baseDir, append(files, c.Files[0]), hooks, initialize, err
	case len(c.Files) > 0:
		proj, err = compose.LoadWithOptions(opts, c.Files...)
		return proj, filepath.Dir(c.Files[0]), files, nil, nil, err
	}
	if found := discoverComposeFile(); found != "" {
		proj, err = compose.LoadWithOptions(opts, found)
		return proj, filepath.Dir(found), files, nil, nil, err
	}
	if _, e := devcontainerFor("."); e == nil {
		proj, baseDir, hooks, initialize, err = loadDevcontainer(".")
		return proj, baseDir, files, hooks, initialize, err
	}
	return nil, "", nil, nil, nil, fmt.Errorf("no compose file or devcontainer found (looked for compose.yaml, compose.yml, docker-compose.yaml, docker-compose.yml, .devcontainer/devcontainer.json, .devcontainer.json)")
}

// loadDevcontainer parses a devcontainer definition and prints any warnings.
func loadDevcontainer(path string) (*compose.Project, string, map[string]*devcontainer.Hooks, *devcontainer.LifecycleCommand, error) {
	res, err := devcontainer.Load(path)
	if err != nil {
		return nil, "", nil, nil, err
	}
	// Loaded during CLI setup with no request context; log against the default.
	ctx := context.Background()
	log := logging.FromContext(ctx)
	for _, w := range res.Warnings {
		log.WarnContext(ctx, "devcontainer", "warning", w)
	}
	return res.Project, res.BaseDir, res.Hooks, res.Initialize, nil
}

// isDevcontainerFile reports whether an -f argument names a devcontainer.json.
func isDevcontainerFile(f string) bool {
	base := filepath.Base(f)
	return base == "devcontainer.json" || base == ".devcontainer.json"
}

// devcontainerFor reports whether dir contains a discoverable devcontainer
// definition, returning its path.
func devcontainerFor(dir string) (string, error) {
	for _, rel := range []string{
		filepath.Join(".devcontainer", "devcontainer.json"),
		".devcontainer.json",
	} {
		p := filepath.Join(dir, rel)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no devcontainer found")
}

// selectServices returns the service names to act on, honoring an explicit
// positional list (validated against the project) or all services otherwise.
func (r *runtime) selectServices(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return r.order, nil
	}
	for _, s := range requested {
		if _, ok := r.plans[s]; !ok {
			return nil, fmt.Errorf("no such service: %s", s)
		}
	}
	// Preserve dependency order for the requested subset.
	var out []string
	want := map[string]bool{}
	for _, s := range requested {
		want[s] = true
	}
	for _, s := range r.order {
		if want[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

// knownResources returns the set of deployment resource names for every service
// defined in the Compose file — the FULL, unfiltered project (rt.project.Project()),
// not the active-profile subset. Orphan detection subtracts this set from the
// project's live workloads, so a service excluded by an inactive profile must NOT
// count as an orphan: it is still declared in the file, merely not started. Any
// per-service translation error is ignored here (PlanForStatus is tolerant and
// still returns the resolvable services); a service that failed to translate keeps
// no resource name and simply won't shield a same-named workload, which is
// acceptable for the advisory/removal path.
func (r *runtime) knownResources() map[string]struct{} {
	_, plans, _ := r.project.Project().PlanForStatus(r.projectName)
	known := make(map[string]struct{}, len(plans))
	for _, p := range plans {
		known[p.Resource] = struct{}{}
	}
	return known
}

func discoverComposeFile() string {
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if _, err := os.Stat(name); err == nil {
			return name
		}
	}
	return ""
}
