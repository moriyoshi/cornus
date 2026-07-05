package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/clientconfig"
	"cornus/pkg/svcforward"
)

// parseResolveRules parses --socks5-resolve entries, each "PATTERN=REPLACE" (split
// on the first '='), validating that PATTERN compiles as a regexp.
func parseResolveRules(specs []string) ([]clientconfig.ResolveRule, error) {
	rules := make([]clientconfig.ResolveRule, 0, len(specs))
	for _, s := range specs {
		pattern, replace, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --socks5-resolve %q: want PATTERN=REPLACE", s)
		}
		if pattern == "" {
			return nil, fmt.Errorf("invalid --socks5-resolve %q: empty pattern", s)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("invalid --socks5-resolve pattern %q: %w", pattern, err)
		}
		rules = append(rules, clientconfig.ResolveRule{Pattern: pattern, Replace: replace})
	}
	return rules, nil
}

// normalizeIngressConduitMode validates an --ingress-conduit value, returning the
// stored form: "native", "emulate", or "" (off / none / disabled / empty).
func normalizeIngressConduitMode(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "native":
		return "native", nil
	case "emulate":
		return "emulate", nil
	case "off", "none", "disabled", "":
		return "", nil
	default:
		return "", fmt.Errorf("invalid --ingress-conduit %q: want native, emulate, or off", s)
	}
}

// parseIngressControllerFlag parses <namespace>/<service>[:httpPort/httpsPort] into a
// clientconfig.IngressController, defaulting the ports to 80 / 443.
func parseIngressControllerFlag(s string) (*clientconfig.IngressController, error) {
	ns, rest, ok := strings.Cut(s, "/")
	if !ok || ns == "" || rest == "" {
		return nil, fmt.Errorf("invalid --ingress-controller %q: want <namespace>/<service>[:httpPort/httpsPort]", s)
	}
	svc, ports, hasPorts := strings.Cut(rest, ":")
	if svc == "" {
		return nil, fmt.Errorf("invalid --ingress-controller %q: empty service", s)
	}
	c := &clientconfig.IngressController{Namespace: ns, Service: svc, HTTPPort: 80, HTTPSPort: 443}
	if hasPorts {
		httpStr, httpsStr, _ := strings.Cut(ports, "/")
		if err := parsePortInto(s, httpStr, &c.HTTPPort); err != nil {
			return nil, err
		}
		if err := parsePortInto(s, httpsStr, &c.HTTPSPort); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// parsePortInto parses a 1-65535 port from p (empty leaves *dst untouched), erroring
// with context flag when invalid.
func parsePortInto(flag, p string, dst *int) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return fmt.Errorf("invalid --ingress-controller %q: bad port %q (want 1-65535)", flag, p)
	}
	*dst = n
	return nil
}

// ConfigCmd manages the client-side connection profiles (contexts) used to reach
// a remote cornus server, mirroring the shape of `kubectl config`. The file lives
// at the platform user config dir (or --config / CORNUS_CONFIG).
type ConfigCmd struct {
	GetContexts    ConfigGetContextsCmd    `kong:"cmd,name='get-contexts',help='List the configured connection profiles.'"`
	CurrentContext ConfigCurrentContextCmd `kong:"cmd,name='current-context',help='Print the current (default) context name.'"`
	UseContext     ConfigUseContextCmd     `kong:"cmd,name='use-context',help='Set the current (default) context.'"`
	SetContext     ConfigSetContextCmd     `kong:"cmd,name='set-context',help='Create or update a context.'"`
	DeleteContext  ConfigDeleteContextCmd  `kong:"cmd,name='delete-context',help='Remove a context.'"`
	View           ConfigViewCmd           `kong:"cmd,help='Print the client config file (bearer tokens redacted unless --show-tokens).'"`
}

// ConfigGetContextsCmd lists the contexts.
type ConfigGetContextsCmd struct{}

