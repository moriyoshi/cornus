// Package clientconfig is the CLI-side connection configuration for talking to a
// remote cornus server: a kubeconfig-style file of named contexts, each holding an
// endpoint, credentials, TLS material, and an optional in-cluster port-forward
// target. It is deliberately separate from the server-side pkg/config (which
// describes a running server's data directory and listener) — this one lives on a
// developer's machine and is never read by the server.
package clientconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

// File is the on-disk client configuration: a set of named contexts plus the one
// currently selected. It maps to a kubeconfig-like YAML document.
type File struct {
	// CurrentContext is the context used when no --context flag is given. Empty
	// means "no context selected"; the CLI then relies on per-command flags/env.
	CurrentContext string `json:"current-context,omitempty"`
	// Contexts is the named connection profiles, keyed by name.
	Contexts map[string]*Context `json:"contexts,omitempty"`
}

// Context is one named remote endpoint with the credentials and transport settings
// needed to reach it.
type Context struct {
	// Server is the cornus server base URL (e.g. https://cornus.example.com or
	// http://127.0.0.1:5000). When PortForward is set and Server is empty, the CLI
	// forwards to the in-cluster Service and dials the local end instead.
	Server string `json:"server,omitempty"`
	// RegistryHost overrides the "host[:port]" that built images are tagged with and
	// that deploy pull refs carry. Empty (the usual case) means derive it: the CLI
	// asks the server (GET /.cornus/v1/info) for the address its deploy targets can pull
	// from, falling back to the Server endpoint's host. Set this only for topologies
	// the server cannot introspect, or to force a specific registry name.
	RegistryHost string `json:"registry-host,omitempty"`
	// Token is the bearer token / JWT sent as "Authorization: Bearer". Empty falls
	// back to the CORNUS_TOKEN environment variable.
	Token string `json:"token,omitempty"`
	// TLS carries optional custom-CA / mTLS / insecure settings for HTTPS endpoints.
	TLS *TLS `json:"tls,omitempty"`
	// PortForward, when set, describes an in-cluster Service the CLI port-forwards
	// to before dialing (consumed by pkg/svcforward).
	PortForward *PortForward `json:"port-forward,omitempty"`
	// KubeAuth, when set, derives the bearer token from the cluster: the CLI mints a
	// short-lived audience-scoped ServiceAccount token via the Kubernetes
	// TokenRequest API instead of using a static Token. It takes precedence over
	// Token but yields to an explicit CORNUS_TOKEN env override.
	KubeAuth *KubeAuth `json:"kube-auth,omitempty"`
	// ViaServer, when non-nil, forces workload streaming operations (compose logs,
	// port-forward) to route through the cornus server proxy instead of the CLI
	// reaching the workload pods directly with the developer's kubeconfig. It only
	// matters for a cluster profile (PortForward/KubeAuth set), where the direct
	// path is otherwise preferred. A tri-state pointer so an explicit false can
	// persist; nil means "unset" (defer to the default: direct). It is the
	// lowest-precedence layer of the toggle, below the CORNUS_VIA_SERVER env var and
	// the per-command --via-server flag. Transport-only: it does not disable
	// KubeAuth token minting.
	ViaServer *bool `json:"via-server,omitempty"`
	// Conduit selects how a client session (deploy --server, compose up) exposes a
	// deployment's ports to the caller: per-port automatic forwarding (the default,
	// Compose-like) or a single client-side SOCKS5 split-tunnel proxy. nil means the
	// default (port-forward). It is the lowest-precedence layer, below the
	// CORNUS_CONDUIT env var and a per-command --conduit flag.
	Conduit *Conduit `json:"conduit,omitempty"`
	// SSHTunnel, when set and Server is empty, reaches the cornus server through an
	// SSH tunnel: the CLI establishes an SSH connection to the remote host (honoring
	// ~/.ssh/config unless disabled) and routes every request to the server over it.
	// It is the docker/containerd-host analogue of PortForward and is mutually
	// exclusive with it. Consumed by cmd/cornus/internal/clientconn + pkg/sshclient.
	SSHTunnel *SSHTunnel `json:"ssh-tunnel,omitempty"`
}

