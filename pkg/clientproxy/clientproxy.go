// Package clientproxy resolves the caller's operating-system proxy configuration
// into the standard proxy environment variables, so client-side egress "env" mode
// can propagate the developer's own proxy settings into a remote container.
//
// Resolution precedence matches every HTTP client's: the process environment
// wins, and only when it is unset do we consult the platform's system settings —
// macOS (scutil), Windows (WinINET/registry), Linux (GNOME/gsettings). Each
// platform probe lives behind a build tag; the pure parsers are here so they are
// unit-testable on any OS.
//
// Both cases of every variable are honored: we READ HTTP_PROXY/http_proxy,
// HTTPS_PROXY/https_proxy, ALL_PROXY/all_proxy and NO_PROXY/no_proxy (the uppercase
// spelling wins on conflict) — httpproxy.FromEnvironment already does this for the
// HTTP/HTTPS/NO_PROXY trio, and we read ALL_PROXY the same way since it does not
// model it — because many Unix tools (curl, wget) only set the lowercase form; and
// we SET both the uppercase and lowercase spelling of each injected variable,
// because apps read one or the other.
//
// SOCKS scheme note: the ALL_PROXY value is carried and emitted VERBATIM. The
// difference between "socks5://" (the client resolves DNS locally, then hands the
// SOCKS server an IP) and "socks5h://" (the SOCKS server resolves the hostname
// remotely) is semantically load-bearing here — a container behind an air-gapped
// egress usually cannot resolve external names, so remote/proxy-side resolution
// ("socks5h") is what makes it work — and client support is inconsistent (curl
// distinguishes them; some clients reject "socks5h://"; Go's x/net/proxy forwards
// the hostname regardless of scheme). We therefore never rewrite a user-provided
// scheme. When we SYNTHESIZE an ALL_PROXY from a platform SOCKS setting (which
// carries no scheme), we choose "socks5h://" so names resolve at the terminus.
package clientproxy

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/net/http/httpproxy"
)

// ProxyConfig is the caller's resolved proxy configuration. All (ALL_PROXY) is
// kept distinct from HTTP/HTTPS because it commonly names a SOCKS proxy whose
// scheme ("socks5" vs "socks5h") must be preserved verbatim (see the package doc).
type ProxyConfig struct {
	HTTP    string // HTTP_PROXY
	HTTPS   string // HTTPS_PROXY
	All     string // ALL_PROXY (may be socks5://, socks5h://, http://, ...) — scheme preserved
	NoProxy string // NO_PROXY
}

func (c *ProxyConfig) empty() bool {
	return c.HTTP == "" && c.HTTPS == "" && c.All == ""
}

// Resolve returns the effective proxy configuration for the caller: the process
// environment if it names any proxy, otherwise the platform's system settings, and
// an empty (all-fields-blank) config when neither is configured.
func Resolve() (*ProxyConfig, error) {
	env := fromEnv()
	if !env.empty() {
		return env, nil
	}
	plat, err := platformProxy()
	if err != nil {
		return nil, err
	}
	if plat != nil && !plat.empty() {
		// Preserve any NO_PROXY the environment set even when the proxies come from
		// the platform.
		if env.NoProxy != "" && plat.NoProxy == "" {
			plat.NoProxy = env.NoProxy
		}
		return plat, nil
	}
	return env, nil
}

// fromEnv reads the standard proxy variables from the process environment.
// HTTP/HTTPS/NO_PROXY go through httpproxy.FromEnvironment, which already reads
// both the uppercase and lowercase spelling of each (uppercase wins). ALL_PROXY is
// read directly (httpproxy does not model it) and kept verbatim so its SOCKS scheme
// (socks5 vs socks5h) is not normalized.
func fromEnv() *ProxyConfig {
	h := httpproxy.FromEnvironment()
	return &ProxyConfig{
		HTTP:    h.HTTPProxy,
		HTTPS:   h.HTTPSProxy,
		All:     firstEnv("ALL_PROXY", "all_proxy"),
		NoProxy: h.NoProxy,
	}
}

// firstEnv returns the first non-empty environment value among keys, so callers
// pass the uppercase spelling first to give it precedence.
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// EnvVars resolves the caller's proxy configuration and renders it as the proxy
// environment variables to inject into a container. Both upper- and lower-case
// spellings are emitted (apps read one or the other). extraNoProxy patterns
// (e.g. destinations a routing rule keeps on the cluster network) are merged into
// NO_PROXY. An empty map means no proxy is configured — nothing to inject.
func EnvVars(extraNoProxy []string) (map[string]string, error) {
	cfg, err := Resolve()
	if err != nil {
		return nil, err
	}
	return configToEnv(cfg, extraNoProxy), nil
}

// configToEnv renders a proxy config as the environment-variable map. Pure and
// order-independent so it is directly testable. ALL_PROXY is emitted only when the
// caller actually has one (never fabricated from the HTTP/HTTPS proxy), and its
// value — including any socks5/socks5h scheme — passes through unchanged.
func configToEnv(cfg *ProxyConfig, extraNoProxy []string) map[string]string {
	out := map[string]string{}
	set := func(upper, lower, val string) {
		if val == "" {
			return
		}
		out[upper] = val
		out[lower] = val
	}
	if cfg.empty() {
		// No proxy configured: nothing to inject. A NO_PROXY without a proxy is
		// meaningless, so we do not emit one (env mode is a no-op).
		return out
	}
	set("HTTP_PROXY", "http_proxy", cfg.HTTP)
	set("HTTPS_PROXY", "https_proxy", cfg.HTTPS)
	set("ALL_PROXY", "all_proxy", cfg.All)
	noProxy := mergeNoProxy(cfg.NoProxy, extraNoProxy)
	set("NO_PROXY", "no_proxy", noProxy)
	return out
}

