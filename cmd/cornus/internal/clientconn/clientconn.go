// Package clientconn resolves how the cornus CLI connects to a server: it merges
// a per-command endpoint flag with the selected connection profile (see
// pkg/clientconfig), applies the profile's credentials (a static token, a
// cluster-minted ServiceAccount token, or none) and TLS material, and opens an
// automatic tunnel when the profile names one — a Kubernetes port-forward to an
// in-cluster Service, or an SSH tunnel to a remote docker/containerd host (see
// pkg/sshclient), whose dialer is injected into the client transport. It lives in
// an internal package (not package main) so both the top-level commands and the
// `cornus compose` subpackage can share one resolver, bound via kong.
package clientconn

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cornus/pkg/client"
	"cornus/pkg/clientconduit"
	"cornus/pkg/clientconfig"
	"cornus/pkg/ingressnative"
	"cornus/pkg/kubeauth"
	"cornus/pkg/kubefwd"
	"cornus/pkg/portfwd"
	"cornus/pkg/socks5"
	"cornus/pkg/sshclient"
	"cornus/pkg/svcforward"
)

// Resolver builds connections from the client config. It holds the global
// --config-file / --context flag values; one instance is bound into kong so every
// command's Run method can receive it.
type Resolver struct {
	// ConfigFile is the --config-file / CORNUS_CONFIG value; "" means the default path.
	ConfigFile string
	// Context is the --context / CORNUS_CONTEXT value; "" means the config's current.
	Context string
	// ProjectContextFile is the --context-file / CORNUS_CONTEXT_FILE value: an explicit
	// per-project context override file (a bare clientconfig.Context in JSON/YAML/TOML).
	// "" means auto-discover clientconfig.ProjectContextNames by walking up from workDir.
	ProjectContextFile string
	// NoProjectContext is the --no-context-file toggle: skip per-project override
	// discovery entirely.
	NoProjectContext bool
	// TrustProjectContext is the --trust-context-file / CORNUS_TRUST_CONTEXT_FILE
	// opt-in: honor the security-sensitive fields (server, token, tls, ssh-tunnel,
	// kube-auth, ...) of an auto-discovered project override and bypass provenance
	// vetting. Off by default, so a merely-discovered file cannot silently redirect
	// the endpoint or credentials; an explicitly named --context-file is trusted for
	// its fields regardless (the user pointed at it) but still provenance-checked.
	TrustProjectContext bool
	// workDir is the directory the auto-discovery walk starts from; "" means the
	// process working directory. Set in tests to avoid depending on the real cwd.
	workDir string
}

// Conn is a resolved connection: the endpoint URL, the resolved bearer token and
// TLS config, and a Cleanup that tears down anything the resolution started (the
// port-forward). Cleanup is never nil and is safe to defer.
type Conn struct {
	Endpoint string
	Token    string
	TLS      *tls.Config
	Cleanup  func()
	// DialContext, when non-nil, is the transport dialer every request to the server
	// rides — set for an SSH-tunnel profile so REST, WebSocket, and registry traffic
	// all go through the one SSH connection. It is distinct from the Dialer method,
	// which is the port-forward *workload* dialer. nil means dial the endpoint
	// directly.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// RegistryHost is the profile's optional override for the image-tag / pull-ref
	// host (clientconfig.Context.RegistryHost). Empty means the caller derives it
	// (server /.cornus/v1/info, then the endpoint host). Unlike Endpoint it is never
	// rewritten by the port-forward: the forward's localhost address is only a
	// control-plane rendezvous, not something a cluster node can pull from.
	RegistryHost string
	// KubeCluster, when non-nil, names the kube context+namespace of the cluster this
	// profile targets, so log/exec-style commands can reach workload pods directly
	// with the developer's kubeconfig instead of proxying through the server. Derived
	// from the profile's PortForward / KubeAuth block; nil for non-cluster profiles.
	KubeCluster *KubeCluster
	// ProfileViaServer is the profile's via-server toggle (clientconfig.Context.
	// ViaServer): the lowest-precedence layer of the "route workload streams through
	// the server instead of direct-to-pod" decision. nil means unset. Combine it
	// with the env var and a per-command flag via ViaServer.
	ProfileViaServer *bool
	// ProfileConduit is the profile's session conduit preference (clientconfig.Context.
	// Conduit): mode plus SOCKS5 settings. nil means unset. Combine it with the env
	// var and a per-command flag via ConduitMode / ConduitConfig.
	ProfileConduit *clientconfig.Conduit
}