// SSHTunnel describes an SSH connection through which the CLI reaches a cornus
// server running on a remote docker/containerd host. The tunnel is transparent:
// once configured, ordinary commands (deploy, compose, exec, ...) route through it
// with no per-command flags. See pkg/sshclient for the dialer and pkg/clientconn
// for how it is resolved into a connection.
type SSHTunnel struct {
	// Addr is the ssh destination: an ssh_config Host alias (resolved through
	// ~/.ssh/config and /etc/ssh/ssh_config) or a literal host[:port].
	Addr string `json:"addr,omitempty"`
	// User is the SSH login user; empty defers to ssh_config, then the current user.
	User string `json:"user,omitempty"`
	// RemoteAddr is the address the remote cornus server listens on, from the remote
	// host's point of view (default 127.0.0.1:5000). It becomes the endpoint URL host
	// dialed through the tunnel.
	RemoteAddr string `json:"remote-addr,omitempty"`
	// IdentityFile is an explicit PEM private key on disk for public-key auth. Empty
	// falls back to the local ssh-agent (unless NoAgent) and ssh_config's IdentityFile.
	IdentityFile string `json:"identity-file,omitempty"`
	// NoAgent opts out of the default local $SSH_AUTH_SOCK authentication (mainly for
	// the OpenSSH "too many authentication failures" case).
	NoAgent bool `json:"no-agent,omitempty"`
	// KnownHosts is a known_hosts file for host-key verification. Empty defers to
	// ssh_config's UserKnownHostsFile, then the built-in host-key policy.
	KnownHosts string `json:"known-hosts,omitempty"`
	// HostKey pins a single expected host key as an authorized_keys-format line.
	HostKey string `json:"host-key,omitempty"`
	// Insecure disables host-key verification (dev only).
	Insecure bool `json:"insecure-host-key,omitempty"`
	// RemoteTLS dials the tunneled endpoint over https:// because the remote cornus
	// terminates TLS itself (an SSH local-forward carries raw bytes transparently, so
	// TLS composes end-to-end — unlike a Kubernetes port-forward). Usually paired with
	// TLS.ServerName so verification matches the server's certificate, not 127.0.0.1.
	RemoteTLS bool `json:"remote-tls,omitempty"`
	// NoSSHConfig skips consulting ~/.ssh/config and /etc/ssh/ssh_config; only the
	// explicit fields above are used.
	NoSSHConfig bool `json:"no-ssh-config,omitempty"`
	// UseSSHBinary forces the ssh-binary fallback transport (a persistent
	// `ssh -N -L <unixsock>:<remote>` dialed as a unix socket), which honors the full
	// ssh_config including Match and ProxyCommand. It is auto-selected when the
	// resolved host has a ProxyCommand.
	UseSSHBinary bool `json:"use-ssh-binary,omitempty"`
}

// Conduit is a context's session conduit preference: the mode plus, for SOCKS5, its
// proxy settings.
type Conduit struct {
	// Mode is "" / "port-forward" (the default) or "socks5".
	Mode string `json:"mode,omitempty"`
	// Socks5 tunes the SOCKS5 proxy; consulted only when Mode == "socks5".
	Socks5 *Socks5 `json:"socks5,omitempty"`
	// Ingress, when set, reaches a workload's declared ingress host(s) through the
	// SOCKS5 conduit. It is opt-in (nil means off) and requires the socks5 conduit
	// mode. See Ingress.
	Ingress *Ingress `json:"ingress,omitempty"`
}

// Clone returns a deep copy of c.
func (c *Conduit) Clone() *Conduit {
	if c == nil {
		return nil
	}
	return &Conduit{
		Mode:    c.Mode,
		Socks5:  c.Socks5.Clone(),
		Ingress: c.Ingress.Clone(),
	}
}

// Merge returns a deep copy of c overlaid with the non-zero fields of override.
// Neither input is mutated. Nested blocks merge field-by-field.
func (c *Conduit) Merge(override *Conduit) *Conduit {
	if override == nil {
		return c.Clone()
	}
	result := c.Clone()
	if result == nil {
		result = &Conduit{}
	}
	if override.Mode != "" {
		result.Mode = override.Mode
	}
	if override.Socks5 != nil {
		result.Socks5 = result.Socks5.Merge(override.Socks5)
	}
	if override.Ingress != nil {
		result.Ingress = result.Ingress.Merge(override.Ingress)
	}
	return result
}

