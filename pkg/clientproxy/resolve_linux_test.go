//go:build linux

package clientproxy

import "testing"

func TestPlatformProxyGSettings(t *testing.T) {
	values := map[string]string{
		"org.gnome.system.proxy mode":         "'manual'",
		"org.gnome.system.proxy.http host":    "'proxy.corp'",
		"org.gnome.system.proxy.http port":    "8080",
		"org.gnome.system.proxy.https host":   "'proxy.corp'",
		"org.gnome.system.proxy.https port":   "8443",
		"org.gnome.system.proxy.socks host":   "'socks.corp'",
		"org.gnome.system.proxy.socks port":   "1080",
		"org.gnome.system.proxy ignore-hosts": "['localhost', '10.0.0.0/8']",
	}
	orig := gsettingsGet
	t.Cleanup(func() { gsettingsGet = orig })
	gsettingsGet = func(schema, key string) (string, bool) {
		v, ok := values[schema+" "+key]
		return v, ok
	}
	cfg, err := platformProxy()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP != "proxy.corp:8080" || cfg.HTTPS != "proxy.corp:8443" {
		t.Fatalf("gsettings proxy = %+v", cfg)
	}
	if cfg.All != "socks5h://socks.corp:1080" {
		t.Fatalf("gsettings socks = %q, want socks5h://socks.corp:1080", cfg.All)
	}
	if cfg.NoProxy != "localhost,10.0.0.0/8" {
		t.Fatalf("gsettings NoProxy = %q", cfg.NoProxy)
	}
}

func TestPlatformProxyGSettingsModeNone(t *testing.T) {
	orig := gsettingsGet
	t.Cleanup(func() { gsettingsGet = orig })
	gsettingsGet = func(schema, key string) (string, bool) {
		if schema == "org.gnome.system.proxy" && key == "mode" {
			return "'none'", true
		}
		return "", false
	}
	cfg, err := platformProxy()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP != "" || cfg.HTTPS != "" || cfg.All != "" {
		t.Fatalf("mode=none should yield no proxy, got %+v", cfg)
	}
}

func TestResolveEnvWinsOverPlatform(t *testing.T) {
	orig := gsettingsGet
	t.Cleanup(func() { gsettingsGet = orig })
	gsettingsGet = func(schema, key string) (string, bool) {
		if schema == "org.gnome.system.proxy" && key == "mode" {
			return "'manual'", true
		}
		if key == "host" {
			return "'system.proxy'", true
		}
		return "", false
	}
	t.Setenv("HTTP_PROXY", "http://env.proxy:3128")
	t.Setenv("HTTPS_PROXY", "")
	cfg, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP != "http://env.proxy:3128" {
		t.Fatalf("env should win, got %+v", cfg)
	}
}

func TestResolveEnvAllProxyPreserved(t *testing.T) {
	// A user's ALL_PROXY (SOCKS) is read from the environment and preserved verbatim,
	// scheme and all — even when no HTTP/HTTPS proxy is set.
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "socks5h://env.socks:1080")
	cfg, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.All != "socks5h://env.socks:1080" {
		t.Fatalf("ALL_PROXY = %q, want socks5h://env.socks:1080", cfg.All)
	}
}