func (c *ConfigGetContextsCmd) Run(cli *CLI) error {
	f, err := cli.loadConfig()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(f.Contexts))
	for name := range f.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)

	tbl := cli.out().Table("CURRENT", "NAME", "SERVER")
	for _, name := range names {
		cur := ""
		if name == f.CurrentContext {
			cur = "*"
		}
		// A null-valued context in the file (e.g. `contexts:\n  prod:`) unmarshals to
		// a nil pointer; print the row without dereferencing it.
		server := ""
		if ctx := f.Contexts[name]; ctx != nil {
			server = ctx.Server
			switch {
			case server != "":
				// explicit server
			case ctx.PortForward != nil:
				pf := ctx.PortForward
				if pf.Service == "" {
					// A namespace-only block (e.g. saved with --no-detect); the Service is
					// detected or supplied later.
					server = fmt.Sprintf("(port-forward ns/%s)", pf.Namespace)
				} else {
					server = fmt.Sprintf("(port-forward svc/%s:%d)", pf.Service, pf.RemotePort)
				}
			case ctx.SSHTunnel != nil:
				st := ctx.SSHTunnel
				dest := st.Addr
				if st.User != "" {
					dest = st.User + "@" + st.Addr
				}
				remote := st.RemoteAddr
				if remote == "" {
					remote = "127.0.0.1:5000"
				}
				server = fmt.Sprintf("(ssh-tunnel %s -> %s)", dest, remote)
			}
		}
		tbl.Row(cur, name, server)
	}
	return tbl.Flush()
}

// ConfigCurrentContextCmd prints the current context name.
type ConfigCurrentContextCmd struct{}

func (c *ConfigCurrentContextCmd) Run(cli *CLI) error {
	f, err := cli.loadConfig()
	if err != nil {
		return err
	}
	if f.CurrentContext == "" {
		return fmt.Errorf("no current context set")
	}
	cli.out().Item("%s", f.CurrentContext)
	return nil
}

// ConfigUseContextCmd sets the current context.
type ConfigUseContextCmd struct {
	Name string `kong:"arg,required,help='Context to make current.'"`
}

func (c *ConfigUseContextCmd) Run(cli *CLI) error {
	path, err := cli.configPath()
	if err != nil {
		return err
	}
	f, err := clientconfig.Load(path)
	if err != nil {
		return err
	}
	if _, ok := f.Contexts[c.Name]; !ok {
		return fmt.Errorf("context %q not found", c.Name)
	}
	f.CurrentContext = c.Name
	if err := clientconfig.Save(path, f); err != nil {
		return err
	}
	cli.out().Done("switched to context %q", c.Name)
	return nil
}

