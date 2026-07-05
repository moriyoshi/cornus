package clientconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	toml "github.com/pelletier/go-toml"
	"sigs.k8s.io/yaml"
)

// ProjectContextNames are the default filenames a per-project context override is
// discovered under, in priority order (the first one present in a directory wins).
// A project drops one of these beside its sources to pin the connection settings
// used while working in that tree; see the CLI's --context-file / --no-context-file
// flags and clientconn's walk-up discovery.
var ProjectContextNames = []string{
	"cornus-context.json",
	"cornus-context.yaml",
	"cornus-context.yml",
	"cornus-context.toml",
}

// LoadContextFile reads a single context definition from a JSON, YAML, or TOML
// file: a bare Context document (the fields that live under one context — server,
// token, tls, port-forward, kube-auth, ssh-tunnel, via-server, conduit,
// registry-host). It backs both `config set-context --from-file` and the
// per-project override file.
//
// Decoding is strict — an unknown key fails loudly — so a typo, or a full config
// document (one with a top-level contexts:/current-context: map), is rejected
// rather than silently decoding to an empty context, steering the user to the
// bare-context shape. JSON/YAML go through sigs.k8s.io/yaml (JSON is a subset of
// YAML, so one path handles both); TOML is parsed with pelletier/go-toml and then
// routed through the same strict JSON path so it honors the identical json: field
// names and unknown-key rejection.
func LoadContextFile(path string) (*Context, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ctx Context
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		tree, err := toml.LoadBytes(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		// Re-encode the parsed TOML as JSON and decode it through the same strict
		// json-tag path as YAML/JSON, so TOML keys map to the identical field names
		// (e.g. registry-host) and an unknown key is rejected the same way.
		j, err := json.Marshal(tree.ToMap())
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := yaml.UnmarshalStrict(j, &ctx); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	default:
		if err := yaml.UnmarshalStrict(data, &ctx); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if err := ctx.validateResolveRules(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &ctx, nil
}

// classifyFields lists the fields set in c, tagging each as security-sensitive or
// not. Sensitive fields can redirect the endpoint, supply or redirect credentials,
// or weaken TLS — the ones a merely-discovered (untrusted) project override must
// not be allowed to set. ViaServer is the only always-safe field (it only picks
// direct-to-pod vs server-proxied workload streaming). Order is stable for
// deterministic messages.
func classifyFields(c *Context) (all, sensitive []string) {
	fields := []struct {
		name string
		set  bool
		sens bool
	}{
		{"server", c.Server != "", true},
		{"registry-host", c.RegistryHost != "", true},
		{"token", c.Token != "", true},
		{"tls", c.TLS != nil, true},
		{"port-forward", c.PortForward != nil, true},
		{"kube-auth", c.KubeAuth != nil, true},
		{"ssh-tunnel", c.SSHTunnel != nil, true},
		{"conduit", c.Conduit != nil, true},
		{"via-server", c.ViaServer != nil, false},
	}
	for _, f := range fields {
		if f.set {
			all = append(all, f.name)
			if f.sens {
				sensitive = append(sensitive, f.name)
			}
		}
	}
	return all, sensitive
}

// FieldNames returns the names of the fields set in c, and the subset that are
// security-sensitive (see classifyFields). Used to build the per-project override's
// visible notice and to decide what an untrusted file may contribute.
func FieldNames(c *Context) (all, sensitive []string) { return classifyFields(c) }

// StripSensitive zeroes every security-sensitive field of c and returns their
// names. It drops the credential/endpoint/TLS fields of an untrusted (auto-
// discovered, not opted-in) project override while keeping the safe ones so a
// planted or cloned-in file cannot silently redirect the connection.
func StripSensitive(c *Context) []string {
	_, sensitive := classifyFields(c)
	c.Server = ""
	c.RegistryHost = ""
	c.Token = ""
	c.TLS = nil
	c.PortForward = nil
	c.KubeAuth = nil
	c.SSHTunnel = nil
	c.Conduit = nil
	return sensitive
}

// SetsEndpoint reports whether c supplies a field that becomes the connection
// endpoint (so a bearer token would be sent there): a server URL, an SSH tunnel, or
// an in-cluster port-forward target.
func (c *Context) SetsEndpoint() bool {
	return c.Server != "" || c.SSHTunnel != nil || c.PortForward != nil
}

// SuppliesCredential reports whether c carries a credential of its own — a static
// token or a kube-auth mint — rather than relying on the selected context's.
func (c *Context) SuppliesCredential() bool {
	return c.Token != "" || c.KubeAuth != nil
}

// validateResolveRules compile-checks any SOCKS5 resolution rules decoded from a
// context file. These bypass the set-context CLI's resolve-file validation, so a
// bad pattern must be caught here (mirroring cmd/cornus's resolvefile add).
func (c *Context) validateResolveRules() error {
	if c.Conduit == nil || c.Conduit.Socks5 == nil {
		return nil
	}
	for _, r := range c.Conduit.Socks5.Resolve {
		if r.Pattern == "" {
			return fmt.Errorf("empty resolve pattern")
		}
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("invalid resolve pattern %q: %w", r.Pattern, err)
		}
	}
	return nil
}

// Merge overlays src onto dst field by field: a non-zero scalar or non-nil
// sub-struct in src overwrites dst, while a zero/nil field in src leaves dst in
// place. This is the "only overwrite fields that were given" semantics shared by
// the set-context CLI-flag block and the per-project override merge, so a file
// layer composes with the flags the same way one edit composes with a stored
// context. Applied in order, later calls win — which is how repeated
// --from-file / --from-file-override paths collapse into one layer.
func Merge(dst, src *Context) {
	if src.Server != "" {
		dst.Server = src.Server
	}
	if src.RegistryHost != "" {
		dst.RegistryHost = src.RegistryHost
	}
	if src.Token != "" {
		dst.Token = src.Token
	}
	if src.TLS != nil {
		if dst.TLS == nil {
			dst.TLS = &TLS{}
		}
		if src.TLS.CACert != "" {
			dst.TLS.CACert = src.TLS.CACert
		}
		if src.TLS.ClientCert != "" {
			dst.TLS.ClientCert = src.TLS.ClientCert
		}
		if src.TLS.ClientKey != "" {
			dst.TLS.ClientKey = src.TLS.ClientKey
		}
		if src.TLS.ServerName != "" {
			dst.TLS.ServerName = src.TLS.ServerName
		}
		// One-way, matching the --insecure-skip-verify flag: a file can only enable it.
		if src.TLS.InsecureSkipVerify {
			dst.TLS.InsecureSkipVerify = true
		}
	}
	if src.PortForward != nil {
		if dst.PortForward == nil {
			dst.PortForward = &PortForward{}
		}
		if src.PortForward.KubeContext != "" {
			dst.PortForward.KubeContext = src.PortForward.KubeContext
		}
		if src.PortForward.Namespace != "" {
			dst.PortForward.Namespace = src.PortForward.Namespace
		}
		if src.PortForward.Service != "" {
			dst.PortForward.Service = src.PortForward.Service
		}
		if src.PortForward.RemotePort != 0 {
			dst.PortForward.RemotePort = src.PortForward.RemotePort
		}
	}
	if src.KubeAuth != nil {
		if dst.KubeAuth == nil {
			dst.KubeAuth = &KubeAuth{}
		}
		if src.KubeAuth.KubeContext != "" {
			dst.KubeAuth.KubeContext = src.KubeAuth.KubeContext
		}
		if src.KubeAuth.Namespace != "" {
			dst.KubeAuth.Namespace = src.KubeAuth.Namespace
		}
		if src.KubeAuth.ServiceAccount != "" {
			dst.KubeAuth.ServiceAccount = src.KubeAuth.ServiceAccount
		}
		if src.KubeAuth.Audience != "" {
			dst.KubeAuth.Audience = src.KubeAuth.Audience
		}
		if src.KubeAuth.ExpirationSeconds != 0 {
			dst.KubeAuth.ExpirationSeconds = src.KubeAuth.ExpirationSeconds
		}
	}
	if src.SSHTunnel != nil {
		if dst.SSHTunnel == nil {
			dst.SSHTunnel = &SSHTunnel{}
		}
		s, d := src.SSHTunnel, dst.SSHTunnel
		if s.Addr != "" {
			d.Addr = s.Addr
		}
		if s.User != "" {
			d.User = s.User
		}
		if s.RemoteAddr != "" {
			d.RemoteAddr = s.RemoteAddr
		}
		if s.IdentityFile != "" {
			d.IdentityFile = s.IdentityFile
		}
		if s.KnownHosts != "" {
			d.KnownHosts = s.KnownHosts
		}
		if s.HostKey != "" {
			d.HostKey = s.HostKey
		}
		// The bool toggles are one-way (a file can only enable them), matching the
		// enabling-only flags in set-context.
		if s.NoAgent {
			d.NoAgent = true
		}
		if s.Insecure {
			d.Insecure = true
		}
		if s.RemoteTLS {
			d.RemoteTLS = true
		}
		if s.NoSSHConfig {
			d.NoSSHConfig = true
		}
		if s.UseSSHBinary {
			d.UseSSHBinary = true
		}
	}
	// ViaServer is a tri-state *bool, so a file may express an explicit false; any
	// non-nil value overrides.
	if src.ViaServer != nil {
		dst.ViaServer = src.ViaServer
	}
	if src.Conduit != nil {
		if dst.Conduit == nil {
			dst.Conduit = &Conduit{}
		}
		if src.Conduit.Mode != "" {
			dst.Conduit.Mode = src.Conduit.Mode
		}
		if src.Conduit.Socks5 != nil {
			if dst.Conduit.Socks5 == nil {
				dst.Conduit.Socks5 = &Socks5{}
			}
			if src.Conduit.Socks5.Listen != "" {
				dst.Conduit.Socks5.Listen = src.Conduit.Socks5.Listen
			}
			if src.Conduit.Socks5.ServiceHostSuffix != "" {
				dst.Conduit.Socks5.ServiceHostSuffix = src.Conduit.Socks5.ServiceHostSuffix
			}
			// Wholesale replace, matching how --socks5-resolve(-file) replaces the list.
			if len(src.Conduit.Socks5.Resolve) > 0 {
				dst.Conduit.Socks5.Resolve = src.Conduit.Socks5.Resolve
			}
		}
	}
}