// KubeCluster identifies the Kubernetes cluster (context + namespace) a connection
// profile targets, so the CLI can talk to workload pods directly with the
// developer's kubeconfig credentials.
type KubeCluster struct {
	KubeContext string
	Namespace   string
}

// Dialer returns the port-forward tunnel dialer for this connection. For a
// cluster profile with the direct path in effect it prefers a direct pod dialer
// (the developer's kubeconfig, bypassing the server's under-privileged
// ServiceAccount) and falls back to the server proxy only when the direct attempt
// cannot open a tunnel. When viaServer is true, or for a non-cluster profile, it
// is the plain server-proxy client. The result satisfies portfwd.Dialer and is
// what port-forwarding call sites should pass to portfwd.Start. Compute viaServer
// via ViaServer so the CLI-flag > env > profile precedence is honored.
func (cn *Conn) Dialer(viaServer bool) portfwd.Dialer {
	proxy := cn.Client()
	if cn.KubeCluster == nil || viaServer {
		return proxy
	}
	return kubefwd.Fallback{
		Primary:   kubefwd.New(cn.KubeCluster.KubeContext, cn.KubeCluster.Namespace),
		Secondary: proxy,
	}
}

// ViaServer resolves whether workload streaming operations (logs, port-forward)
// route through the cornus server instead of talking to workload pods directly,
// honoring precedence: an explicit per-command flag (cliOverride) wins, else the
// CORNUS_VIA_SERVER env var, else the profile's via-server field, else false
// (direct-to-pod, the default). Each layer is a tri-state (nil / unset defers to
// the next); the env var accepts 1/true/yes/on and 0/false/no/off.
func (cn *Conn) ViaServer(cliOverride *bool) bool {
	return viaServerEnabled(cliOverride, os.Getenv("CORNUS_VIA_SERVER"), cn.ProfileViaServer)
}

// conduitMode applies the conduit-mode precedence (flag > env > profile > default
// port-forward) for a bare mode word. Each layer is normalized; an empty/whitespace
// value defers to the next layer. It is the mode-only core that ConduitConfig
// generalizes to URL specs; kept standalone because TestConduitMode exercises it.
func conduitMode(cliOverride, env, profile string) string {
	for _, v := range []string{cliOverride, env, profile} {
		if m := normalizeConduitMode(v); m != "" {
			return m
		}
	}
	return clientconduit.ModePortForward
}

// normalizeConduitMode lowercases/trims a mode value and folds the hyphenless
// spelling; "" means unset (defer). An unknown non-empty value is returned as-is
// so the eventual clientconduit.Start surfaces a clear error.
func normalizeConduitMode(s string) string {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "":
		return ""
	case "portforward":
		return clientconduit.ModePortForward
	default:
		return v
	}
}

// ConduitSpec is a parsed --conduit / conduit-mode value. A bare word selects
// only the mode (port-forward, socks5, none); a socks5[h]://host:port[?suffix=SUFFIX]
// URL selects socks5 mode and additionally carries the proxy's listen address
// and, optionally, its service-host suffix. socks5 and socks5h are accepted
// interchangeably: cornus's proxy always resolves service names remotely, so the
// distinction is cosmetic — the scheme is offered only so a URL copied from a
// proxy config is accepted verbatim.
type ConduitSpec struct {
	Mode      string // port-forward, socks5, none, an opaque value, or "" (unset)
	Listen    string // SOCKS5 bind address, from the URL authority
	Suffix    string // service-host suffix, from ?suffix=
	HasListen bool   // the URL carried an authority (Listen is meaningful)
	HasSuffix bool   // the URL carried ?suffix= (Suffix is meaningful, even if empty)
	// SessionLocal requests a private, session-scoped SOCKS5 proxy rather than
	// joining the one shared per tunnel config. It is meaningful only when
	// SessionLocalSet is true; a bare "socks5" word leaves it unset (defer to the
	// profile), while any socks5:// URL sets it — the ".shared" sentinel host to
	// false (explicitly shared) and every other authority to true.
	SessionLocal    bool
	SessionLocalSet bool
}