// ConfigSetContextCmd creates or updates a context. By default it *replaces* any
// existing context of the same name: the result is exactly what this invocation
// specifies (--from-file layers, then flags, then --from-file-override layers).
// Pass --merge to instead layer those settings onto the existing context, leaving
// unset fields in place — the edit-in-place mode. --insecure-skip-verify only
// enables the setting.
type ConfigSetContextCmd struct {
	Name          string `kong:"arg,required,help='Context name.'"`
	Server        string `kong:"name='server',help='Cornus server base URL (http(s)://host:port).'"`
	RegistryHost  string `kong:"name='registry-host',help='Override the host[:port] built images are tagged with and deploy pull refs carry. Empty (the usual case) derives it from the server (GET /.cornus/v1/info), falling back to the endpoint host. Set only for topologies the server cannot introspect.'"`
	Token         string `kong:"name='token',help='Bearer token / JWT sent as Authorization: Bearer.'"`
	CACert        string `kong:"name='tls-ca-cert',type='path',help='PEM CA bundle that verifies the server certificate.'"`
	ClientCert    string `kong:"name='tls-client-cert',type='path',help='PEM client certificate for mTLS (requires --tls-client-key).'"`
	ClientKey     string `kong:"name='tls-client-key',type='path',help='PEM client key for mTLS (requires --tls-client-cert).'"`
	ServerName    string `kong:"name='tls-server-name',help='Override the certificate hostname (SNI) verified against, for when the dial address differs from the cert identity (e.g. an SSH-tunnel endpoint dialed as 127.0.0.1).'"`
	Insecure      bool   `kong:"name='insecure-skip-verify',help='Disable server certificate verification (testing only).'"`
	Namespace     string `kong:"name='namespace',short='n',help='Namespace of the cornus install; auto-detects the Service and port unless --pf-service or --no-detect is set.'"`
	NoDetect      bool   `kong:"name='no-detect',help='Store --namespace without contacting the cluster to detect the Service.'"`
	PFKubeContext string `kong:"name='pf-kube-context',help='kubeconfig context for the automatic port-forward.'"`
	PFNamespace   string `kong:"name='pf-namespace',help='Namespace of the in-cluster Service to port-forward to (alias for --namespace).'"`
	PFService     string `kong:"name='pf-service',help='Name of the in-cluster Service to port-forward to (skips auto-detection).'"`
	PFRemotePort  int    `kong:"name='pf-remote-port',help='Service port to port-forward to.'"`

	KubeAuthServiceAccount string `kong:"name='kube-auth-service-account',help='Mint the bearer token from this cluster ServiceAccount via the TokenRequest API (instead of a static --token).'"`
	KubeAuthAudience       string `kong:"name='kube-auth-audience',help='Audience for the minted ServiceAccount token; must match the server CORNUS_JWT_AUDIENCE.'"`
	KubeAuthNamespace      string `kong:"name='kube-auth-namespace',help='Namespace of the ServiceAccount (defaults to --pf-namespace).'"`
	KubeAuthKubeContext    string `kong:"name='kube-auth-kube-context',help='kubeconfig context to mint the token through (defaults to --pf-kube-context).'"`
	KubeAuthExpiration     int64  `kong:"name='kube-auth-expiration-seconds',help='Requested token lifetime in seconds (0 = default 3600).'"`

	SSHHost         string `kong:"name='ssh-host',help='Reach the server through an SSH tunnel to this destination: an ssh_config Host alias or host[:port]. The docker/containerd-host analogue of --pf-* (mutually exclusive with them).'"`
	SSHUser         string `kong:"name='ssh-user',help='SSH login user (defaults to ssh_config, then the current user).'"`
	SSHRemoteAddr   string `kong:"name='ssh-remote-addr',help='Address the remote cornus server listens on, from the remote host (default 127.0.0.1:5000).'"`
	SSHIdentityFile string `kong:"name='ssh-identity-file',type='path',help='PEM private key for SSH public-key auth (defaults to the ssh-agent and ssh_config IdentityFile).'"`
	SSHNoAgent      bool   `kong:"name='ssh-no-agent',help='Do not use the local ssh-agent for the tunnel (mainly for the too-many-auth-failures case).'"`
	SSHKnownHosts   string `kong:"name='ssh-known-hosts',type='path',help='known_hosts file for SSH host-key verification (defaults to ssh_config, then ~/.ssh/known_hosts).'"`
	SSHHostKey      string `kong:"name='ssh-host-key',help='Pin a single SSH host key as an authorized_keys-format line.'"`
	SSHInsecure     bool   `kong:"name='ssh-insecure-host-key',help='Skip SSH host-key verification (dev only).'"`
	SSHNoConfig     bool   `kong:"name='ssh-no-config',help='Do not consult ~/.ssh/config or /etc/ssh/ssh_config; use only the --ssh-* flags.'"`
	SSHUseBinary    bool   `kong:"name='ssh-use-binary',help='Force the system ssh binary (unix-socket forward) for full ssh_config fidelity (ProxyCommand, Match). Auto-selected when the host has a ProxyCommand.'"`
	SSHTLS          bool   `kong:"name='ssh-tls',help='Dial the tunneled endpoint over https:// because the remote server terminates TLS (usually paired with --tls-server-name).'"`

	ViaServer *bool `kong:"name='via-server',negatable,help='Route workload logs/port-forward through the cornus server proxy instead of reaching pods directly with your kubeconfig (cluster profiles only). --no-via-server forces the direct path. Overridden per-run by CORNUS_VIA_SERVER or a command --via-server flag.'"`

	ConduitMode         string   `kong:"name='conduit-mode',default='',help='How a client session (deploy --server, compose up) exposes ports: port-forward (per-port local listeners, the default), socks5 (one split-tunnel proxy reaching services by name), or a socks5://host:port[?suffix=SUFFIX] URL that also sets the proxy bind address and service-host suffix (socks5h:// is accepted as a synonym). Overridden per-run by CORNUS_CONDUIT or a command --conduit flag.'"`
	Socks5ServiceSuffix string   `kong:"name='socks5-service-host-suffix',help='Host suffix whose SOCKS5 CONNECT targets are tunneled to the matching service (default .cornus.internal); other hosts conduit directly.'"`
	Socks5Resolve       []string `kong:"name='socks5-resolve',help='Advanced SOCKS5 resolution rule PATTERN=REPLACE (repeatable, ordered, first match wins); replaces the suffix default. PATTERN matches host:port, REPLACE yields service:port (sed-style \\1 backrefs).'"`

	IngressConduit      string `kong:"name='ingress-conduit',default='',help='Reach a workload ingress (x-cornus-ingress) through the SOCKS5 conduit: native (tunnel to the real cluster ingress controller), emulate (a client-side reverse proxy with a generated cert), or off. Requires conduit-mode socks5. Overridden per-run by CORNUS_INGRESS_CONDUIT or a command --ingress-conduit flag.'"`
	IngressController   string `kong:"name='ingress-controller',help='Native-mode ingress controller Service to tunnel to, as <namespace>/<service>[:httpPort/httpsPort]. Empty learns it from the server (GET /.cornus/v1/info).'"`
	IngressEmulateCA    string `kong:"name='ingress-emulate-ca',type='path',help='Emulate-mode: PEM CA certificate that signs the per-host leaf certs (with --ingress-emulate-ca-key). Empty uses a generated, persisted session CA.'"`
	IngressEmulateCAKey string `kong:"name='ingress-emulate-ca-key',type='path',help='Emulate-mode: PEM CA private key paired with --ingress-emulate-ca.'"`

	FromFile         []string `kong:"name='from-file',type='path',help='Load a context definition (bare Context object, JSON/YAML) as a base layer that individual --flags override; repeatable, later files override earlier ones.'"`
	FromFileOverride []string `kong:"name='from-file-override',type='path',help='Load a context definition (bare Context object, JSON/YAML) that overrides the individual --flags; repeatable, later files override earlier ones.'"`

	Merge bool `kong:"name='merge',help='Merge the given settings into the existing context instead of replacing it: unset fields keep their stored value (edit-in-place). Without --merge the context is replaced with exactly what this invocation specifies.'"`
}

