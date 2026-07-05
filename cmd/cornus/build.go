package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
	"cornus/cmd/cornus/internal/reghost"
	"cornus/pkg/build/builder"
	"cornus/pkg/build/buildwire"
	"cornus/pkg/imageref"
	"cornus/pkg/wire"
)

// buildResult is the structured result of a build: "built TAG (DIGEST)" in
// plain/fancy mode, a JSON object in json mode.
type buildResult struct {
	Event  string `json:"event"`
	Tag    string `json:"tag"`
	Digest string `json:"digest,omitempty"`
}

func (r buildResult) Human(p cliout.Printer) {
	if r.Digest != "" {
		p.Line("built %s (%s)", r.Tag, r.Digest)
		return
	}
	p.Line("built %s", r.Tag)
}

// bearerHeader returns an http.Header carrying "Authorization: Bearer <token>", or
// nil when token is empty. It is passed to the remote build handshake so the CLI
// authenticates against a cornus server that has auth enabled.
func bearerHeader(token string) http.Header {
	if token == "" {
		return nil
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return h
}

// BuildCmd builds an image from a context. By default it uses the in-process
// build engine and pushes to the target registry; with --builder it runs the
// build on a remote cornus server, streaming the context, --build-context
// directories, and secrets to it over 9P/WebSocket.
type BuildCmd struct {
	Tag          string   `kong:"name='tag',short='t',required,help='Target image reference, e.g. localhost:5000/app:v1.'"`
	Context      string   `kong:"arg,default='.',help='Build context directory.'"`
	Dockerfile   string   `kong:"name='file',short='f',default='Dockerfile',help='Path to the Dockerfile, relative to the context.'"`
	BuildArg     []string `kong:"name='build-arg',sep='none',help='Build args (KEY=VALUE), repeatable.'"`
	Secret       []string `kong:"name='secret',sep='none',help='Secret mounts (id=NAME,src=PATH), repeatable.'"`
	SSH          []string `kong:"name='ssh',sep='none',help='SSH agent forwarding: \"default\" or \"ID[=SOCKET]\" (RUN --mount=type=ssh), repeatable.'"`
	BuildContext []string `kong:"name='build-context',sep='none',help='Named build context NAME=PATH (RUN --mount=type=bind,from=NAME), repeatable.'"`
	Builder      string   `kong:"name='builder',help='Remote cornus build endpoint (ws:// or http(s):// base URL). When set, the build runs there and this machine streams the context, build-context dirs, and secrets over 9P/WebSocket.',env='CORNUS_BUILDER'"`
	Rootless     bool     `kong:"name='rootless',help='Run the build in rootless mode (user namespaces).',env='CORNUS_ROOTLESS'"`
	Lazy         bool     `kong:"name='lazy',help='Serve --build-context dirs on demand over 9P (lazy build) instead of syncing them eagerly. Also enabled server-wide by CORNUS_LAZY_BUILD.',env='CORNUS_LAZY_BUILD'"`
	CacheTo      []string `kong:"name='cache-to',sep='none',help='Cache export backend (buildx syntax), e.g. type=registry,ref=HOST/app:cache[,registry.insecure=true]. For type=local, dest= is an engine-managed key (auto-derived from --tag if omitted), not a filesystem path. Repeatable.'"`
	CacheFrom    []string `kong:"name='cache-from',sep='none',help='Cache import backend (buildx syntax), e.g. type=registry,ref=HOST/app:cache[,registry.insecure=true]. For type=local, src= is an engine-managed key (auto-derived from --tag if omitted), not a filesystem path. Repeatable.'"`
	NoCache      bool     `kong:"name='no-cache',help='Do not use the build cache.'"`
	NoPush       bool     `kong:"name='no-push',help='Build only; do not push the result.'"`
	Insecure     bool     `kong:"name='insecure',default='true',help='Allow pushing to an HTTP (non-TLS) registry.'"`
	Registry     string   `kong:"name='registry',env='CORNUS_REGISTRY',help='Registry host for tags without a registry part (remote builds). Defaults to the server-advertised host, else the builder endpoint host.'"`
}

// Run executes the build, locally or on a remote builder.
func (c *BuildCmd) Run(cli *CLI, r *clientconn.Resolver) error {
	buildArgs, err := parseKeyVals(c.BuildArg)
	if err != nil {
		return fmt.Errorf("--build-arg: %w", err)
	}
	secrets, err := parseSecrets(c.Secret)
	if err != nil {
		return fmt.Errorf("--secret: %w", err)
	}
	ssh, err := parseSSH(c.SSH)
	if err != nil {
		return fmt.Errorf("--ssh: %w", err)
	}
	named, err := parseBuildContexts(c.BuildContext)
	if err != nil {
		return fmt.Errorf("--build-context: %w", err)
	}

	cacheExports, err := parseCacheOpts(c.CacheTo)
	if err != nil {
		return fmt.Errorf("--cache-to: %w", err)
	}
	cacheImports, err := parseCacheOpts(c.CacheFrom)
	if err != nil {
		return fmt.Errorf("--cache-from: %w", err)
	}

	// An explicit --builder, or a selected connection profile that names a server,
	// runs the build remotely; otherwise use the in-process engine on this host.
	d := cli.out()
	cn, err := r.Resolve(c.Builder)
	if err != nil {
		return err
	}
	if cn.Endpoint != "" {
		defer cn.Cleanup()
		return c.runRemote(cli.rootContext(), d, cn, buildArgs, secrets, ssh, cacheExports, cacheImports)
	}

	cfg := cli.resolveConfig()
	if err := cfg.EnsureDirs(); err != nil {
		return fmt.Errorf("preparing data dir: %w", err)
	}

	eng, err := builder.New(builder.Config{Root: cfg.CacheDir(), Rootless: c.Rootless})
	if err != nil {
		return fmt.Errorf("starting build engine: %w", err)
	}
	defer eng.Close()

	ctx, stop := signal.NotifyContext(cli.rootContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sink, finish := d.BuildProgress()
	defer finish()
	res, err := eng.Build(ctx, builder.Request{
		ContextDir:    c.Context,
		Dockerfile:    c.Dockerfile,
		Target:        c.Tag,
		BuildArgs:     buildArgs,
		Secrets:       secrets,
		NamedContexts: named,
		SSH:           ssh,
		NoCache:       c.NoCache,
		Lazy:          c.Lazy,
		CacheExports:  cacheExports,
		CacheImports:  cacheImports,
		Push:          !c.NoPush,
		Insecure:      c.Insecure,
	}, sink)
	if err != nil {
		return err
	}

	return d.Emit(buildResult{Event: "built", Tag: c.Tag, Digest: res.ImageDigest})
}

// runRemote streams the caller's context, named-context directories, secrets,
// and SSH agents to a remote cornus builder over 9P/WebSocket.
func (c *BuildCmd) runRemote(base context.Context, d *cliout.Driver, cn *clientconn.Conn, buildArgs map[string]string, secrets []builder.SecretSource, ssh []builder.SSHSource, cacheExports, cacheImports []builder.CacheOption) error {
	ctx, stop := signal.NotifyContext(base, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A --tag without a registry part belongs to the server's builtin registry
	// (Docker-fidelity: a bare tag names the default registry). Qualify it with
	// the resolved cornus registry host before the remote build pushes it.
	target := imageref.QualifyBare(c.Tag, reghost.Resolve(ctx, cn.Client(), c.Registry))

	dfName := c.Dockerfile
	if dfName == "" {
		dfName = "Dockerfile"
	}
	ctxAbs, err := filepath.Abs(c.Context)
	if err != nil {
		return err
	}
	dfDir := filepath.Dir(filepath.Join(ctxAbs, dfName))

	secretVals := map[string][]byte{}
	for _, s := range secrets {
		data, err := os.ReadFile(s.Path)
		if err != nil {
			return fmt.Errorf("reading secret %q: %w", s.ID, err)
		}
		secretVals[s.ID] = data
	}

	named, err := parseBuildContexts(c.BuildContext)
	if err != nil {
		return fmt.Errorf("--build-context: %w", err)
	}

	sshSockets := map[string]string{}
	var sshIDs []string
	for _, s := range ssh {
		sshSockets[s.ID] = s.Socket
		sshIDs = append(sshIDs, s.ID)
	}
	sort.Strings(sshIDs)

	// With --lazy (or CORNUS_LAZY_BUILD, which kong folds into c.Lazy), named
	// contexts are served on demand over 9P (the server reads only what the build
	// touches) instead of eagerly synced. The server's snapshotter supports the
	// lazy path unconditionally, so this needs no server-side opt-in.
	eager, lazy := named, map[string]string(nil)
	if c.Lazy {
		eager, lazy = nil, named
	}

	spec := buildwire.BuildSpec{
		Target:         target,
		DockerfileName: filepath.Base(dfName),
		BuildArgs:      buildArgs,
		NamedContexts:  sortedKeys(eager),
		SecretIDs:      sortedSecretIDs(secretVals),
		SSHIDs:         sshIDs,
		CacheExports:   toWireCacheOpts(cacheExports),
		CacheImports:   toWireCacheOpts(cacheImports),
		Push:           !c.NoPush,
		Insecure:       c.Insecure,
		NoCache:        c.NoCache,
	}
	opts := buildwire.ServeOpts{
		ContextDir:     ctxAbs,
		DockerfileDir:  dfDir,
		DockerfileName: filepath.Base(dfName),
		NamedContexts:  eager,
		LazyContexts:   lazy,
		Secrets:        secretVals,
		SSHSockets:     sshSockets,
	}

	sink, finish := d.BuildProgress()
	defer finish()
	res, err := buildwire.Serve(ctx, resolveBuilderURL(cn.Endpoint), spec, opts, sink, bearerHeader(cn.Token), wire.ClientTransport{TLS: cn.TLS, DialContext: cn.DialContext})
	if err != nil {
		return err
	}
	return d.Emit(buildResult{Event: "built", Tag: target, Digest: res.ImageDigest})
}

// parseSSH parses SSH agent specs: "default" or "ID[=SOCKET]". A missing socket
// falls back to $SSH_AUTH_SOCK.
func parseSSH(items []string) ([]builder.SSHSource, error) {
	var out []builder.SSHSource
	for _, it := range items {
		id, sock, has := strings.Cut(it, "=")
		if id == "" {
			id = "default"
		}
		if !has || sock == "" {
			sock = os.Getenv("SSH_AUTH_SOCK")
		}
		if sock == "" {
			return nil, fmt.Errorf("%q: no agent socket (set SSH_AUTH_SOCK or ID=SOCKET)", it)
		}
		out = append(out, builder.SSHSource{ID: id, Socket: sock})
	}
	return out, nil
}

// parseBuildContexts parses "NAME=PATH" entries into a name->absolute-path map.
func parseBuildContexts(items []string) (map[string]string, error) {
	out := map[string]string{}
	for _, it := range items {
		name, p, ok := strings.Cut(it, "=")
		if !ok || name == "" || p == "" {
			return nil, fmt.Errorf("expected NAME=PATH, got %q", it)
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		out[name] = abs
	}
	return out, nil
}

// parseCacheOpts parses buildx-style cache specs ("type=registry,ref=...,key=val")
// into builder CacheOptions. "type" is required; all other keys become Attrs.
func parseCacheOpts(specs []string) ([]builder.CacheOption, error) {
	var out []builder.CacheOption
	for _, s := range specs {
		if strings.TrimSpace(s) == "" {
			continue
		}
		opt := builder.CacheOption{Attrs: map[string]string{}}
		for _, field := range strings.Split(s, ",") {
			k, v, ok := strings.Cut(field, "=")
			if !ok {
				return nil, fmt.Errorf("invalid cache option %q (want key=value)", field)
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			if k == "type" {
				opt.Type = v
			} else {
				opt.Attrs[k] = v
			}
		}
		if opt.Type == "" {
			return nil, fmt.Errorf("cache option %q missing type=", s)
		}
		out = append(out, opt)
	}
	return out, nil
}

// toWireCacheOpts converts builder CacheOptions to their wire form for a remote build.
func toWireCacheOpts(opts []builder.CacheOption) []buildwire.CacheOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]buildwire.CacheOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, buildwire.CacheOption{Type: o.Type, Attrs: o.Attrs})
	}
	return out
}

// resolveBuilderURL normalizes a builder reference to a ws:// attach URL.
func resolveBuilderURL(b string) string {
	switch {
	case strings.HasPrefix(b, "http://"):
		b = "ws://" + strings.TrimPrefix(b, "http://")
	case strings.HasPrefix(b, "https://"):
		b = "wss://" + strings.TrimPrefix(b, "https://")
	case strings.HasPrefix(b, "ws://"), strings.HasPrefix(b, "wss://"):
	default:
		b = "ws://" + b
	}
	if !strings.Contains(b, "/.cornus/v1/build/attach") {
		b = strings.TrimRight(b, "/") + "/.cornus/v1/build/attach"
	}
	return b
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSecretIDs(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseKeyVals parses "KEY=VALUE" entries into a map.
func parseKeyVals(items []string) (map[string]string, error) {
	out := map[string]string{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("expected KEY=VALUE, got %q", it)
		}
		out[k] = v
	}
	return out, nil
}

// parseSecrets parses buildx-style "id=NAME,src=PATH" secret specs.
func parseSecrets(items []string) ([]builder.SecretSource, error) {
	var out []builder.SecretSource
	for _, it := range items {
		var s builder.SecretSource
		for _, field := range strings.Split(it, ",") {
			k, v, ok := strings.Cut(field, "=")
			if !ok {
				return nil, fmt.Errorf("expected id=NAME,src=PATH, got %q", it)
			}
			switch k {
			case "id":
				s.ID = v
			case "src", "source":
				s.Path = v
			default:
				return nil, fmt.Errorf("unknown secret field %q", k)
			}
		}
		if s.ID == "" {
			return nil, fmt.Errorf("secret missing id: %q", it)
		}
		if s.Path == "" {
			s.Path = s.ID
		}
		out = append(out, s)
	}
	return out, nil
}