// ParseConduitSpec parses one conduit selector. "" yields the zero spec (unset,
// defer to the next layer). A value containing "://" is a SOCKS5 URL; anything
// else is a bare mode word (normalized). It errors on a malformed URL, a non-socks5
// scheme, a stray path, or an unknown query parameter so typos surface instead of
// silently no-op'ing.
func ParseConduitSpec(s string) (ConduitSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ConduitSpec{}, nil
	}
	if !strings.Contains(s, "://") {
		return ConduitSpec{Mode: normalizeConduitMode(s)}, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return ConduitSpec{}, fmt.Errorf("invalid conduit URL %q: %w", s, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
	default:
		return ConduitSpec{}, fmt.Errorf("invalid conduit URL %q: scheme must be socks5 or socks5h", s)
	}
	if u.Path != "" && u.Path != "/" {
		return ConduitSpec{}, fmt.Errorf("invalid conduit URL %q: unexpected path %q", s, u.Path)
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ConduitSpec{}, fmt.Errorf("invalid conduit URL %q: %w", s, err)
	}
	for k := range q {
		if k != "suffix" {
			return ConduitSpec{}, fmt.Errorf("invalid conduit URL %q: unknown query parameter %q", s, k)
		}
	}
	spec := ConduitSpec{Mode: clientconduit.ModeSocks5, SessionLocalSet: true}
	if u.Hostname() == ".shared" {
		// The reserved ".shared" host selects the shared proxy explicitly. It carries
		// no host of its own, but may pin the shared proxy's port
		// ("socks5://.shared:1085" -> the shared proxy binds 127.0.0.1:1085).
		spec.SessionLocal = false
		if p := u.Port(); p != "" {
			spec.Listen = "127.0.0.1:" + p
			spec.HasListen = true
		}
	} else {
		// Any other authority selects a private, session-local proxy: an empty
		// authority binds an ephemeral port, "host:port" binds that specific address.
		spec.SessionLocal = true
		if u.Host != "" {
			spec.Listen = u.Host
			spec.HasListen = true
		}
	}
	if q.Has("suffix") {
		spec.Suffix = q.Get("suffix")
		spec.HasSuffix = true
	}
	return spec, nil
}

// ConduitConfig builds the resolved clientconduit.Config for a session. It layers,
// lowest precedence first: the profile's stored conduit settings, then
// CORNUS_CONDUIT, then the per-command cliOverride — each overriding only the
// fields it names (a bare "socks5" sets the mode but leaves the profile's listen
// and suffix in place; a socks5://host:port URL also overrides those). The mode
// defaults to port-forward when no layer sets it. Pass clientconduit.ModeNone as
// cliOverride to honor a --no-forward-ports flag. A malformed override is kept as
// an opaque mode so clientconduit.Start surfaces the error at bind time.
func (cn *Conn) ConduitConfig(cliOverride string) clientconduit.Config {
	var cfg clientconduit.Config
	if cn.ProfileConduit != nil {
		cfg.Mode = normalizeConduitMode(cn.ProfileConduit.Mode)
		if s := cn.ProfileConduit.Socks5; s != nil {
			cfg.Socks5Listen = s.Listen
			cfg.Socks5Suffix = s.ServiceHostSuffix
			cfg.Socks5BareServiceNames = s.BareServiceNames
			for _, r := range s.Resolve {
				cfg.Socks5Resolve = append(cfg.Socks5Resolve, socks5.Rule{Pattern: r.Pattern, Replace: r.Replace})
			}
		}
	}
	for _, layer := range []string{os.Getenv("CORNUS_CONDUIT"), cliOverride} {
		spec, err := ParseConduitSpec(layer)
		if err != nil {
			cfg.Mode = strings.TrimSpace(layer)
			continue
		}
		if spec.Mode != "" {
			cfg.Mode = spec.Mode
		}
		if spec.SessionLocalSet {
			cfg.Socks5SessionLocal = spec.SessionLocal
			// A shared (.shared) or ephemeral session-local selector carries no bind
			// address; clear any inherited one so the ephemeral/default resolution below
			// applies. An explicit "host:port" reinstates it via HasListen just after.
			cfg.Socks5Listen = ""
		}
		if spec.HasListen {
			cfg.Socks5Listen = spec.Listen
		}
		if spec.HasSuffix {
			cfg.Socks5Suffix = spec.Suffix
		}
	}
	// A session-local proxy with no pinned address binds an ephemeral port, so
	// coexisting sessions never fight over one (and never fall back to the shared
	// default 1080).
	if cfg.Socks5SessionLocal && cfg.Socks5Listen == "" {
		cfg.Socks5Listen = "127.0.0.1:0"
	}
	if cfg.Mode == "" {
		cfg.Mode = clientconduit.ModePortForward
	}
	return cfg
}