// confirmSetDefaultContext reports whether a newly created first context should
// become the default (current) context. It delegates to the output driver's
// Confirm, which prompts only on a terminal and returns the default (false — so
// scripts and CI stay deterministic) otherwise. It is a var so tests can
// simulate the answer without a PTY.
var confirmSetDefaultContext = func(d *cliout.Driver, name string) bool {
	return d.Confirm(fmt.Sprintf("Set context %q as the default (current) context?", name), false)
}

func (c *ConfigSetContextCmd) Run(cli *CLI) error {
	path, err := cli.configPath()
	if err != nil {
		return err
	}
	f, err := clientconfig.Load(path)
	if err != nil {
		return err
	}
	// firstContext is true only when the config has no contexts yet, so the one
	// being created is the very first — the case where we offer to make it default.
	firstContext := len(f.Contexts) == 0
	// --merge seeds the working context from the stored one so unset fields are
	// preserved (edit-in-place); the default starts empty, so the saved context ends
	// up as exactly what this invocation specifies (a full replace).
	var ctx *clientconfig.Context
	if c.Merge {
		ctx = f.Contexts[c.Name]
	}
	if ctx == nil {
		ctx = &clientconfig.Context{}
	}
	// Load both file layers up front so a bad path or parse error surfaces before we
	// mutate anything. --from-file is a base layer beneath the CLI flags;
	// --from-file-override sits above them. Within each, later files win.
	baseLayers, err := loadContextFiles(c.FromFile)
	if err != nil {
		return err
	}
	overrideLayers, err := loadContextFiles(c.FromFileOverride)
	if err != nil {
		return err
	}
	for _, src := range baseLayers {
		clientconfig.Merge(ctx, src)
	}
	if c.Server != "" {
		ctx.Server = c.Server
	}
	if c.RegistryHost != "" {
		ctx.RegistryHost = c.RegistryHost
	}
	if c.Token != "" {
		ctx.Token = c.Token
	}
	if c.CACert != "" || c.ClientCert != "" || c.ClientKey != "" || c.ServerName != "" || c.Insecure {
		if ctx.TLS == nil {
			ctx.TLS = &clientconfig.TLS{}
		}
		if c.CACert != "" {
			ctx.TLS.CACert = c.CACert
		}
		if c.ClientCert != "" {
			ctx.TLS.ClientCert = c.ClientCert
		}
		if c.ClientKey != "" {
			ctx.TLS.ClientKey = c.ClientKey
		}
		if c.ServerName != "" {
			ctx.TLS.ServerName = c.ServerName
		}
		if c.Insecure {
			ctx.TLS.InsecureSkipVerify = true
		}
	}
	if c.SSHHost != "" || c.SSHUser != "" || c.SSHRemoteAddr != "" || c.SSHIdentityFile != "" ||
		c.SSHNoAgent || c.SSHKnownHosts != "" || c.SSHHostKey != "" || c.SSHInsecure ||
		c.SSHNoConfig || c.SSHUseBinary || c.SSHTLS {
		if ctx.SSHTunnel == nil {
			ctx.SSHTunnel = &clientconfig.SSHTunnel{}
		}
		st := ctx.SSHTunnel
		if c.SSHHost != "" {
			st.Addr = c.SSHHost
		}
		if c.SSHUser != "" {
			st.User = c.SSHUser
		}
		if c.SSHRemoteAddr != "" {
			st.RemoteAddr = c.SSHRemoteAddr
		}
		if c.SSHIdentityFile != "" {
			st.IdentityFile = c.SSHIdentityFile
		}
		if c.SSHKnownHosts != "" {
			st.KnownHosts = c.SSHKnownHosts
		}
		if c.SSHHostKey != "" {
			st.HostKey = c.SSHHostKey
		}
		// The bool toggles only enable (like --insecure-skip-verify), so an unrelated
		// edit leaves a stored true in place.
		if c.SSHNoAgent {
			st.NoAgent = true
		}
		if c.SSHInsecure {
			st.Insecure = true
		}
		if c.SSHNoConfig {
			st.NoSSHConfig = true
		}
		if c.SSHUseBinary {
			st.UseSSHBinary = true
		}
		if c.SSHTLS {
			st.RemoteTLS = true
		}
	}
	namespace := c.Namespace
	if namespace == "" {
		namespace = c.PFNamespace
	}
	if c.PFKubeContext != "" || namespace != "" || c.PFService != "" || c.PFRemotePort != 0 {
		if ctx.PortForward == nil {
			ctx.PortForward = &clientconfig.PortForward{}
		}
		if c.PFKubeContext != "" {
			ctx.PortForward.KubeContext = c.PFKubeContext
		}
		if namespace != "" {
			ctx.PortForward.Namespace = namespace
		}
		if c.PFService != "" {
			ctx.PortForward.Service = c.PFService
		}
		if c.PFRemotePort != 0 {
			ctx.PortForward.RemotePort = c.PFRemotePort
		}
		// Auto-detect the in-cluster cornus Service from the namespace when the user
		// did not name one explicitly. Skipped for --server (no port-forward needed),
		// an explicit --pf-service, or --no-detect (store the namespace only).
		if c.Server == "" && c.PFService == "" && !c.NoDetect && namespace != "" {
			res, err := svcforward.Discover(context.Background(), svcforward.DiscoverOptions{
				KubeContext: ctx.PortForward.KubeContext,
				Namespace:   namespace,
			})
			if err != nil {
				return err
			}
			ctx.PortForward.Service = res.Service
			ctx.PortForward.RemotePort = res.RemotePort
			cli.out().Info("detected service %s/%s port %d (%s)", namespace, res.Service, res.RemotePort, res.Managed)
		}
	}
	if c.KubeAuthServiceAccount != "" || c.KubeAuthAudience != "" || c.KubeAuthNamespace != "" || c.KubeAuthKubeContext != "" || c.KubeAuthExpiration != 0 {
		if ctx.KubeAuth == nil {
			ctx.KubeAuth = &clientconfig.KubeAuth{}
		}
		if c.KubeAuthServiceAccount != "" {
			ctx.KubeAuth.ServiceAccount = c.KubeAuthServiceAccount
		}
		if c.KubeAuthAudience != "" {
			ctx.KubeAuth.Audience = c.KubeAuthAudience
		}
		if c.KubeAuthNamespace != "" {
			ctx.KubeAuth.Namespace = c.KubeAuthNamespace
		}
		if c.KubeAuthKubeContext != "" {
			ctx.KubeAuth.KubeContext = c.KubeAuthKubeContext
		}
		if c.KubeAuthExpiration != 0 {
			ctx.KubeAuth.ExpirationSeconds = c.KubeAuthExpiration
		}
	}
	// --via-server / --no-via-server (tri-state): only overwrite when the flag was
	// given, so an unrelated set-context edit leaves the stored value untouched.
	if c.ViaServer != nil {
		ctx.ViaServer = c.ViaServer
	}
	// Conduit mode + SOCKS5 settings: only touch ctx.Conduit when a related flag was
	// given, so an unrelated set-context edit leaves the stored value in place.
	// --conduit-mode may be a bare word or a socks5://host:port[?suffix=…] URL that
	// additionally carries the listen address and service-host suffix into the stored
	// Socks5 block; --socks5-service-host-suffix / --socks5-resolve override on top.
	if c.ConduitMode != "" || c.Socks5ServiceSuffix != "" || len(c.Socks5Resolve) > 0 {
		if ctx.Conduit == nil {
			ctx.Conduit = &clientconfig.Conduit{}
		}
		if c.ConduitMode != "" {
			spec, err := clientconn.ParseConduitSpec(c.ConduitMode)
			if err != nil {
				return err
			}
			// A context describes only the shared proxy. A session-local URL
			// ("socks5://[host:port]") is a per-run choice, not a stored setting.
			if spec.SessionLocalSet && spec.SessionLocal {
				return fmt.Errorf("--conduit-mode %q selects a session-local proxy, which is a per-run choice: pass it as --conduit at run time. To pin the context's shared proxy address, use socks5://.shared:PORT", c.ConduitMode)
			}
			ctx.Conduit.Mode = spec.Mode
			if spec.HasListen || spec.HasSuffix {
				if ctx.Conduit.Socks5 == nil {
					ctx.Conduit.Socks5 = &clientconfig.Socks5{}
				}
				if spec.HasListen {
					ctx.Conduit.Socks5.Listen = spec.Listen
				}
				if spec.HasSuffix {
					ctx.Conduit.Socks5.ServiceHostSuffix = spec.Suffix
				}
			}
		}
		if c.Socks5ServiceSuffix != "" || len(c.Socks5Resolve) > 0 {
			if ctx.Conduit.Socks5 == nil {
				ctx.Conduit.Socks5 = &clientconfig.Socks5{}
			}
			if c.Socks5ServiceSuffix != "" {
				ctx.Conduit.Socks5.ServiceHostSuffix = c.Socks5ServiceSuffix
			}
			if len(c.Socks5Resolve) > 0 {
				rules, err := parseResolveRules(c.Socks5Resolve)
				if err != nil {
					return err
				}
				ctx.Conduit.Socks5.Resolve = rules
			}
		}
	}
	// Ingress-via-conduit: only touched when a related flag is set, so an unrelated
	// edit leaves the stored value in place (mirrors the conduit block above).
	if c.IngressConduit != "" || c.IngressController != "" || c.IngressEmulateCA != "" || c.IngressEmulateCAKey != "" {
		if ctx.Conduit == nil {
			ctx.Conduit = &clientconfig.Conduit{}
		}
		if ctx.Conduit.Ingress == nil {
			ctx.Conduit.Ingress = &clientconfig.Ingress{}
		}
		in := ctx.Conduit.Ingress
		if c.IngressConduit != "" {
			mode, err := normalizeIngressConduitMode(c.IngressConduit)
			if err != nil {
				return err
			}
			in.Mode = mode
		}
		if c.IngressController != "" {
			ctrl, err := parseIngressControllerFlag(c.IngressController)
			if err != nil {
				return err
			}
			in.Controller = ctrl
		}
		if c.IngressEmulateCA != "" {
			in.CAFile = c.IngressEmulateCA
		}
		if c.IngressEmulateCAKey != "" {
			in.CAKeyFile = c.IngressEmulateCAKey
		}
	}
	// --from-file-override: applied last, so file values win over the CLI flags.
	for _, src := range overrideLayers {
		clientconfig.Merge(ctx, src)
	}
	if f.Contexts == nil {
		f.Contexts = map[string]*clientconfig.Context{}
	}
	f.Contexts[c.Name] = ctx

	// Offer to make the very first context the default, so a fresh user does not
	// then have to run `use-context`. Only when interactive (see the var).
	madeDefault := false
	if firstContext && f.CurrentContext == "" && confirmSetDefaultContext(cli.out(), c.Name) {
		f.CurrentContext = c.Name
		madeDefault = true
	}
	if err := clientconfig.Save(path, f); err != nil {
		return err
	}
	if madeDefault {
		cli.out().Done("context %q saved and set as the current context", c.Name)
	} else {
		cli.out().Done("context %q saved", c.Name)
	}
	return nil
}

