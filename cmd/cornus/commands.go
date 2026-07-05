package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"sigs.k8s.io/yaml"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
	"cornus/cmd/cornus/internal/egressflags"
	"cornus/cmd/cornus/internal/lineage"
	"cornus/cmd/cornus/internal/telemetryflags"
	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/clientconduit"
	"cornus/pkg/clientproxy"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/barehost"
	"cornus/pkg/deploy/containerdhost"
	"cornus/pkg/deploy/dockerhost"
	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/deploywire"
	"cornus/pkg/knative"
)

// PushCmd copies an image into a registry (e.g. cornus's own). Source may be
// another registry reference or a local image tarball (OCI / docker-archive).
type PushCmd struct {
	Source   string `kong:"arg,required,help='Source: a registry reference or a local image tarball path.'"`
	Dest     string `kong:"arg,required,help='Destination registry reference, e.g. localhost:5000/app:v1.'"`
	Insecure bool   `kong:"name='insecure',default='true',help='Allow HTTP (non-TLS) registries.'"`
}

// Run copies Source to Dest.
func (c *PushCmd) Run(cli *CLI) error {
	opts := []crane.Option{}
	if c.Insecure {
		opts = append(opts, crane.Insecure)
	}
	// When auth is enabled on the cornus registry, authenticate with the shared
	// bearer token. Attach it via a keychain scoped to the destination registry
	// host only, so a cross-registry copy (crane.Copy pulls from Source and pushes
	// to Dest with the same options) never sends the cornus token to an unrelated
	// source registry. Cross-registry copies needing distinct source credentials
	// are the deferred registry-credential story.
	if tok := os.Getenv("CORNUS_TOKEN"); tok != "" {
		destRef, err := name.ParseReference(c.Dest)
		if err != nil {
			return fmt.Errorf("destination %q is not a valid reference: %w", c.Dest, err)
		}
		opts = append(opts, crane.WithAuthFromKeychain(&bearerForRegistry{
			registry: destRef.Context().RegistryStr(),
			token:    tok,
		}))
	}
	if fi, err := os.Stat(c.Source); err == nil && !fi.IsDir() {
		img, err := crane.Load(c.Source)
		if err != nil {
			return fmt.Errorf("loading tarball %s: %w", c.Source, err)
		}
		if err := crane.Push(img, c.Dest, opts...); err != nil {
			return fmt.Errorf("pushing to %s: %w", c.Dest, err)
		}
	} else {
		if _, err := name.ParseReference(c.Source); err != nil {
			return fmt.Errorf("source %q is neither a file nor a valid reference: %w", c.Source, err)
		}
		if err := crane.Copy(c.Source, c.Dest, opts...); err != nil {
			return fmt.Errorf("copying %s -> %s: %w", c.Source, c.Dest, err)
		}
	}
	cli.out().Done("pushed %s", c.Dest)
	return nil
}

