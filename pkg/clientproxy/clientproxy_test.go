package clientproxy

import (
	"testing"
)

func TestConfigToEnv(t *testing.T) {
	cfg := &ProxyConfig{
		HTTP:    "http://proxy.corp:8080",
		HTTPS:   "http://proxy.corp:8443",
		NoProxy: "localhost,127.0.0.1",
	}
	env := configToEnv(cfg, []string{"*.internal", "10.0.0.0/8"})
	want := map[string]string{
		"HTTP_PROXY":  "http://proxy.corp:8080",
		"http_proxy":  "http://proxy.corp:8080",
		"HTTPS_PROXY": "http://proxy.corp:8443",
		"https_proxy": "http://proxy.corp:8443",
		"NO_PROXY":    "localhost,127.0.0.1,*.internal,10.0.0.0/8",
		"no_proxy":    "localhost,127.0.0.1,*.internal,10.0.0.0/8",
	}
	if len(env) != len(want) {
		t.Fatalf("env has %d entries, want %d: %v", len(env), len(want), env)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%q] = %q, want %q", k, env[k], v)
		}
	}
}

func TestFromEnvReadsBothCasesCapitalWins(t *testing.T) {
	// Start from a clean slate so ambient proxy env cannot leak into the assertions.
	for _, k := range []string{
		"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy",
		"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy",
	} {
		t.Setenv(k, "")
	}
	// Lowercase-only vars are honored (curl/wget convention).
	t.Setenv("http_proxy", "http://lower:8080")
	t.Setenv("no_proxy", "lowerlocal")
	t.Setenv("all_proxy", "socks5h://lower:1080")
	// On conflict the uppercase spelling wins.
	t.Setenv("https_proxy", "http://lower:8443")
	t.Setenv("HTTPS_PROXY", "http://upper:8443")

	cfg := fromEnv()
	if cfg.HTTP != "http://lower:8080" {
		t.Errorf("HTTP = %q, want lowercase honored", cfg.HTTP)
	}
	if cfg.HTTPS != "http://upper:8443" {
		t.Errorf("HTTPS = %q, want uppercase to win the conflict", cfg.HTTPS)
	}
	if cfg.All != "socks5h://lower:1080" {
		t.Errorf("All = %q, want lowercase all_proxy honored (scheme preserved)", cfg.All)
	}
	if cfg.NoProxy != "lowerlocal" {
		t.Errorf("NoProxy = %q, want lowercase no_proxy honored", cfg.NoProxy)
	}
}

func TestConfigToEnvNoFabricatedAllProxy(t *testing.T) {
	// With only HTTP/HTTPS set, ALL_PROXY must NOT be fabricated (it commonly means
	// SOCKS / catch-all and must not be inferred from an HTTP proxy).
	env := configToEnv(&ProxyConfig{HTTP: "http://p:8080", HTTPS: "http://p:8443"}, nil)
	if _, ok := env["ALL_PROXY"]; ok {
		t.Fatalf("ALL_PROXY should not be fabricated, got %q", env["ALL_PROXY"])
	}
}

func TestConfigToEnvSocksSchemePreserved(t *testing.T) {
	// The socks5h vs socks5 distinction is load-bearing and must pass through
	// verbatim in both directions.
	for _, scheme := range []string{"socks5h://p:1080", "socks5://p:1080"} {
		env := configToEnv(&ProxyConfig{All: scheme}, nil)
		if env["ALL_PROXY"] != scheme || env["all_proxy"] != scheme {
			t.Fatalf("ALL_PROXY = %q / %q, want %q", env["ALL_PROXY"], env["all_proxy"], scheme)
		}
	}
}

func TestConfigToEnvEmpty(t *testing.T) {
	// No proxy configured => nothing injected (even with extra NO_PROXY, which is
	// meaningless without a proxy).
	env := configToEnv(&ProxyConfig{}, []string{"*.internal"})
	if len(env) != 0 {
		t.Fatalf("expected empty env, got %v", env)
	}
}

func TestMergeNoProxyDedup(t *testing.T) {
	got := mergeNoProxy("localhost, 127.0.0.1 ,", []string{"127.0.0.1", "*.internal"})
	want := "localhost,127.0.0.1,*.internal"
	if got != want {
		t.Fatalf("mergeNoProxy = %q, want %q", got, want)
	}
}

func TestParseScutilProxy(t *testing.T) {
	out := `<dictionary> {
  HTTPEnable : 1
  HTTPProxy : proxy.corp
  HTTPPort : 8080
  HTTPSEnable : 1
  HTTPSProxy : proxy.corp
  HTTPSPort : 8443
  SOCKSEnable : 1
  SOCKSProxy : socks.corp
  SOCKSPort : 1080
  ExceptionsList : <array> {
    0 : 192.168.0.0/16
    1 : localhost
  }
  FTPPassive : 1
}`
	cfg := parseScutilProxy(out)
	if cfg.HTTP != "proxy.corp:8080" {
		t.Errorf("HTTP = %q", cfg.HTTP)
	}
	if cfg.HTTPS != "proxy.corp:8443" {
		t.Errorf("HTTPS = %q", cfg.HTTPS)
	}
	if cfg.All != "socks5h://socks.corp:1080" {
		t.Errorf("All (SOCKS) = %q, want socks5h://socks.corp:1080", cfg.All)
	}
	if cfg.NoProxy != "192.168.0.0/16,localhost" {
		t.Errorf("NoProxy = %q", cfg.NoProxy)
	}
}

func TestParseScutilProxyDisabled(t *testing.T) {
	out := `<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 0
  SOCKSEnable : 0
}`
	cfg := parseScutilProxy(out)
	if cfg.HTTP != "" || cfg.HTTPS != "" || cfg.All != "" {
		t.Errorf("expected no proxies, got %+v", cfg)
	}
}

func TestParseWinInetProxySingle(t *testing.T) {
	cfg := parseWinInetProxy("proxy.corp:8080", "localhost;*.internal;<local>", true)
	if cfg.HTTP != "proxy.corp:8080" || cfg.HTTPS != "proxy.corp:8080" {
		t.Errorf("single-proxy = %+v", cfg)
	}
	if cfg.NoProxy != "localhost,*.internal" {
		t.Errorf("NoProxy = %q", cfg.NoProxy)
	}
}

func TestParseWinInetProxyPerScheme(t *testing.T) {
	cfg := parseWinInetProxy("http=h1:80;https=h2:443;socks=s1:1080", "", true)
	if cfg.HTTP != "h1:80" || cfg.HTTPS != "h2:443" {
		t.Errorf("per-scheme = %+v", cfg)
	}
	if cfg.All != "socks5h://s1:1080" {
		t.Errorf("socks = %q, want socks5h://s1:1080", cfg.All)
	}
}

func TestParseWinInetProxyDisabled(t *testing.T) {
	cfg := parseWinInetProxy("proxy.corp:8080", "", false)
	if cfg.HTTP != "" || cfg.HTTPS != "" {
		t.Errorf("disabled proxy should be empty, got %+v", cfg)
	}
}