// mergeNoProxy unions a base NO_PROXY string with extra patterns, de-duplicated
// and stably ordered (base entries first, then new extras in input order).
func mergeNoProxy(base string, extra []string) string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range strings.Split(base, ",") {
		add(s)
	}
	for _, s := range extra {
		add(s)
	}
	return strings.Join(out, ",")
}

// parseScutilProxy parses the output of `scutil --proxy` (macOS) into a proxy
// config. The relevant keys are HTTPEnable/HTTPProxy/HTTPPort,
// HTTPSEnable/HTTPSProxy/HTTPSPort, SOCKSEnable/SOCKSProxy/SOCKSPort, and
// ExceptionsList (a numbered array). A configured SOCKS proxy becomes ALL_PROXY as
// "socks5h://host:port" (remote/proxy-side DNS — see the package doc). Pure so it
// is testable off-macOS.
func parseScutilProxy(out string) *ProxyConfig {
	fields := map[string]string{}
	var exceptions []string
	inExceptions := false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if inExceptions {
			// Array entries look like "0 : 192.168.0.0/16"; the closing "}" ends it.
			if strings.HasPrefix(trimmed, "}") {
				inExceptions = false
				continue
			}
			if i := strings.Index(trimmed, " : "); i >= 0 {
				exceptions = append(exceptions, strings.TrimSpace(trimmed[i+3:]))
			}
			continue
		}
		if strings.HasPrefix(trimmed, "ExceptionsList : <array>") {
			inExceptions = true
			continue
		}
		if i := strings.Index(trimmed, " : "); i >= 0 {
			key := strings.TrimSpace(trimmed[:i])
			val := strings.TrimSpace(trimmed[i+3:])
			fields[key] = val
		}
	}
	cfg := &ProxyConfig{}
	if fields["HTTPEnable"] == "1" && fields["HTTPProxy"] != "" {
		cfg.HTTP = joinHostPort(fields["HTTPProxy"], fields["HTTPPort"])
	}
	if fields["HTTPSEnable"] == "1" && fields["HTTPSProxy"] != "" {
		cfg.HTTPS = joinHostPort(fields["HTTPSProxy"], fields["HTTPSPort"])
	}
	if fields["SOCKSEnable"] == "1" && fields["SOCKSProxy"] != "" {
		cfg.All = socks5hURL(joinHostPort(fields["SOCKSProxy"], fields["SOCKSPort"]))
	}
	if len(exceptions) > 0 {
		cfg.NoProxy = strings.Join(exceptions, ",")
	}
	return cfg
}

// parseWinInetProxy parses the Windows WinINET proxy settings (ProxyEnable,
// ProxyServer, ProxyOverride) into a proxy config. ProxyServer is either a single
// "host:port" (all protocols) or a per-scheme list ("http=h:p;https=h:p;socks=h:p").
// A "socks=" entry becomes ALL_PROXY as "socks5h://host:port". Pure so it is
// testable off-Windows.
func parseWinInetProxy(proxyServer, proxyOverride string, enabled bool) *ProxyConfig {
	cfg := &ProxyConfig{}
	if !enabled || strings.TrimSpace(proxyServer) == "" {
		return cfg
	}
	if strings.Contains(proxyServer, "=") {
		for _, part := range strings.Split(proxyServer, ";") {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) != 2 {
				continue
			}
			val := strings.TrimSpace(kv[1])
			switch strings.ToLower(strings.TrimSpace(kv[0])) {
			case "http":
				cfg.HTTP = val
			case "https":
				cfg.HTTPS = val
			case "socks":
				cfg.All = socks5hURL(val)
			}
		}
	} else {
		p := strings.TrimSpace(proxyServer)
		cfg.HTTP = p
		cfg.HTTPS = p
	}
	if ov := strings.TrimSpace(proxyOverride); ov != "" {
		var parts []string
		for _, e := range strings.Split(ov, ";") {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			// Windows uses "<local>" for the bypass-local-addresses token; drop it.
			if strings.EqualFold(e, "<local>") {
				continue
			}
			parts = append(parts, e)
		}
		cfg.NoProxy = strings.Join(parts, ",")
	}
	return cfg
}

// socks5hURL renders a bare host:port SOCKS endpoint as a socks5h:// URL
// (remote/proxy-side DNS resolution — correct for an air-gapped egress terminus).
// An empty host yields "", and a value that already carries a scheme is returned
// unchanged so a user's deliberate socks5:// vs socks5h:// choice is preserved.
func socks5hURL(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return ""
	}
	if strings.Contains(hostport, "://") {
		return hostport
	}
	return "socks5h://" + hostport
}

func joinHostPort(host, port string) string {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" {
		return ""
	}
	if port == "" || port == "0" {
		return host
	}
	return fmt.Sprintf("%s:%s", host, port)
}