// DeployCmd applies a deployment spec to the local Docker host backend, or —
// with --server — to a remote cornus server, streaming any client-local bind
// mounts over 9P for the container's lifetime.
type DeployCmd struct {
	File           string               `kong:"name='file',short='f',required,help='Deployment spec file (YAML or JSON).'"`
	Delete         bool                 `kong:"name='delete',help='Delete the named deployment instead of applying it (works locally and against a --server, e.g. to tear down a --detach deploy).'"`
	Project        string               `kong:"name='project',short='p',help='Project this deployment belongs to, recorded as workload lineage (compose deploys set it automatically from the project name).'"`
	Detach         bool                 `kong:"name='detach',short='d',help='Stateless remote deploy: POST the spec to the --server, print the resulting status, and exit; the workload persists with no client session. Client-local bind mounts (including --local-mount) need a live session and are rejected; published ports bind on the server host and are not auto-forwarded. Tear down later with cornus deploy -f <spec> --delete --server <url>. A no-op for local deploys, which already return after apply.'"`
	Server         string               `kong:"name='server',help='Remote cornus server URL (http(s):// or ws(s)://). When set, deploy runs against the remote server: client-local bind mounts are streamed over 9P and the command stays in the foreground for the container lifetime (Ctrl-C tears it down). With --detach the spec is applied statelessly instead and the command exits.'"`
	LocalMount     []string             `kong:"name='local-mount',sep='none',help='Extra client-local bind mount SRC:DST[:ro][,cache][,async], served over 9P to a --server. cache marks the source immutable so reads come from the per-file block cache (implies ro); async is a writable, cache-coherent mount (cache=mmap + block proxy) for write-intensive workloads (single-writer). Repeatable.'"`
	NoForwardPorts bool                 `kong:"name='no-forward-ports',help='Do not auto-forward published ports to local listeners during a --server session.'"`
	Conduit        string               `kong:"name='conduit',default='',help='Session conduit mode: port-forward (per-port local listeners, the default) or socks5 (one split-tunnel proxy reaching services by name). A bare word sets only the mode and keeps the profile SOCKS5 listen/suffix; a socks5://host:port[?suffix=SUFFIX] URL also overrides the bind address and service-host suffix (socks5h:// is a synonym). Takes precedence over CORNUS_CONDUIT and the profile mode. --no-forward-ports disables conduit entirely.'"`
	IngressConduit string               `kong:"name='ingress-conduit',default='',help='Reach the deployment ingress through the SOCKS5 conduit: native (tunnel to the real cluster ingress controller) or emulate (a client-side reverse proxy with a generated cert), or off. Requires --conduit socks5. Takes precedence over CORNUS_INGRESS_CONDUIT and the profile.'"`
	ViaServer      *bool                `kong:"name='via-server',negatable,help='Route auto-forwarded ports through the cornus server proxy instead of connecting to pods directly with your kubeconfig (cluster profiles only). --no-via-server forces the direct path. Overrides CORNUS_VIA_SERVER and the profile.'"`
	Egress         egressflags.Flags    `kong:"embed"`
	Telemetry      telemetryflags.Flags `kong:"embed"`
}

// localBackend selects the local deploy backend from CORNUS_DEPLOY_BACKEND —
// dockerhost by default, containerd, or bare (the daemonless OCI-runtime
// backend) — mirroring the server's selection (kubernetes stays server-only;
// deploying into a cluster goes through `cornus deploy --server`). Supported
// values are "" / "dockerhost" (the default), "containerd", and "bare"; any
// other value — including "kubernetes", which only the server honors — falls
// through to dockerhost with a warning.
func localBackend(cli *CLI) (deploy.Backend, error) {
	switch v := os.Getenv("CORNUS_DEPLOY_BACKEND"); v {
	case "containerd":
		return containerdhost.New(
			containerdhost.Config{DataDir: cli.resolveConfig().DataDir},
			containerdhost.WithPolicy(hostpolicy.Permissive()),
		)
	case "bare":
		return barehost.New(
			barehost.Config{DataDir: cli.resolveConfig().DataDir},
			barehost.WithPolicy(hostpolicy.Permissive()),
		)
	default:
		if v != "" && v != "dockerhost" {
			cli.out().Warn("CORNUS_DEPLOY_BACKEND %q not supported for local deploy; falling back to dockerhost", v)
		}
		return dockerhost.New(dockerhost.WithPolicy(dockerhost.PermissivePolicy()))
	}
}

