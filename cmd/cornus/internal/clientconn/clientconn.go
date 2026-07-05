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
	"strconv"
	"strings"
	"time"

	"cornus/pkg/client"
	"cornus/pkg/clientconduit"
	"cornus/pkg/clientconfig"
	"cornus/pkg/ingressemu"
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
	// Config stores the profile and environment layers; command overrides merge on demand.
	Config Config
}

// Config is one layer of session networking configuration. Values from a later
// layer override only the fields they explicitly set. The unexported markers retain
// explicit empty values that the persisted clientconfig schema otherwise represents
// as zero values.
type Config struct {
	ViaServer    *bool
	Conduit      *clientconfig.Conduit
	SessionLocal *bool

	clearSocks5Listen bool
	clearSocks5Suffix bool
	ingressModeSet    bool
}

func configFromContext(ctx *clientconfig.Context) Config {
	if ctx == nil {
		return Config{}
	}
	config := Config{ViaServer: cloneBool(ctx.ViaServer), Conduit: ctx.Conduit.Clone()}
	config.ingressModeSet = config.Conduit != nil && config.Conduit.Ingress != nil
	return config
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// Merge returns a deep copy of c with override applied field-by-field.
func (c Config) Merge(override Config) Config {
	result := Config{
		ViaServer:         cloneBool(c.ViaServer),
		Conduit:           c.Conduit.Clone(),
		SessionLocal:      cloneBool(c.SessionLocal),
		clearSocks5Listen: c.clearSocks5Listen,
		clearSocks5Suffix: c.clearSocks5Suffix,
		ingressModeSet:    c.ingressModeSet,
	}
	if override.ViaServer != nil {
		result.ViaServer = cloneBool(override.ViaServer)
	}
	if override.clearSocks5Listen {
		result.ensureSocks5().Listen = ""
	}
	if override.clearSocks5Suffix {
		result.ensureSocks5().ServiceHostSuffix = ""
	}
	if override.Conduit != nil {
		result.Conduit = result.Conduit.Merge(override.Conduit)
	}
	if override.ingressModeSet {
		ingress := result.ensureIngress()
		ingress.Mode = ""
		if override.Conduit != nil && override.Conduit.Ingress != nil {
			ingress.Mode = override.Conduit.Ingress.Mode
		}
		result.ingressModeSet = true
	}
	if override.SessionLocal != nil {
		result.SessionLocal = cloneBool(override.SessionLocal)
	}
	result.clearSocks5Listen = result.clearSocks5Listen || override.clearSocks5Listen
	result.clearSocks5Suffix = result.clearSocks5Suffix || override.clearSocks5Suffix
	return result
}

func (c *Config) ensureSocks5() *clientconfig.Socks5 {
	if c.Conduit == nil {
		c.Conduit = &clientconfig.Conduit{}
	}
	if c.Conduit.Socks5 == nil {
		c.Conduit.Socks5 = &clientconfig.Socks5{}
	}
	return c.Conduit.Socks5
}

func (c *Config) ensureIngress() *clientconfig.Ingress {
	if c.Conduit == nil {
		c.Conduit = &clientconfig.Conduit{}
	}
	if c.Conduit.Ingress == nil {
		c.Conduit.Ingress = &clientconfig.Ingress{}
	}
	return c.Conduit.Ingress
}

// resolveConfig applies low-to-high-precedence layers in order.
func resolveConfig(configs ...Config) Config {
	var result Config
	for _, config := range configs {
		result = result.Merge(config)
	}
	return result
}

// ResolveConfig overlays command-specific values on the profile and environment
// configuration captured when the connection was resolved.
func (cn *Conn) ResolveConfig(overrides ...Config) Config {
	layers := make([]Config, 0, len(overrides)+1)
	layers = append(layers, cn.Config)
	layers = append(layers, overrides...)
	return resolveConfig(layers...)
}

func (c Config) toConduitConfig() clientconduit.Config {
	var cfg clientconduit.Config
	if c.Conduit != nil {
		cfg.Mode = clientconduit.Mode(normalizeConduitMode(c.Conduit.Mode))
		if socks := c.Conduit.Socks5; socks != nil {
			cfg.Socks5Listen = socks.Listen
			cfg.Socks5Suffix = socks.ServiceHostSuffix
			if socks.BareServiceNames != nil {
				cfg.Socks5BareServiceNames = cloneBool(socks.BareServiceNames)
			}
			for _, rule := range socks.Resolve {
				cfg.Socks5Resolve = append(cfg.Socks5Resolve, socks5.Rule{Pattern: rule.Pattern, Replace: rule.Replace})
			}
		}
	}
	if c.SessionLocal != nil {
		cfg.Socks5SessionLocal = *c.SessionLocal
	}
	return cfg
}

// ConfigFromEnv parses the session-networking environment variables.
func ConfigFromEnv() (Config, error) {
	var viaServer *bool
	if value, ok := parseBoolish(os.Getenv("CORNUS_VIA_SERVER")); ok {
		viaServer = &value
	}
	return ConfigFromOptions(
		viaServer,
		os.Getenv("CORNUS_CONDUIT"),
		os.Getenv("CORNUS_INGRESS_CONDUIT"),
		os.Getenv("CORNUS_INGRESS_CONTROLLER"),
		os.Getenv("CORNUS_INGRESS_EMULATED_CA"),
		os.Getenv("CORNUS_INGRESS_EMULATED_CA_KEY"),
	)
}

// ConfigFromOptions parses one explicit configuration layer. Empty strings are
// unset and defer to lower-precedence layers.
func ConfigFromOptions(viaServer *bool, conduit, ingressMode, ingressController, caFile, caKeyFile string) (Config, error) {
	config := Config{ViaServer: cloneBool(viaServer)}
	if conduit != "" {
		spec, err := ParseConduitSpec(conduit)
		if err != nil {
			return Config{}, err
		}
		config = config.Merge(spec.config())
	}
	if ingressMode != "" {
		mode, ok := normalizeIngressMode(ingressMode)
		if !ok {
			return Config{}, fmt.Errorf("invalid ingress conduit mode %q: want native, emulate, or off", ingressMode)
		}
		config.ensureIngress().Mode = mode
		config.ingressModeSet = true
	}
	if ingressController != "" {
		controller, err := ParseIngressController(ingressController)
		if err != nil {
			return Config{}, err
		}
		config.ensureIngress().Controller = controller
	}
	if caFile != "" {
		config.ensureIngress().CAFile = caFile
	}
	if caKeyFile != "" {
		config.ensureIngress().CAKeyFile = caKeyFile
	}
	return config, nil
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
	viaServer := cn.ResolveConfig(Config{ViaServer: cliOverride}).ViaServer
	if viaServer == nil {
		return false
	}
	return *viaServer
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

// conduitMode is the mode-only form of the configuration precedence used by
// legacy callers and focused tests.
func conduitMode(cli, env, profile string) string {
	for _, value := range []string{cli, env, profile} {
		if mode := normalizeConduitMode(value); mode != "" {
			return mode
		}
	}
	return clientconduit.ModePortForward
}

// ConduitSpec is a parsed conduit selector. Presence bits preserve explicit
// empty URL fields so a higher-precedence layer can clear an inherited value.
type ConduitSpec struct {
	Mode            string
	Listen          string
	Suffix          string
	HasListen       bool
	HasSuffix       bool
	SessionLocal    bool
	SessionLocalSet bool
}

func (s ConduitSpec) config() Config {
	config := Config{Conduit: &clientconfig.Conduit{Mode: s.Mode}}
	if s.HasListen || s.HasSuffix {
		config.Conduit.Socks5 = &clientconfig.Socks5{}
	}
	if s.HasListen {
		config.Conduit.Socks5.Listen = s.Listen
	}
	if s.HasSuffix {
		config.Conduit.Socks5.ServiceHostSuffix = s.Suffix
		config.clearSocks5Suffix = s.Suffix == ""
	}
	if s.SessionLocalSet {
		config.SessionLocal = cloneBool(&s.SessionLocal)
		config.clearSocks5Listen = true
	}
	return config
}

// ParseConduitSpec parses one conduit selector. A bare word sets only the mode;
// a socks5 URL also carries the fields explicitly present in the URL.
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
	for key := range q {
		if key != "suffix" {
			return ConduitSpec{}, fmt.Errorf("invalid conduit URL %q: unknown query parameter %q", s, key)
		}
	}
	spec := ConduitSpec{Mode: clientconduit.ModeSocks5, SessionLocalSet: true}
	if u.Hostname() == ".shared" {
		if port := u.Port(); port != "" {
			spec.Listen = "127.0.0.1:" + port
			spec.HasListen = true
		}
	} else {
		spec.SessionLocal = true
		if u.Host != "" {
			// The conduit URL selector has no non-loopback opt-in (that is
			// `cornus socks5 --allow-non-loopback`, which does not take a URL), so a
			// non-loopback host here is always a misconfiguration — reject it at parse
			// time for the clearest, earliest error. clientconduit.Start is the backstop.
			if socks5.LooksNonLoopback(u.Host) {
				return ConduitSpec{}, fmt.Errorf("invalid conduit URL %q: non-loopback bind address %q — the SOCKS5 conduit has no authentication, so off-host it is an open proxy (use a loopback address, or run `cornus socks5 --allow-non-loopback` for an intentional off-host proxy)", s, u.Host)
			}
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

// ParseIngressController parses <namespace>/<service>[:httpPort/httpsPort],
// defaulting omitted ports to 80 and 443.
func ParseIngressController(s string) (*clientconfig.IngressController, error) {
	namespace, rest, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok || namespace == "" || rest == "" {
		return nil, fmt.Errorf("invalid ingress controller %q: want <namespace>/<service>[:httpPort/httpsPort]", s)
	}
	service, ports, hasPorts := strings.Cut(rest, ":")
	if service == "" {
		return nil, fmt.Errorf("invalid ingress controller %q: empty service", s)
	}
	controller := &clientconfig.IngressController{Namespace: namespace, Service: service, HTTPPort: 80, HTTPSPort: 443}
	if !hasPorts {
		return controller, nil
	}
	httpPort, httpsPort, _ := strings.Cut(ports, "/")
	for _, value := range []struct {
		raw  string
		dest *int
	}{{httpPort, &controller.HTTPPort}, {httpsPort, &controller.HTTPSPort}} {
		if strings.TrimSpace(value.raw) == "" {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSpace(value.raw))
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid ingress controller %q: bad port %q (want 1-65535)", s, value.raw)
		}
		*value.dest = port
	}
	return controller, nil
}

// ConduitConfigFor resolves profile, environment, and explicit configuration and
// converts it to the runtime conduit representation.
func (cn *Conn) ConduitConfigFor(overrides ...Config) clientconduit.Config {
	cfg := cn.ResolveConfig(overrides...).toConduitConfig()
	if cfg.Socks5SessionLocal && cfg.Socks5Listen == "" {
		cfg.Socks5Listen = "127.0.0.1:0"
	}
	if cfg.Mode == "" {
		cfg.Mode = clientconduit.ModePortForward
	}
	return cfg
}

// ConduitConfig is the mode-only convenience used by commands without ingress
// options. A malformed value is retained as an opaque mode for Start to reject.
func (cn *Conn) ConduitConfig(cliOverride string) clientconduit.Config {
	spec, err := ParseConduitSpec(cliOverride)
	if err != nil {
		return cn.ConduitConfigFor(Config{Conduit: &clientconfig.Conduit{Mode: strings.TrimSpace(cliOverride)}})
	}
	return cn.ConduitConfigFor(spec.config())
}

func (c Config) ingressMode() string {
	if c.Conduit == nil || c.Conduit.Ingress == nil {
		return ""
	}
	mode, _ := normalizeIngressMode(c.Conduit.Ingress.Mode)
	return mode
}

// IngressMode resolves the ingress mode with a mode-only CLI override.
func (cn *Conn) IngressMode(cliOverride string) string {
	override, _ := ConfigFromOptions(nil, "", cliOverride, "", "", "")
	return cn.ResolveConfig(override).ingressMode()
}

// normalizeIngressMode maps an ingress token to its runtime form.
func normalizeIngressMode(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "native":
		return "native", true
	case "emulate":
		return "emulate", true
	case "off", "none", "disabled", "":
		return "", true
	default:
		return "", false
	}
}

// ApplyIngress applies a mode-only command override.
func (cn *Conn) ApplyIngress(ctx context.Context, cfg *clientconduit.Config, cliOverride string) {
	override, _ := ConfigFromOptions(nil, "", cliOverride, "", "", "")
	cn.ApplyIngressConfig(ctx, cfg, override)
}

// ApplyIngressConfig resolves every ingress field and stores the runtime form on cfg.
func (cn *Conn) ApplyIngressConfig(ctx context.Context, cfg *clientconduit.Config, overrides ...Config) {
	config := cn.ResolveConfig(overrides...)
	mode := config.ingressMode()
	if mode == "" {
		cfg.Ingress = nil
		return
	}
	ingress := config.Conduit.Ingress
	runtimeIngress := &clientconduit.IngressConfig{
		Mode:         clientconduit.IngressMode(mode),
		SuffixDomain: strings.TrimPrefix(cfg.Socks5Suffix, "."),
		CAFile:       ingress.CAFile,
		CAKeyFile:    ingress.CAKeyFile,
	}
	for _, cert := range ingress.Certificates {
		certFile, keyFile := cert.Certificate, cert.Key
		if abs, err := filepath.Abs(certFile); err == nil {
			certFile = abs
		}
		if abs, err := filepath.Abs(keyFile); err == nil {
			keyFile = abs
		}
		runtimeIngress.Certificates = append(runtimeIngress.Certificates, ingressemu.CertificateSource{Pattern: cert.Pattern, CertFile: certFile, KeyFile: keyFile})
	}
	if controller := ingress.Controller; controller != nil && controller.Service != "" {
		kubeContext := controller.KubeContext
		if kubeContext == "" && cn.KubeCluster != nil {
			kubeContext = cn.KubeCluster.KubeContext
		}
		runtimeIngress.Controller = &ingressnative.Controller{
			KubeContext: kubeContext,
			Namespace:   controller.Namespace,
			Service:     controller.Service,
			HTTPPort:    controller.HTTPPort,
			HTTPSPort:   controller.HTTPSPort,
		}
	}
	if mode == "native" && runtimeIngress.Controller == nil {
		runtimeIngress.Controller = cn.fetchController(ctx)
	}
	cfg.Ingress = runtimeIngress
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

	envConfig, err := ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	cn := &Conn{
		Endpoint: explicitServer,
		Cleanup:  func() {},
		Config:   resolveConfig(configFromContext(cc), envConfig),
	}
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