// IngressMode resolves the ingress-via-conduit mode with precedence
// flag > CORNUS_INGRESS_CONDUIT > profile, normalizing to "native", "emulate", or ""
// (off). An explicit "off" at any layer disables it (returns "").
func (cn *Conn) IngressMode(cliOverride string) string {
	prof := ""
	if cn.ProfileConduit != nil && cn.ProfileConduit.Ingress != nil {
		prof = cn.ProfileConduit.Ingress.Mode
	}
	for _, layer := range []string{cliOverride, os.Getenv("CORNUS_INGRESS_CONDUIT"), prof} {
		switch m := normalizeIngressMode(layer); m {
		case "":
			continue
		case "off":
			return ""
		default:
			return m
		}
	}
	return ""
}

// normalizeIngressMode maps an ingress-mode token to "native", "emulate", "off", or
// "" (unset/unrecognized).
func normalizeIngressMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "native":
		return "native"
	case "emulate":
		return "emulate"
	case "off", "none", "disabled":
		return "off"
	default:
		return ""
	}
}

// ApplyIngress resolves the ingress-via-conduit config and stores it on cfg.Ingress
// (nil when off). The mode follows IngressMode's precedence; the emulate CA and (for
// native) the controller come from the profile, and a native mode with no configured
// controller learns it from the server's GET /.cornus/v1/info. It does I/O only for
// that last case; a fetch failure leaves the controller nil (AddIngress then reports
// it per service).
func (cn *Conn) ApplyIngress(ctx context.Context, cfg *clientconduit.Config, cliOverride string) {
	mode := cn.IngressMode(cliOverride)
	if mode == "" {
		cfg.Ingress = nil
		return
	}
	ic := &clientconduit.IngressConfig{
		Mode:         mode,
		SuffixDomain: strings.TrimPrefix(cfg.Socks5Suffix, "."),
	}
	if pc := cn.ProfileConduit; pc != nil && pc.Ingress != nil {
		ic.CAFile = pc.Ingress.CAFile
		ic.CAKeyFile = pc.Ingress.CAKeyFile
		if c := pc.Ingress.Controller; c != nil {
			ic.Controller = &ingressnative.Controller{
				KubeContext: c.KubeContext,
				Namespace:   c.Namespace,
				Service:     c.Service,
				HTTPPort:    c.HTTPPort,
				HTTPSPort:   c.HTTPSPort,
			}
		}
	}
	if mode == "native" && ic.Controller == nil {
		ic.Controller = cn.fetchController(ctx)
	}
	cfg.Ingress = ic
}

// fetchController learns the ingress controller Service from the server's advertised
// ingress facts (GET /.cornus/v1/info). Returns nil when the server advertises none.
// The client's kube context (from a cluster profile) is stamped on so the native
// dialer loads the right kubeconfig; the server cannot know it.
func (cn *Conn) fetchController(ctx context.Context) *ingressnative.Controller {
	info, err := cn.Client().Info(ctx)
	if err != nil || info.Ingress == nil || info.Ingress.Controller == nil {
		return nil
	}
	c := info.Ingress.Controller
	kctx := ""
	if cn.KubeCluster != nil {
		kctx = cn.KubeCluster.KubeContext
	}
	return &ingressnative.Controller{
		KubeContext: kctx,
		Namespace:   c.Namespace,
		Service:     c.Service,
		HTTPPort:    c.HTTPPort,
		HTTPSPort:   c.HTTPSPort,
	}
}