// Run applies (or deletes) the deployment described by the spec file.
func (c *DeployCmd) Run(cli *CLI) error {
	data, err := os.ReadFile(c.File)
	if err != nil {
		return fmt.Errorf("reading spec: %w", err)
	}
	// A Knative Serving Service manifest is a first-class deploy descriptor
	// alongside the native spec: detect one and translate it to a DeploySpec (with
	// a Knative block the kubernetes backend round-trips into a native ksvc), else
	// parse the file as a native spec.
	var spec api.DeploySpec
	if knative.Detect(data) {
		s, warnings, lerr := knative.Load(data)
		if lerr != nil {
			return fmt.Errorf("parsing knative descriptor: %w", lerr)
		}
		spec = s
		for _, w := range warnings {
			cli.out().Warn("knative: %s", w)
		}
	} else if uerr := yaml.Unmarshal(data, &spec); uerr != nil {
		return fmt.Errorf("parsing spec: %w", uerr)
	}
	if spec.Name == "" {
		return fmt.Errorf("spec.name is required")
	}
	for _, lm := range c.LocalMount {
		m, err := parseLocalMount(lm)
		if err != nil {
			return err
		}
		spec.Mounts = append(spec.Mounts, m)
	}
	if err := c.Egress.Apply(&spec); err != nil {
		return err
	}
	if err := c.Telemetry.Apply(&spec); err != nil {
		return err
	}
	// Env mode: resolve the caller's own proxy settings and inject them now (runs
	// client-side for both local and remote deploys). Proxy/transparent modes are
	// realised later by the backend/relay.
	if err := clientproxy.ApplyEgressEnv(&spec); err != nil {
		return err
	}
	// Record the deploy's lineage (client host/user/dir/git) unless a delete,
	// which carries only a name. --project wins over any project the spec file
	// already declared; the server stamps the authenticated Subject.
	if !c.Delete {
		spec.Origin = applyOrigin(spec.Origin, lineage.Collect(""), c.Project)
	}

	d := cli.out()
	cn, err := cli.resolveConn(c.Server)
	if err != nil {
		return err
	}
	if cn.Endpoint != "" {
		return c.runRemote(cli.rootContext(), d, cn, spec)
	}

	// A client-side-egress relay mode routes traffic back through a client session,
	// which a local deploy (no server) has no way to hold — and egressing to the
	// local host, which already runs the workload, is meaningless. Require --server.
	if !c.Delete && spec.Egress.NeedsRelay() {
		return fmt.Errorf("client-side egress mode %q needs a live client session; deploy against a --server (a local deploy has no session and would egress to this same host)", spec.Egress.Mode)
	}

	// Local CLI: the caller already has direct runtime access on this host, so
	// the server-side default-deny policy would only add friction with no
	// security gain. Deploy with a permissive policy.
	backend, err := localBackend(cli)
	if err != nil {
		return fmt.Errorf("connecting to deploy backend: %w", err)
	}
	defer backend.Close()

	ctx := cli.rootContext()
	if c.Delete {
		if err := backend.Delete(ctx, spec.Name); err != nil {
			return err
		}
		d.Done("deleted %s", spec.Name)
		return nil
	}

	status, err := backend.Apply(ctx, spec)
	if err != nil {
		return err
	}
	return d.Emit(newDeployResult(status))
}

// deployResult is the structured result of a deploy: a "deployed NAME: R/T
// instances running" line in plain/fancy mode, a JSON object in json mode.
type deployResult struct {
	Event    string      `json:"event"`
	Name     string      `json:"name"`
	Running  int         `json:"running"`
	Total    int         `json:"total"`
	Endpoint string      `json:"endpoint,omitempty"`
	Detached bool        `json:"detached,omitempty"`
	Origin   *api.Origin `json:"origin,omitempty"`
}

func newDeployResult(status api.DeployStatus) deployResult {
	running := 0
	for _, in := range status.Instances {
		if in.Running {
			running++
		}
	}
	return deployResult{Event: "deployed", Name: status.Name, Running: running, Total: len(status.Instances), Origin: status.Origin}
}

// summarizeOrigin renders an origin as a terse one-line "project, user@host,
// git@abcdef1*" summary (empty parts dropped), or "" when there is nothing to
// show. Used to append lineage to the human deploy output.
func summarizeOrigin(o *api.Origin) string {
	if o == nil {
		return ""
	}
	var parts []string
	if o.Project != "" {
		parts = append(parts, o.Project)
	}
	switch {
	case o.User != "" && o.Host != "":
		parts = append(parts, o.User+"@"+o.Host)
	case o.Host != "":
		parts = append(parts, o.Host)
	case o.User != "":
		parts = append(parts, o.User)
	}
	if o.Subject != "" {
		parts = append(parts, "auth:"+o.Subject)
	}
	if o.Git != nil && o.Git.Commit != "" {
		c := o.Git.Commit
		if len(c) > 7 {
			c = c[:7]
		}
		if o.Git.Dirty {
			c += "*"
		}
		parts = append(parts, "git@"+c)
	}
	return strings.Join(parts, ", ")
}

