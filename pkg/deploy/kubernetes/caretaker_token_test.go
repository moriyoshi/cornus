package kubernetes

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"cornus/pkg/caretaker"
)

// decodeCaretakerConfig extracts the caretaker.Config from the env slice the
// helper produces.
func decodeCaretakerConfig(t *testing.T, env []corev1.EnvVar) caretaker.Config {
	t.Helper()
	for _, e := range env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			var cfg caretaker.Config
			if err := json.Unmarshal([]byte(e.Value), &cfg); err != nil {
				t.Fatalf("unmarshal caretaker config: %v", err)
			}
			return cfg
		}
	}
	t.Fatal("CORNUS_CARETAKER_CONFIG not found")
	return caretaker.Config{}
}

func TestCaretakerConfigEnvToken(t *testing.T) {
	serverBound := []struct {
		name string
		cfg  caretaker.Config
	}{
		{"mounts", caretaker.Config{Mounts: []caretaker.MountRole{{Server: "ws://r", Name: "m0"}}}},
		{"hub", caretaker.Config{Hub: &caretaker.HubRole{Server: "ws://r"}}},
	}
	dataPlaneOnly := []struct {
		name string
		cfg  caretaker.Config
	}{
		{"dns", caretaker.Config{DNS: &caretaker.DNSRole{}}},
		{"proxy", caretaker.Config{Proxy: &caretaker.ProxyRole{Mode: "cooperative"}}},
	}

	// With a token configured, server-bound configs carry it; data-plane-only ones
	// (which never dial the server) do not.
	withTok := &Backend{caretakerToken: "sekret"}
	for _, tc := range serverBound {
		if got := decodeCaretakerConfig(t, withTok.caretakerConfigEnv(tc.cfg, "web")); got.Token != "sekret" {
			t.Fatalf("%s: token = %q, want it stamped", tc.name, got.Token)
		}
	}
	for _, tc := range dataPlaneOnly {
		if got := decodeCaretakerConfig(t, withTok.caretakerConfigEnv(tc.cfg, "web")); got.Token != "" {
			t.Fatalf("%s: token = %q, want none (never dials the server)", tc.name, got.Token)
		}
	}

	// With no token configured (auth off), nothing is stamped even on server-bound
	// configs.
	noTok := &Backend{}
	if got := decodeCaretakerConfig(t, noTok.caretakerConfigEnv(serverBound[0].cfg, "web")); got.Token != "" {
		t.Fatalf("token = %q, want none when server has no static token", got.Token)
	}
}

// TestCaretakerConfigEnvSecretRef proves the hardened path: with a secret ref
// configured, a server-bound sidecar sources the token via secretKeyRef and the
// token is NOT embedded in the config JSON (never a pod-spec literal).
func TestCaretakerConfigEnvSecretRef(t *testing.T) {
	// A value is also set to confirm the secret ref takes precedence over embedding.
	b := &Backend{caretakerToken: "embedded", caretakerTokenSecret: "cornus-caretaker", caretakerTokenSecretKey: "token"}

	env := b.caretakerConfigEnv(caretaker.Config{Mounts: []caretaker.MountRole{{Server: "ws://r", Name: "m0"}}}, "web")

	// Config JSON carries no token literal.
	if got := decodeCaretakerConfig(t, env); got.Token != "" {
		t.Fatalf("config Token = %q, want empty (sourced from Secret, not embedded)", got.Token)
	}
	// A CORNUS_TOKEN env sources from the Secret via secretKeyRef.
	var ref *corev1.SecretKeySelector
	for _, e := range env {
		if e.Name == "CORNUS_TOKEN" && e.ValueFrom != nil {
			ref = e.ValueFrom.SecretKeyRef
		}
	}
	if ref == nil {
		t.Fatal("expected a CORNUS_TOKEN env sourced from a secretKeyRef")
	}
	if ref.Name != "cornus-caretaker" || ref.Key != "token" {
		t.Fatalf("secretKeyRef = %s/%s, want cornus-caretaker/token", ref.Name, ref.Key)
	}

	// DNS-only (not server-bound) gets no token env at all.
	dnsEnv := b.caretakerConfigEnv(caretaker.Config{DNS: &caretaker.DNSRole{}}, "web")
	for _, e := range dnsEnv {
		if e.Name == "CORNUS_TOKEN" {
			t.Fatal("dns-only sidecar must not receive a token secretKeyRef")
		}
	}
}

func TestParseSecretRef(t *testing.T) {
	cases := []struct{ in, name, key string }{
		{"", "", ""},
		{"sec", "sec", "token"},
		{"sec/mykey", "sec", "mykey"},
		{" sec ", "sec", "token"},
		{"sec/", "sec", "token"},
	}
	for _, c := range cases {
		n, k := parseSecretRef(c.in)
		if n != c.name || k != c.key {
			t.Fatalf("parseSecretRef(%q) = %q,%q; want %q,%q", c.in, n, k, c.name, c.key)
		}
	}
}