// Ingress opts a context into reaching a workload's ingress host(s) through the
// SOCKS5 conduit, in one of two modes (see Mode). It is a conduit concern because it
// rides the same name-resolving SOCKS5 proxy the workload ports do.
type Ingress struct {
	// Mode is "native" (transparent tunnel to the real cluster ingress controller,
	// which does Host/path routing and terminates TLS) or "emulate" (a client-side
	// HTTP(S) reverse proxy that routes to the workload and terminates TLS with a
	// generated cert). Empty means off.
	Mode string `json:"mode,omitempty"`
	// Controller (native mode) names the ingress controller Service to tunnel to.
	// Empty asks the client to learn it from the server's GET /.cornus/v1/info.
	Controller *IngressController `json:"controller,omitempty"`
	// CAFile / CAKeyFile (emulate mode) are PEM paths to a CA that signs the per-host
	// leaf certificates the emulated ingress serves. Empty uses a generated,
	// persisted session CA (see pkg/ingressemu).
	CAFile    string `json:"ca-file,omitempty"`
	CAKeyFile string `json:"ca-key-file,omitempty"`
	// Certificates are ordered server-certificate rules shared by emulated and
	// native ingress. Pattern is optional; when empty it is derived from the
	// certificate's DNS SANs.
	Certificates []IngressCertificate `json:"certificates,omitempty"`
}