// applyOrigin folds the freshly collected client origin into whatever the spec
// file already declared, then applies an explicit project override. The
// collected host/user/dir/git win over file-declared values (they are the live
// truth); a spec-file Project is kept unless project (the --project flag) is
// non-empty. Any Subject is cleared — only the server may set it. Returns nil
// when nothing at all is known.
func applyOrigin(fileOrigin, collected *api.Origin, project string) *api.Origin {
	o := &api.Origin{}
	if fileOrigin != nil {
		*o = *fileOrigin
	}
	if collected != nil {
		if collected.Host != "" {
			o.Host = collected.Host
		}
		if collected.User != "" {
			o.User = collected.User
		}
		if collected.Directory != "" {
			o.Directory = collected.Directory
		}
		if collected.Git != nil {
			o.Git = collected.Git
		}
	}
	if project != "" {
		o.Project = project
	}
	o.Subject = "" // server-stamped; never trust a client value
	if o.Project == "" && o.Host == "" && o.User == "" && o.Directory == "" && o.Git == nil {
		return nil
	}
	return o
}

func (r deployResult) Human(p cliout.Printer) {
	origin := ""
	if s := summarizeOrigin(r.Origin); s != "" {
		origin = " (origin: " + s + ")"
	}
	switch {
	case r.Detached:
		p.Line("deployed %s to %s: %d/%d instances running (detached)%s", r.Name, r.Endpoint, r.Running, r.Total, origin)
	default:
		p.Line("deployed %s: %d/%d instances running%s", r.Name, r.Running, r.Total, origin)
	}
}

