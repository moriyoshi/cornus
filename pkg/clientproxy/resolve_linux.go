//go:build linux

package clientproxy

import (
	"os/exec"
	"strings"
)

// gsettingsGet reads one GNOME proxy key; a field so tests can stub the probe.
var gsettingsGet = func(schema, key string) (string, bool) {
	out, err := exec.Command("gsettings", "get", schema, key).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// platformProxy consults the GNOME/gsettings proxy configuration on Linux (only
// when the environment named no proxy — Resolve gates that). Non-GNOME desktops
// and headless hosts simply have no gsettings, so this returns an empty config and
// the environment stays authoritative.
func platformProxy() (*ProxyConfig, error) {
	mode, ok := gsettingsGet("org.gnome.system.proxy", "mode")
	if !ok || unquoteGSettings(mode) != "manual" {
		return &ProxyConfig{}, nil
	}
	cfg := &ProxyConfig{}
	if host, ok := gsettingsGet("org.gnome.system.proxy.http", "host"); ok {
		if h := unquoteGSettings(host); h != "" {
			port, _ := gsettingsGet("org.gnome.system.proxy.http", "port")
			cfg.HTTP = joinHostPort(h, strings.TrimSpace(port))
		}
	}
	if host, ok := gsettingsGet("org.gnome.system.proxy.https", "host"); ok {
		if h := unquoteGSettings(host); h != "" {
			port, _ := gsettingsGet("org.gnome.system.proxy.https", "port")
			cfg.HTTPS = joinHostPort(h, strings.TrimSpace(port))
		}
	}
	if host, ok := gsettingsGet("org.gnome.system.proxy.socks", "host"); ok {
		if h := unquoteGSettings(host); h != "" {
			port, _ := gsettingsGet("org.gnome.system.proxy.socks", "port")
			cfg.All = socks5hURL(joinHostPort(h, strings.TrimSpace(port)))
		}
	}
	if ignore, ok := gsettingsGet("org.gnome.system.proxy", "ignore-hosts"); ok {
		cfg.NoProxy = parseGSettingsList(ignore)
	}
	return cfg, nil
}

// unquoteGSettings strips the surrounding single quotes gsettings prints for
// string values ("'proxy.corp'").
func unquoteGSettings(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "'")
	v = strings.TrimSuffix(v, "'")
	return v
}

// parseGSettingsList turns a gsettings string array ("['localhost', '10.0.0.0/8']")
// into a comma-separated NO_PROXY value.
func parseGSettingsList(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "[")
	v = strings.TrimSuffix(v, "]")
	var parts []string
	for _, e := range strings.Split(v, ",") {
		e = unquoteGSettings(e)
		if e != "" {
			parts = append(parts, e)
		}
	}
	return strings.Join(parts, ",")
}