// ConfigDeleteContextCmd removes a context.
type ConfigDeleteContextCmd struct {
	Name string `kong:"arg,required,help='Context to delete.'"`
}

func (c *ConfigDeleteContextCmd) Run(cli *CLI) error {
	path, err := cli.configPath()
	if err != nil {
		return err
	}
	f, err := clientconfig.Load(path)
	if err != nil {
		return err
	}
	if _, ok := f.Contexts[c.Name]; !ok {
		return fmt.Errorf("context %q not found", c.Name)
	}
	delete(f.Contexts, c.Name)
	if f.CurrentContext == c.Name {
		f.CurrentContext = ""
	}
	if err := clientconfig.Save(path, f); err != nil {
		return err
	}
	cli.out().Done("context %q deleted", c.Name)
	return nil
}

// ConfigViewCmd prints the config file, redacting bearer tokens by default.
// --export instead prints a single context as a bare Context object (no contexts:
// wrapper) that round-trips into `set-context --from-file`; in that mode the token
// is included by default (the point is a reusable export) unless --redact.
type ConfigViewCmd struct {
	ShowTokens bool   `kong:"name='show-tokens',help='Print bearer tokens instead of redacting them (whole-file view).'"`
	Export     bool   `kong:"name='export',help='Print only one context as a bare Context object (no contexts: wrapper), ready to feed back into set-context --from-file. Selects the global --context, or the current context when unset.'"`
	Redact     bool   `kong:"name='redact',help='With --export, replace the bearer token with REDACTED (export includes the real token by default).'"`
	OutputFile string `kong:"name='output-file',short='o',type='path',help='Write to this file (created 0600) instead of stdout.'"`
}