// runRemote deploys spec against a remote cornus server. The default is a
// foreground deploy-attach session: client-local bind mounts are streamed over
// 9P, published ports are auto-forwarded to local listeners unless
// --no-forward-ports is set, and Ctrl-C (or SIGTERM) requests a graceful
// teardown of the remote deployment. With --detach the spec is instead POSTed
// once to /.cornus/v1/deploy and the command exits, leaving the workload running;
// --delete removes a deployment by name and exits.
func (c *DeployCmd) runRemote(base context.Context, d *cliout.Driver, cn *clientconn.Conn, spec api.DeploySpec) error {
	defer cn.Cleanup()
	ctx, stop := signal.NotifyContext(base, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cl := cn.Client()
	if c.Delete {
		// One-shot remote delete: the teardown verb for a --detach deploy (an
		// attach-session deploy tears down on disconnect and never needs this).
		if err := cl.Delete(ctx, spec.Name); err != nil {
			return err
		}
		d.Done("deleted %s", spec.Name)
		return nil
	}
	if c.Detach {
		return c.runRemoteDetached(ctx, d, cl, cn.Endpoint, spec)
	}
	var fwdOnce sync.Once
	var conduit clientconduit.Conduit
	var conduitErr error
	// Streamed deploy logs are the point of the foreground session — route them
	// to stdout through a LineWriter so json mode wraps them and concurrent
	// writes never split a line.
	logs := d.LineWriter(d.Out(), "")
	defer logs.Close()
	d.Step("deploying %s to %s (Ctrl-C to tear down)...", spec.Name, cn.Endpoint)
	err := cl.DeployAttach(ctx, spec, func(e deploywire.Event) {
		if e.Log != "" {
			io.WriteString(logs, e.Log)
		}
		if e.Ready {
			fwdOnce.Do(func() {
				conduit, conduitErr = c.startConduit(ctx, d, cn, spec)
				if conduitErr != nil {
					// An explicitly-requested conduit that couldn't start: don't hold a
					// half-broken session — unwind now and surface the error.
					d.Error("%v", conduitErr)
					stop()
				}
			})
		}
		if e.Ready && e.Status != nil {
			d.Emit(newDeployResult(*e.Status))
		}
		if e.Err != "" {
			d.Error("deploy error: %s", e.Err)
		}
	})
	if conduit != nil {
		conduit.Close()
	}
	// An explicitly-requested conduit that failed to start (a SOCKS5 bind conflict,
	// or a bad CORNUS_CONDUIT value) is a real error — reported even when the session
	// ended via the stop() we called on that failure (ctx is then cancelled).
	if conduitErr != nil {
		return conduitErr
	}
	if err != nil && ctx.Err() == nil {
		return err
	}
	d.Done("torn down")
	return nil
}

// startConduit brings up the session's conduit (port-forward listeners or a SOCKS5
// proxy) for the ready workload and prints how to reach it. --no-forward-ports
// disables it. It returns a nil Conduit (nothing to close) when conduit is off or
// nothing was exposed, and a non-nil error when an explicitly-requested conduit
// could not start (a SOCKS5 bind conflict or a bad mode) — the port-forward
// constructor never fails, so that error is SOCKS5/config-specific.
func (c *DeployCmd) startConduit(ctx context.Context, d *cliout.Driver, cn *clientconn.Conn, spec api.DeploySpec) (clientconduit.Conduit, error) {
	mode := c.Conduit
	if c.NoForwardPorts {
		mode = clientconduit.ModeNone
	}
	cfg := cn.ConduitConfig(mode)
	if cfg.Mode == clientconduit.ModeNone {
		return nil, nil
	}
	cn.ApplyIngress(ctx, &cfg, c.IngressConduit)
	eg, err := clientconduit.Start(ctx, cn.Dialer(cn.ViaServer(c.ViaServer)), cfg)
	if err != nil {
		return nil, fmt.Errorf("conduit setup failed: %w", err)
	}
	for _, line := range eg.Banner() {
		d.Info("%s", line)
	}
	fwds, err := eg.Add(ctx, spec.Name, spec.Ports)
	if err != nil {
		d.Error("port-forward setup failed: %v", err)
	}
	for _, f := range fwds {
		if f.Protocol == "udp" {
			d.Info("forwarding %s -> %s:%d/udp", f.Local, f.Name, f.Container)
		} else {
			d.Info("forwarding %s -> %s:%d", f.Local, f.Name, f.Container)
		}
	}
	// Reach the deployment ingress through the conduit (native or emulate), when opted
	// in and the spec requests one. Non-fatal: a failure just logs.
	ingressHosts, ierr := eg.AddIngress(ctx, spec.Name, spec.Ingress, spec.Ports)
	if ierr != nil {
		d.Error("ingress via conduit failed: %v", ierr)
	}
	for _, h := range ingressHosts {
		d.Info("ingress reachable at %s (through the SOCKS5 proxy)", h)
	}
	// A port-forward session that exposed nothing (all mappings skipped) and has no
	// banner or ingress holds nothing open — close it so it does not keep the process
	// alive.
	if len(eg.Banner()) == 0 && len(fwds) == 0 && len(ingressHosts) == 0 {
		eg.Close()
		return nil, nil
	}
	return eg, nil
}

// runRemoteDetached applies spec statelessly (POST /.cornus/v1/deploy) and returns:
// nothing is held open, and the workload persists on the server until deleted
// with `cornus deploy -f <spec> --delete --server <url>`. Specs that need a
// live client session — client-local bind mounts, which the attach path serves
// over 9P — are rejected up front; published ports bind on the server host and
// are not auto-forwarded.
func (c *DeployCmd) runRemoteDetached(ctx context.Context, d *cliout.Driver, cl *client.Client, endpoint string, spec api.DeploySpec) error {
	if err := checkDetachable(spec); err != nil {
		return err
	}
	if len(spec.Ports) > 0 && !c.NoForwardPorts {
		d.Info("detached deploy: published ports bind on the server host and are not auto-forwarded; use 'cornus port-forward' for a local listener")
	}
	status, err := cl.Deploy(ctx, spec)
	if err != nil {
		return err
	}
	res := newDeployResult(status)
	res.Endpoint = endpoint
	res.Detached = true
	if err := d.Emit(res); err != nil {
		return err
	}
	d.Info("tear down with: cornus deploy -f %s --delete --server %s", c.File, endpoint)
	return nil
}

// checkDetachable reports why spec cannot be deployed detached: client-local
// bind mounts (absolute or ./ ../ ~ sources, including --local-mount entries)
// are served to the server over 9P by the attach session, which a stateless
// POST cannot provide. Named-volume/bare-name mount sources detach fine.
func checkDetachable(spec api.DeploySpec) error {
	if srcs := client.LocalMountSources(spec); len(srcs) > 0 {
		return fmt.Errorf("--detach cannot serve client mounts (client-local sources: %s); drop --detach or remove the client-local mounts (including --local-mount)", strings.Join(srcs, ", "))
	}
	if spec.Credentials != nil && len(spec.Credentials.Sources) > 0 {
		return fmt.Errorf("--detach cannot broker client-sourced credentials (they are minted on the client for the session's lifetime); drop --detach or remove spec.credentials")
	}
	// A relay egress mode that routes traffic to the CLIENT needs a live session, so
	// it cannot detach. A deploy whose policy routes only to the gateway/cluster/deny
	// CAN detach: gateway-routed traffic egresses from the durable gateway node (the
	// server), no client session required. env mode always detaches fine.
	if e := spec.Egress; e != nil && e.NeedsRelay() && egressRoutesToClient(e) {
		return fmt.Errorf("--detach cannot route client-side egress to the client (mode %q needs a live session); route to \"gateway\" (a durable egress node) instead, use --egress=env, or drop --detach", e.Mode)
	}
	return nil
}

// egressRoutesToClient reports whether an egress policy could route any destination
// to the CLIENT terminus (which needs a live session). A rule or default naming
// "client", or a script (whose verdicts cannot be determined statically), counts.
func egressRoutesToClient(e *api.EgressSpec) bool {
	if strings.TrimSpace(e.Script) != "" {
		return true // conservative: a script may route to the client
	}
	if e.Default == "client" {
		return true
	}
	for _, r := range e.Rules {
		if r.Route == "client" {
			return true
		}
	}
	return false
}

// bearerForRegistry is an authn.Keychain that hands out the cornus bearer token
// only when the requested resource's registry host matches registry, and stays
// anonymous for every other host. This scopes the token to the destination
// registry so a cross-registry crane.Copy cannot leak it to the source host.
type bearerForRegistry struct {
	registry string
	token    string
}

// Resolve returns the bearer authenticator for the matching registry host and
// anonymous credentials otherwise.
func (k *bearerForRegistry) Resolve(res authn.Resource) (authn.Authenticator, error) {
	if k.token != "" && res.RegistryStr() == k.registry {
		return &authn.Bearer{Token: k.token}, nil
	}
	return authn.Anonymous, nil
}

// parseLocalMount parses a SRC:DST[:opts] client-local bind mount spec. opts is a
// comma-separated list of "ro" (read-only), "cache" (immutable — serve reads from
// the server-side block cache; implies read-only), and "async" (writable,
// cache-coherent async mount — cache=mmap + block proxy, for write-intensive
// workloads; single-writer only; excludes ro/cache).
func parseLocalMount(s string) (api.Mount, error) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return api.Mount{}, fmt.Errorf("invalid --local-mount %q: want SRC:DST[:ro][,cache]", s)
	}
	m := api.Mount{Source: parts[0], Target: parts[1]}
	if len(parts) == 3 {
		for _, opt := range strings.Split(parts[2], ",") {
			switch opt {
			case "ro":
				m.ReadOnly = true
			case "cache":
				// Caching is only sound for immutable, read-only content.
				m.Immutable = true
				m.ReadOnly = true
			case "async":
				// Writable, cache-coherent async mount (cache=mmap + block proxy).
				// Mutually exclusive with ro/cache; single-writer only.
				m.AsyncCache = true
			default:
				return api.Mount{}, fmt.Errorf("invalid --local-mount %q: unknown option %q (want ro,cache,async)", s, opt)
			}
		}
	}
	if m.AsyncCache && (m.ReadOnly || m.Immutable) {
		return api.Mount{}, fmt.Errorf("invalid --local-mount %q: async is writable and cannot combine with ro/cache", s)
	}
	return m, nil
}