// IngressCertificate selects a user-provided TLS certificate for an ingress
// hostname. Certificate and Key are PEM file paths. Pattern may be an
// exact DNS name or a supported wildcard; empty derives patterns from DNS SANs.
type IngressCertificate struct {
	Pattern     string `json:"pattern,omitempty"`
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

// Clone returns a deep copy of i.
func (i *Ingress) Clone() *Ingress {
	if i == nil {
		return nil
	}
	return &Ingress{
		Mode:         i.Mode,
		Controller:   i.Controller.Clone(),
		CAFile:       i.CAFile,
		CAKeyFile:    i.CAKeyFile,
		Certificates: append([]IngressCertificate(nil), i.Certificates...),
	}
}

// Merge returns a deep copy of i overlaid with the non-zero fields of override.
func (i *Ingress) Merge(override *Ingress) *Ingress {
	if override == nil {
		return i.Clone()
	}
	result := i.Clone()
	if result == nil {
		result = &Ingress{}
	}
	if override.Mode != "" {
		result.Mode = override.Mode
	}
	if override.Controller != nil {
		result.Controller = result.Controller.Merge(override.Controller)
	}
	if override.CAFile != "" {
		result.CAFile = override.CAFile
	}
	if override.CAKeyFile != "" {
		result.CAKeyFile = override.CAKeyFile
	}
	if len(override.Certificates) != 0 {
		result.Certificates = append([]IngressCertificate(nil), override.Certificates...)
	}
	return result
}

// IngressController is a native-passthrough target: the in-cluster ingress
// controller Service the client port-forwards to (with the developer's kubeconfig)
// so the real controller receives the browser's SNI/Host and routes accordingly.
type IngressController struct {
	// KubeContext selects a kubeconfig context; empty uses the profile cluster context.
	KubeContext string `json:"kube-context,omitempty"`
	// Namespace and Service name the controller Service (e.g. ingress-nginx /
	// ingress-nginx-controller).
	Namespace string `json:"namespace,omitempty"`
	Service   string `json:"service,omitempty"`
	// HTTPPort / HTTPSPort are the controller Service ports; zero defaults to 80 / 443.
	HTTPPort  int `json:"http-port,omitempty"`
	HTTPSPort int `json:"https-port,omitempty"`
}

// Clone returns a copy of c.
func (c *IngressController) Clone() *IngressController {
	if c == nil {
		return nil
	}
	cc := *c
	return &cc
}

// Merge returns a copy of c overlaid with the non-zero fields of override.
func (c *IngressController) Merge(override *IngressController) *IngressController {
	if override == nil {
		return c.Clone()
	}
	result := c.Clone()
	if result == nil {
		result = &IngressController{}
	}
	if override.KubeContext != "" {
		result.KubeContext = override.KubeContext
	}
	if override.Namespace != "" {
		result.Namespace = override.Namespace
	}
	if override.Service != "" {
		result.Service = override.Service
	}
	if override.HTTPPort != 0 {
		result.HTTPPort = override.HTTPPort
	}
	if override.HTTPSPort != 0 {
		result.HTTPSPort = override.HTTPSPort
	}
	return result
}

// Socks5 configures the SOCKS5 split-tunnel proxy (see pkg/socks5).
type Socks5 struct {
	// Listen is the local address the proxy binds (default 127.0.0.1:1080).
	Listen string `json:"listen,omitempty"`
	// ServiceHostSuffix builds the everyday default resolution rule: a CONNECT host
	// bearing this suffix is stripped to a service name and tunneled in, everything
	// else egresses directly (default .cornus.internal). Ignored when Resolve is set.
	ServiceHostSuffix string `json:"service-host-suffix,omitempty"`
	// Resolve is an advanced, ordered list of resolution rules that replaces the
	// suffix default entirely; the first matching rule wins.
	Resolve []ResolveRule `json:"resolve,omitempty"`
	// BareServiceNames toggles whether a bare, single-label host that names a live
	// service (e.g. the compose service name "web", in addition to
	// "web.cornus.internal") is routed inward. A tri-state pointer: nil keeps the
	// default (enabled); set false to disable it when a service name would shadow a
	// real single-label host reached directly.
	BareServiceNames *bool `json:"bare-service-names,omitempty"`
}

// Clone returns a deep copy of s.
func (s *Socks5) Clone() *Socks5 {
	if s == nil {
		return nil
	}
	var bareServiceNames *bool
	if s.BareServiceNames != nil {
		value := *s.BareServiceNames
		bareServiceNames = &value
	}
	return &Socks5{
		Listen:            s.Listen,
		ServiceHostSuffix: s.ServiceHostSuffix,
		Resolve:           append([]ResolveRule(nil), s.Resolve...),
		BareServiceNames:  bareServiceNames,
	}
}

// Merge returns a deep copy of s overlaid with the non-zero fields of override.
// Resolve is replaced wholesale because its order is significant.
func (s *Socks5) Merge(override *Socks5) *Socks5 {
	if override == nil {
		return s.Clone()
	}
	result := s.Clone()
	if result == nil {
		result = &Socks5{}
	}
	if override.Listen != "" {
		result.Listen = override.Listen
	}
	if override.ServiceHostSuffix != "" {
		result.ServiceHostSuffix = override.ServiceHostSuffix
	}
	if len(override.Resolve) > 0 {
		result.Resolve = append([]ResolveRule(nil), override.Resolve...)
	}
	if override.BareServiceNames != nil {
		value := *override.BareServiceNames
		result.BareServiceNames = &value
	}
	return result
}

// ResolveRule is one SOCKS5 resolution rule: a regexp Pattern tested against the
// "host:port" CONNECT subject and a Replace template yielding "service:port"
// (sed-style \1 backreferences accepted). See pkg/socks5.
type ResolveRule struct {
	Pattern string `json:"pattern"`
	Replace string `json:"replace"`
}

// TLS holds the client-side TLS material for an HTTPS endpoint.
type TLS struct {
	// CACert is a path to a PEM CA bundle that verifies the server certificate, for
	// a server whose CA is not in the system trust store.
	CACert string `json:"ca-cert,omitempty"`
	// InsecureSkipVerify disables server certificate verification. Testing only.
	InsecureSkipVerify bool `json:"insecure-skip-verify,omitempty"`
	// ClientCert / ClientKey are paths to a PEM client certificate/key for mTLS.
	ClientCert string `json:"client-cert,omitempty"`
	ClientKey  string `json:"client-key,omitempty"`
	// ServerName overrides the SNI / certificate hostname used to verify the server,
	// for when the dial address differs from the certificate's identity. It is
	// required for an SSH-tunnel profile with remote-tls: the endpoint is dialed as
	// 127.0.0.1:<port> through the tunnel, so without this Go would verify the cert
	// against "127.0.0.1" instead of the server's real hostname.
	ServerName string `json:"server-name,omitempty"`
}

// Config builds a *tls.Config from the profile's TLS settings, loading the CA
// bundle and client certificate/key from disk. It returns (nil, nil) when no TLS
// customization is configured, so callers can fall back to the system defaults.
// A client cert requires its key and vice versa.
func (t *TLS) Config() (*tls.Config, error) {
	if t == nil || (t.CACert == "" && t.ClientCert == "" && t.ClientKey == "" && !t.InsecureSkipVerify && t.ServerName == "") {
		return nil, nil
	}
	cfg := &tls.Config{InsecureSkipVerify: t.InsecureSkipVerify, ServerName: t.ServerName} //nolint:gosec // opt-in via config
	if t.CACert != "" {
		pem, err := os.ReadFile(t.CACert)
		if err != nil {
			return nil, fmt.Errorf("read ca-cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca-cert %s: no valid certificates", t.CACert)
		}
		cfg.RootCAs = pool
	}
	if (t.ClientCert == "") != (t.ClientKey == "") {
		return nil, fmt.Errorf("tls: client-cert and client-key must be set together")
	}
	if t.ClientCert != "" {
		cert, err := tls.LoadX509KeyPair(t.ClientCert, t.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// PortForward describes an in-cluster Service to forward to. RemotePort is the
// Service port; the CLI resolves it to a ready backing pod and its target port.
type PortForward struct {
	KubeContext string `json:"kube-context,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Service     string `json:"service,omitempty"`
	RemotePort  int    `json:"remote-port,omitempty"`
}

// KubeAuth describes a cluster-issued ServiceAccount token to mint as the cornus
// bearer credential. KubeContext and Namespace default to the PortForward block's
// when empty. Audience must match the server's CORNUS_JWT_AUDIENCE.
type KubeAuth struct {
	KubeContext       string `json:"kube-context,omitempty"`
	Namespace         string `json:"namespace,omitempty"`
	ServiceAccount    string `json:"service-account,omitempty"`
	Audience          string `json:"audience,omitempty"`
	ExpirationSeconds int64  `json:"expiration-seconds,omitempty"`
}

// DefaultPath returns the platform-native path to the client config file. It
// honors an explicitly set $XDG_CONFIG_HOME on every OS (an opt-in for users who
// standardize on XDG), and otherwise uses the OS user config directory:
// ~/.config on Linux/BSD, ~/Library/Application Support on macOS, and %AppData%
// on Windows. The --config flag / CORNUS_CONFIG env override this entirely and
// are applied by the caller.
func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "cornus", "config.yaml"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(dir, "cornus", "config.yaml"), nil
}

// Load reads and parses the config file at path. A missing file is not an error:
// it returns an empty (but non-nil) File so callers can treat "no config yet" the
// same as "config with no contexts".
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &File{Contexts: map[string]*Context{}}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Contexts == nil {
		f.Contexts = map[string]*Context{}
	}
	return &f, nil
}

// Save writes f to path, creating the parent directory if needed. The file holds
// bearer tokens and key paths, so it is written 0600 under a 0700 directory.
func Save(path string, f *File) error {
	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create config dir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// os.WriteFile only applies the 0600 mode when it creates the file; a
	// pre-existing config (e.g. left 0644 by an editor or an older code path)
	// keeps its looser mode after truncation, which would leave the stored
	// bearer token world-readable. Enforce 0600 explicitly.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// Resolve selects the context to use: the given name, or CurrentContext when name
// is empty. It returns the resolved name and its context. When neither a name nor
// a current context is set it returns ("", nil, nil) — a legitimate "no profile
// selected" state where the caller falls back to per-command flags and env. A
// name (explicit or current) that does not exist is an error.
func (f *File) Resolve(name string) (string, *Context, error) {
	if name == "" {
		name = f.CurrentContext
	}
	if name == "" {
		return "", nil, nil
	}
	ctx, ok := f.Contexts[name]
	if !ok {
		return "", nil, fmt.Errorf("context %q not found in config", name)
	}
	return name, ctx, nil
}