func (c *ConfigViewCmd) Run(cli *CLI) error {
	f, err := cli.loadConfig()
	if err != nil {
		return err
	}
	var data []byte
	if c.Export {
		// One context, bare (no contexts: wrapper), so the output feeds straight into
		// set-context --from-file. Select the global --context, else current-context.
		name, ctx, err := f.Resolve(cli.Context)
		if err != nil {
			return err
		}
		if name == "" {
			return fmt.Errorf("no context selected: pass --context or set a current context")
		}
		if ctx == nil {
			ctx = &clientconfig.Context{}
		}
		if c.Redact && ctx.Token != "" {
			ctx.Token = "REDACTED"
		}
		if data, err = yaml.Marshal(ctx); err != nil {
			return err
		}
	} else {
		if !c.ShowTokens {
			for _, ctx := range f.Contexts {
				// A null-valued context in the file unmarshals to a nil pointer; skip it
				// rather than dereferencing.
				if ctx != nil && ctx.Token != "" {
					ctx.Token = "REDACTED"
				}
			}
		}
		if data, err = yaml.Marshal(f); err != nil {
			return err
		}
	}
	if c.OutputFile != "" {
		// The output may carry a bearer token, so keep it private like clientconfig.Save.
		if err := os.WriteFile(c.OutputFile, data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", c.OutputFile, err)
		}
		if err := os.Chmod(c.OutputFile, 0o600); err != nil {
			return fmt.Errorf("chmod %s: %w", c.OutputFile, err)
		}
		return nil
	}
	fmt.Print(string(data))
	return nil
}