// viaServerEnabled applies the via-server precedence (flag > env > profile >
// default false). env is the raw CORNUS_VIA_SERVER value; an unrecognized value is
// treated as unset (defer to the profile) rather than an error, so a stray value
// never silently flips the transport.
func viaServerEnabled(cliOverride *bool, env string, profile *bool) bool {
	if cliOverride != nil {
		return *cliOverride
	}
	if v, ok := parseBoolish(env); ok {
		return v
	}
	if profile != nil {
		return *profile
	}
	return false
}

// parseBoolish parses a permissive boolean env value: 1/true/yes/on (true),
// 0/false/no/off (false), case-insensitively; ok is false for "" or anything
// unrecognized.
func parseBoolish(s string) (value, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// Client builds a *client.Client for the resolved endpoint, token, and TLS.
func (cn *Conn) Client() *client.Client {
	var opts []client.Option
	if cn.Token != "" {
		opts = append(opts, client.WithToken(cn.Token))
	}
	if cn.TLS != nil {
		opts = append(opts, client.WithTLSConfig(cn.TLS))
	}
	if cn.DialContext != nil {
		opts = append(opts, client.WithDialer(cn.DialContext))
	}
	return client.New(cn.Endpoint, opts...)
}

// ConfigPath returns the client config file path: the --config-file flag when set,
// otherwise the platform default (honoring $XDG_CONFIG_HOME).
func (r *Resolver) ConfigPath() (string, error) {
	if r.ConfigFile != "" {
		return r.ConfigFile, nil
	}
	return clientconfig.DefaultPath()
}

// AbsConfigPath returns the client config path as an absolute path: the explicit
// --config-file made absolute against the current cwd, else the platform default
// (already absolute). The unified client agent uses this so a relative
// --config-file resolves to the same file regardless of the agent's cwd, which is
// frozen at spawn to whichever client first started it.
func (r *Resolver) AbsConfigPath() (string, error) {
	p, err := r.ConfigPath()
	if err != nil || p == "" {
		return p, err
	}
	return filepath.Abs(p)
}

// LoadConfig loads the client config file (empty when the file does not exist).
func (r *Resolver) LoadConfig() (*clientconfig.File, error) {
	path, err := r.ConfigPath()
	if err != nil {
		return nil, err
	}
	return clientconfig.Load(path)
}

// Resolve resolves the connection to use. Endpoint precedence: the explicit
// per-command flag (already merged with that command's env by kong) wins; else the
// selected context's server; else, when the context only names an in-cluster
// Service, an automatic port-forward's local address. Token precedence: CORNUS_TOKEN
// env > a cluster-minted kube-auth token > the profile's static token. TLS material
// always comes from the context. A returned Endpoint of "" means no server is
// configured (the caller decides whether that is an error or a local fallback).
func (r *Resolver) Resolve(explicitServer string) (*Conn, error) {
	return r.ResolveWith(explicitServer, os.Getenv("CORNUS_TOKEN"))
}

// ResolveWith is Resolve with the environment-derived bearer token supplied
// explicitly (tokenEnv) instead of read from os.Getenv("CORNUS_TOKEN"). The
// unified client agent (pkg cmd/cornus/internal/clientagent) uses it: its process
// env is frozen at spawn, so it must resolve each client's connection with that
// client's token rather than the spawner's. tokenEnv == "" defers to the
// profile's kube-auth mint or static token, exactly as an unset CORNUS_TOKEN does.
func (r *Resolver) ResolveWith(explicitServer, tokenEnv string) (*Conn, error) {
	f, err := r.LoadConfig()
	if err != nil {
		return nil, err
	}
	name, cc, err := f.Resolve(r.Context)
	if err != nil {
		return nil, err
	}

	// Layer a per-project override (cornus-context.*) on top of the selected context
	// when one is discovered. It overrides field-by-field and can stand alone when no
	// context is selected, so it must be applied before the cc == nil short-circuit
	// below. Explicit per-command values still win: the endpoint flag (explicitServer)
	// and CORNUS_TOKEN (tokenEnv) are applied over cc downstream. cc points into the
	// freshly loaded (never cached) File, so mutating it in place is safe. The
	// override has already been provenance-vetted and field-gated by projectOverride.
	ov, ovPath, err := r.projectOverride()
	if err != nil {
		return nil, err
	}
	if ov != nil {
		baseHadToken := cc != nil && cc.Token != ""
		baseHadKubeAuth := cc != nil && cc.KubeAuth != nil
		if cc == nil {
			cc = &clientconfig.Context{}
		}
		clientconfig.Merge(cc, ov)
		if name == "" {
			name = "project"
		}
		// Token co-location: when the override redirects the endpoint but brings no
		// credential of its own, do not hand the selected context's token (or its
		// kube-auth mint) to that endpoint — a project file that only sets `server:`
		// must not silently exfiltrate the global token. An explicit --server
		// (explicitServer) is the user's own endpoint choice and is exempt.
		if explicitServer == "" && ov.SetsEndpoint() && !ov.SuppliesCredential() && (baseHadToken || baseHadKubeAuth) {
			cc.Token = ""
			cc.KubeAuth = nil
			slog.Warn("not sending the selected context's credential to the project override's endpoint; the override supplies none of its own — add a token to it or pass --token",
				"path", ovPath, "endpoint", ov.Server)
		}
	}

	cn := &Conn{Endpoint: explicitServer, Cleanup: func() {}}
	if cc == nil {
		return cn, nil
	}

	token := tokenEnv
	if token == "" {
		switch {
		case cc.KubeAuth != nil:
			token, err = mintKubeToken(cc)
			if err != nil {
				return nil, fmt.Errorf("context %q: %w", name, err)
			}
		case cc.Token != "":
			token = cc.Token
		}
	}
	cn.Token = token

	tc, err := cc.TLS.Config()
	if err != nil {
		return nil, fmt.Errorf("context %q: %w", name, err)
	}
	cn.TLS = tc
	cn.RegistryHost = cc.RegistryHost
	cn.KubeCluster = kubeCluster(cc)
	cn.ProfileViaServer = cc.ViaServer
	cn.ProfileConduit = cc.Conduit

	if cn.Endpoint == "" {
		cn.Endpoint = cc.Server
	}
	// port-forward and ssh-tunnel are mutually exclusive automatic-forward
	// mechanisms; refuse to guess when both are set with no explicit server. An
	// explicit Server (above) makes both inert with no error, preserving the existing
	// lenient precedent.
	if cn.Endpoint == "" && cc.PortForward != nil && cc.SSHTunnel != nil {
		return nil, fmt.Errorf("context %q: port-forward and ssh-tunnel are both configured with no explicit server; set at most one", name)
	}
	if cn.Endpoint == "" && cc.PortForward != nil {
		pf := cc.PortForward
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		fwd, err := svcforward.Start(sctx, svcforward.Options{
			KubeContext: pf.KubeContext,
			Namespace:   pf.Namespace,
			Service:     pf.Service,
			RemotePort:  pf.RemotePort,
		})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("context %q: %w", name, err)
		}
		// A port-forward tunnels raw bytes to the Service; the in-cluster cornus
		// server behind a ClusterIP speaks plain HTTP (TLS, when any, terminates at
		// the cluster edge, which a port-forward bypasses).
		cn.Endpoint = "http://" + fwd.LocalAddr
		cn.Cleanup = fwd.Close
	}
	if cn.Endpoint == "" && cc.SSHTunnel != nil {
		if err := startSSHTunnel(cn, cc.SSHTunnel, name); err != nil {
			return nil, err
		}
	}
	return cn, nil
}

