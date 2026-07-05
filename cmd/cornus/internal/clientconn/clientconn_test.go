package clientconn

import (
	"testing"

	"cornus/pkg/clientconduit"
	"cornus/pkg/clientconfig"
)

func ptr(b bool) *bool { return &b }

func connWithProfileAndEnv(t *testing.T, conduit *clientconfig.Conduit) *Conn {
	t.Helper()
	env, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	return &Conn{Config: resolveConfig(configFromContext(&clientconfig.Context{Conduit: conduit}), env)}
}

// TestParseConduitSpec covers the bare-word and socks5[h]:// URL grammars, plus
// the parse errors that keep a typo from silently no-op'ing.
func TestParseConduitSpec(t *testing.T) {
	ok := []struct {
		name string
		in   string
		want ConduitSpec
	}{
		{"empty is unset", "", ConduitSpec{}},
		{"whitespace is unset", "  ", ConduitSpec{}},
		{"bare socks5 sets only the mode", "socks5", ConduitSpec{Mode: clientconduit.ModeSocks5}},
		{"bare port-forward", "port-forward", ConduitSpec{Mode: clientconduit.ModePortForward}},
		{"hyphenless folds", "portforward", ConduitSpec{Mode: clientconduit.ModePortForward}},
		{"bare none", "none", ConduitSpec{Mode: clientconduit.ModeNone}},
		{"unknown word kept opaque", "bogus", ConduitSpec{Mode: "bogus"}},
		// A bare word joins the shared proxy (SessionLocalSet stays false -> defer).
		// Any socks5:// URL authority selects a session-local proxy...
		{"url carries listen (session-local)", "socks5://127.0.0.1:1085", ConduitSpec{Mode: clientconduit.ModeSocks5, Listen: "127.0.0.1:1085", HasListen: true, SessionLocal: true, SessionLocalSet: true}},
		{"socks5h is a synonym", "socks5h://127.0.0.1:1085", ConduitSpec{Mode: clientconduit.ModeSocks5, Listen: "127.0.0.1:1085", HasListen: true, SessionLocal: true, SessionLocalSet: true}},
		{"scheme case-insensitive", "SOCKS5://127.0.0.1:1085", ConduitSpec{Mode: clientconduit.ModeSocks5, Listen: "127.0.0.1:1085", HasListen: true, SessionLocal: true, SessionLocalSet: true}},
		{"url carries suffix (session-local)", "socks5://127.0.0.1:1085?suffix=.demo.internal", ConduitSpec{Mode: clientconduit.ModeSocks5, Listen: "127.0.0.1:1085", HasListen: true, Suffix: ".demo.internal", HasSuffix: true, SessionLocal: true, SessionLocalSet: true}},
		{"empty suffix is still present", "socks5://127.0.0.1:1085?suffix=", ConduitSpec{Mode: clientconduit.ModeSocks5, Listen: "127.0.0.1:1085", HasListen: true, HasSuffix: true, SessionLocal: true, SessionLocalSet: true}},
		// ...an empty authority is session-local on an ephemeral port.
		{"empty authority is ephemeral session-local", "socks5://?suffix=.demo.internal", ConduitSpec{Mode: clientconduit.ModeSocks5, Suffix: ".demo.internal", HasSuffix: true, SessionLocal: true, SessionLocalSet: true}},
		{"trailing slash accepted", "socks5://127.0.0.1:1085/", ConduitSpec{Mode: clientconduit.ModeSocks5, Listen: "127.0.0.1:1085", HasListen: true, SessionLocal: true, SessionLocalSet: true}},
		// The .shared sentinel selects the shared proxy explicitly, optionally pinning
		// its bind port; it is never session-local.
		{"shared sentinel", "socks5://.shared", ConduitSpec{Mode: clientconduit.ModeSocks5, SessionLocalSet: true}},
		{"shared sentinel with port", "socks5://.shared:1085", ConduitSpec{Mode: clientconduit.ModeSocks5, Listen: "127.0.0.1:1085", HasListen: true, SessionLocalSet: true}},
		{"shared sentinel with suffix", "socks5://.shared?suffix=.demo.internal", ConduitSpec{Mode: clientconduit.ModeSocks5, Suffix: ".demo.internal", HasSuffix: true, SessionLocalSet: true}},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConduitSpec(tc.in)
			if err != nil {
				t.Fatalf("ParseConduitSpec(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseConduitSpec(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}

	bad := []struct {
		name string
		in   string
	}{
		{"wrong scheme", "http://127.0.0.1:1085"},
		{"unknown query param", "socks5://127.0.0.1:1085?suffx=.x"},
		{"stray path", "socks5://127.0.0.1:1085/foo"},
		{"wildcard bind", "socks5://0.0.0.0:1080"},
		{"wildcard bind v6", "socks5://[::]:1080"},
		{"any-interface port only", "socks5://:1080"},
		{"non-loopback ip", "socks5://192.168.1.5:1080"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseConduitSpec(tc.in); err == nil {
				t.Fatalf("ParseConduitSpec(%q) = nil error, want error", tc.in)
			}
		})
	}
}

// TestConduitConfig covers the layered resolution: profile base, then
// CORNUS_CONDUIT, then the per-command override — each field overridden only by a
// layer that names it. In particular a bare "socks5" override keeps the profile's
// listen/suffix/resolve; only the URL form replaces them.
func TestConduitConfig(t *testing.T) {
	profile := &clientconfig.Conduit{
		Mode: "socks5",
		Socks5: &clientconfig.Socks5{
			Listen:            "127.0.0.1:1080",
			ServiceHostSuffix: ".cornus.internal",
			Resolve:           []clientconfig.ResolveRule{{Pattern: `^(.*):5000$`, Replace: `\1:10000`}},
		},
	}

	t.Run("bare socks5 override keeps profile socks5 settings", func(t *testing.T) {
		t.Setenv("CORNUS_CONDUIT", "")
		cn := connWithProfileAndEnv(t, profile)
		cfg := cn.ConduitConfig("socks5")
		if cfg.Mode != clientconduit.ModeSocks5 {
			t.Errorf("Mode = %q, want socks5", cfg.Mode)
		}
		if cfg.Socks5Listen != "127.0.0.1:1080" {
			t.Errorf("Socks5Listen = %q, want the profile value", cfg.Socks5Listen)
		}
		if cfg.Socks5Suffix != ".cornus.internal" {
			t.Errorf("Socks5Suffix = %q, want the profile value", cfg.Socks5Suffix)
		}
		if len(cfg.Socks5Resolve) != 1 {
			t.Errorf("Socks5Resolve = %v, want the profile rule preserved", cfg.Socks5Resolve)
		}
	})

	t.Run("url override replaces listen and suffix, keeps resolve", func(t *testing.T) {
		t.Setenv("CORNUS_CONDUIT", "")
		cn := connWithProfileAndEnv(t, profile)
		cfg := cn.ConduitConfig("socks5://127.0.0.1:1099?suffix=.demo.internal")
		if cfg.Socks5Listen != "127.0.0.1:1099" {
			t.Errorf("Socks5Listen = %q, want the URL value", cfg.Socks5Listen)
		}
		if cfg.Socks5Suffix != ".demo.internal" {
			t.Errorf("Socks5Suffix = %q, want the URL value", cfg.Socks5Suffix)
		}
		if len(cfg.Socks5Resolve) != 1 {
			t.Errorf("Socks5Resolve = %v, want the profile rule preserved", cfg.Socks5Resolve)
		}
	})

	t.Run("flag url beats env, env beats profile", func(t *testing.T) {
		t.Setenv("CORNUS_CONDUIT", "socks5://127.0.0.1:1088")
		cn := connWithProfileAndEnv(t, profile)
		cfg := cn.ConduitConfig("socks5://127.0.0.1:1099")
		if cfg.Socks5Listen != "127.0.0.1:1099" {
			t.Errorf("Socks5Listen = %q, want the flag value (highest precedence)", cfg.Socks5Listen)
		}
		// The env layer alone: listen from env, suffix still from the profile.
		cfg = cn.ConduitConfig("")
		if cfg.Socks5Listen != "127.0.0.1:1088" {
			t.Errorf("Socks5Listen = %q, want the env value", cfg.Socks5Listen)
		}
		if cfg.Socks5Suffix != ".cornus.internal" {
			t.Errorf("Socks5Suffix = %q, want the profile value", cfg.Socks5Suffix)
		}
	})

	t.Run("bare socks5 override is not session-local", func(t *testing.T) {
		t.Setenv("CORNUS_CONDUIT", "")
		cn := connWithProfileAndEnv(t, profile)
		cfg := cn.ConduitConfig("socks5")
		if cfg.Socks5SessionLocal {
			t.Errorf("bare socks5 should join the shared proxy, got session-local")
		}
	})

	t.Run("url authority selects a session-local proxy", func(t *testing.T) {
		t.Setenv("CORNUS_CONDUIT", "")
		cn := connWithProfileAndEnv(t, profile)
		cfg := cn.ConduitConfig("socks5://127.0.0.1:1099")
		if !cfg.Socks5SessionLocal {
			t.Errorf("socks5://host:port should be session-local")
		}
		if cfg.Socks5Listen != "127.0.0.1:1099" {
			t.Errorf("Socks5Listen = %q, want the pinned URL value", cfg.Socks5Listen)
		}
	})

	t.Run("empty authority is session-local on an ephemeral port", func(t *testing.T) {
		t.Setenv("CORNUS_CONDUIT", "")
		cn := connWithProfileAndEnv(t, profile)
		cfg := cn.ConduitConfig("socks5://")
		if !cfg.Socks5SessionLocal {
			t.Errorf("socks5:// should be session-local")
		}
		if cfg.Socks5Listen != "127.0.0.1:0" {
			t.Errorf("Socks5Listen = %q, want ephemeral 127.0.0.1:0 (not the profile 1080)", cfg.Socks5Listen)
		}
	})

	t.Run(".shared overrides a profile back to the shared proxy", func(t *testing.T) {
		t.Setenv("CORNUS_CONDUIT", "socks5://127.0.0.1:1099") // env would be session-local
		cn := connWithProfileAndEnv(t, profile)
		cfg := cn.ConduitConfig("socks5://.shared") // ...but the flag forces shared
		if cfg.Socks5SessionLocal {
			t.Errorf(".shared should force the shared proxy, got session-local")
		}
		if cfg.Socks5Listen != "" {
			t.Errorf("Socks5Listen = %q, want the shared/default listen (empty)", cfg.Socks5Listen)
		}
	})

	t.Run(".shared with a port pins the shared proxy address", func(t *testing.T) {
		t.Setenv("CORNUS_CONDUIT", "")
		cn := connWithProfileAndEnv(t, profile)
		cfg := cn.ConduitConfig("socks5://.shared:1085")
		if cfg.Socks5SessionLocal {
			t.Errorf(".shared:port should be shared, got session-local")
		}
		if cfg.Socks5Listen != "127.0.0.1:1085" {
			t.Errorf("Socks5Listen = %q, want 127.0.0.1:1085", cfg.Socks5Listen)
		}
	})

	t.Run("no profile, no override defaults to port-forward", func(t *testing.T) {
		t.Setenv("CORNUS_CONDUIT", "")
		cn := &Conn{}
		if cfg := cn.ConduitConfig(""); cfg.Mode != clientconduit.ModePortForward {
			t.Errorf("Mode = %q, want port-forward", cfg.Mode)
		}
	})

	t.Run("malformed override kept opaque for Start to reject", func(t *testing.T) {
		t.Setenv("CORNUS_CONDUIT", "")
		cn := &Conn{}
		if cfg := cn.ConduitConfig("http://x"); cfg.Mode != "http://x" {
			t.Errorf("Mode = %q, want the raw string kept for Start to error on", cfg.Mode)
		}
	})
}

// TestConduitMode covers the precedence: CLI flag > CORNUS_CONDUIT env > profile >
// default (port-forward), plus the hyphenless spelling fold.
func TestConduitMode(t *testing.T) {
	cases := []struct {
		name    string
		cli     string
		env     string
		profile string
		want    string
	}{
		{"all unset defaults to port-forward", "", "", "", clientconduit.ModePortForward},
		{"profile socks5", "", "", "socks5", clientconduit.ModeSocks5},
		{"env beats profile", "", "port-forward", "socks5", clientconduit.ModePortForward},
		{"cli beats env and profile", "socks5", "port-forward", "port-forward", clientconduit.ModeSocks5},
		{"whitespace defers", "  ", "  ", "socks5", clientconduit.ModeSocks5},
		{"hyphenless folds", "", "", "portforward", clientconduit.ModePortForward},
		{"unknown returned as-is", "bogus", "", "", "bogus"},
		{"cli none for --no-forward-ports", clientconduit.ModeNone, "", "socks5", clientconduit.ModeNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := conduitMode(tc.cli, tc.env, tc.profile); got != tc.want {
				t.Fatalf("conduitMode(%q, %q, %q) = %q, want %q", tc.cli, tc.env, tc.profile, got, tc.want)
			}
		})
	}
}

// TestViaServerEnabled covers the precedence: CLI flag > CORNUS_VIA_SERVER env >
// profile field > default (false / direct-to-pod).
func TestViaServerEnabled(t *testing.T) {
	cases := []struct {
		name    string
		cli     *bool
		env     string
		profile *bool
		want    bool
	}{
		{"all unset defaults to direct", nil, "", nil, false},
		{"profile only true", nil, "", ptr(true), true},
		{"profile only false", nil, "", ptr(false), false},
		{"env true beats unset profile", nil, "1", nil, true},
		{"env false beats profile true", nil, "0", ptr(true), false},
		{"env true beats profile false", nil, "true", ptr(false), true},
		{"unrecognized env defers to profile", nil, "maybe", ptr(true), true},
		{"empty env defers to profile", nil, "", ptr(true), true},
		{"cli true wins over env false and profile false", ptr(true), "0", ptr(false), true},
		{"cli false wins over env true and profile true", ptr(false), "1", ptr(true), false},
		{"env yes/on truthy", nil, "on", nil, true},
		{"env off falsy", nil, "off", ptr(true), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := viaServerEnabled(tc.cli, tc.env, tc.profile); got != tc.want {
				t.Fatalf("viaServerEnabled(%v, %q, %v) = %v, want %v", tc.cli, tc.env, tc.profile, got, tc.want)
			}
		})
	}
}

// TestParseBoolish covers the permissive env grammar.
func TestParseBoolish(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", "Yes", "on", "  on  "}
	falsy := []string{"0", "false", "No", "OFF"}
	unset := []string{"", "maybe", "2", "enable"}
	for _, s := range truthy {
		if v, ok := parseBoolish(s); !ok || !v {
			t.Errorf("parseBoolish(%q) = (%v,%v), want (true,true)", s, v, ok)
		}
	}
	for _, s := range falsy {
		if v, ok := parseBoolish(s); !ok || v {
			t.Errorf("parseBoolish(%q) = (%v,%v), want (false,true)", s, v, ok)
		}
	}
	for _, s := range unset {
		if _, ok := parseBoolish(s); ok {
			t.Errorf("parseBoolish(%q) ok = true, want false (defer)", s)
		}
	}
}