// ttyPassphrasePrompt builds the interactive passphrase prompt for an encrypted
// SSH identity file on the first, foreground connect. It is a var so tests can
// substitute a deterministic prompt; it honors SSH_ASKPASS and the TTY (see
// sshclient.NewInteractivePrompt) and is nil when no interactive method exists.
var ttyPassphrasePrompt = func() func(keyPath string) ([]byte, error) {
	return sshclient.NewInteractivePrompt()
}

// sshDialer abstracts the two SSH transports (pure-Go Dialer and the ssh-binary
// BinaryForwarder) so ResolveWith can select one and treat it uniformly.
type sshDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	Close() error
}

// startSSHTunnel resolves the profile's ssh_config, establishes the SSH transport
// (pure-Go, or the ssh-binary unix-socket fallback for ProxyCommand / when forced),
// and points the connection at the remote server through it.
func startSSHTunnel(cn *Conn, st *clientconfig.SSHTunnel, name string) error {
	var idFiles []string
	if st.IdentityFile != "" {
		idFiles = []string{st.IdentityFile}
	}
	prof := sshclient.Options{
		User:             st.User,
		IdentityFiles:    idFiles,
		NoAgent:          st.NoAgent,
		KnownHosts:       st.KnownHosts,
		HostKey:          st.HostKey,
		Insecure:         st.Insecure,
		PromptPassphrase: ttyPassphrasePrompt(),
	}
	opts, err := sshclient.Resolve(st.Addr, prof, !st.NoSSHConfig)
	if err != nil {
		return fmt.Errorf("context %q: %w", name, err)
	}
	remote := remoteAddrOrDefault(st.RemoteAddr)

	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var dialer sshDialer
	if st.UseSSHBinary || opts.ProxyCommand != "" {
		dialer, err = sshclient.DialViaBinary(sctx, st.Addr, remote, true)
	} else {
		dialer, err = sshclient.Dial(sctx, opts)
	}
	if err != nil {
		return fmt.Errorf("context %q: %w", name, err)
	}

	scheme := "http"
	if st.RemoteTLS {
		scheme = "https"
	}
	cn.Endpoint = scheme + "://" + remote
	cn.DialContext = dialer.DialContext
	cn.Cleanup = func() { _ = dialer.Close() }
	return nil
}

// remoteAddrOrDefault returns the remote cornus listen address for an SSH-tunnel
// profile, defaulting to 127.0.0.1:5000 (the server's own --addr default).
func remoteAddrOrDefault(addr string) string {
	if addr == "" {
		return "127.0.0.1:5000"
	}
	return addr
}

// Require is Resolve for commands that cannot run without a server: it errors when
// no endpoint could be resolved.
func (r *Resolver) Require(explicitServer string) (*Conn, error) {
	cn, err := r.Resolve(explicitServer)
	if err != nil {
		return nil, err
	}
	if cn.Endpoint == "" {
		cn.Cleanup()
		return nil, fmt.Errorf("no server configured: pass the server flag, set the server env var, or select a context with a server (cornus config use-context)")
	}
	return cn, nil
}

// kubeCluster derives the cluster (context + namespace) a profile targets, so
// log/exec-style commands can reach workload pods directly. It returns nil unless
// the profile has a PortForward or KubeAuth block (i.e. it is a cluster profile).
// The context/namespace precedence mirrors mintKubeToken: PortForward's values win,
// falling back to KubeAuth's. An empty namespace is left for kubeclient.Load to
// resolve (kubeconfig context, then "default").
func kubeCluster(cc *clientconfig.Context) *KubeCluster {
	if cc.PortForward == nil && cc.KubeAuth == nil {
		return nil
	}
	var kctx, kns string
	if cc.PortForward != nil {
		kctx, kns = cc.PortForward.KubeContext, cc.PortForward.Namespace
	}
	if cc.KubeAuth != nil {
		if kctx == "" {
			kctx = cc.KubeAuth.KubeContext
		}
		if kns == "" {
			kns = cc.KubeAuth.Namespace
		}
	}
	return &KubeCluster{KubeContext: kctx, Namespace: kns}
}

// mintKubeToken mints the cluster ServiceAccount token for a kube-auth profile. The
// kube context and namespace default to the port-forward block's when the kube-auth
// block leaves them empty, so a pf profile only needs the service account + audience.
func mintKubeToken(cc *clientconfig.Context) (string, error) {
	ka := cc.KubeAuth
	kctx, kns := ka.KubeContext, ka.Namespace
	if cc.PortForward != nil {
		if kctx == "" {
			kctx = cc.PortForward.KubeContext
		}
		if kns == "" {
			kns = cc.PortForward.Namespace
		}
	}
	tctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return kubeauth.Token(tctx, kubeauth.Options{
		KubeContext:       kctx,
		Namespace:         kns,
		ServiceAccount:    ka.ServiceAccount,
		Audience:          ka.Audience,
		ExpirationSeconds: ka.ExpirationSeconds,
	})
}
