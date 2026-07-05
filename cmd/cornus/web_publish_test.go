package main

import (
	"context"
	"testing"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/clientconduit"
	"cornus/pkg/clientconfig"
)

// publishTestConn is a minimal resolved connection: no profile, so ConduitConfig
// reflects only the override, which is exactly the forcing behavior under test.
func publishTestConn(profile *clientconfig.Conduit) *clientconn.Conn {
	return &clientconn.Conn{Endpoint: "http://fake:5000", Config: clientconn.Config{Conduit: profile}}
}

// publishCfg is the common call. The ctx is needed only for ingress resolution,
// which reaches the server solely in native mode (never in these profile-less
// tests).
func publishCfg(t *testing.T, c *WebCmd, cn *clientconn.Conn) clientconduit.Config {
	t.Helper()
	cfg, _, err := c.publishConduitConfig(context.Background(), cn)
	if err != nil {
		t.Fatalf("publishConduitConfig: %v", err)
	}
	return cfg
}

func TestPublishForcesSocks5(t *testing.T) {
	// A port-forward profile (and no --conduit) must still resolve to socks5:
	// --publish-in-conduit forces it.
	c := &WebCmd{Publish: true, PublishPort: 80}
	cfg := publishCfg(t, c, publishTestConn(&clientconfig.Conduit{Mode: clientconduit.ModePortForward}))
	if cfg.Mode != clientconduit.ModeSocks5 {
		t.Fatalf("conduit mode = %q, want socks5 (forced)", cfg.Mode)
	}
	// A browser has one proxy setting, so the UI belongs where the workloads are and
	// never in a private conduit the profile happened to ask for.
	if cfg.Socks5SessionLocal {
		t.Fatal("published UI must not be put in a private conduit, but SessionLocal is set")
	}
}

func TestPublishRejectsPortForwardConduit(t *testing.T) {
	// An explicit contradiction errors before anything is bound.
	c := &WebCmd{Publish: true, PublishPort: 80, Conduit: "port-forward"}
	if _, _, err := c.publishConduitConfig(context.Background(), publishTestConn(nil)); err == nil {
		t.Fatal("--publish-in-conduit --conduit port-forward should error")
	}
}

func TestPublishRejectsFlagsThatBindAPort(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  *WebCmd
	}{
		{"--addr", &WebCmd{Publish: true, PublishPort: 80, Addr: "127.0.0.1:9999"}},
		{"--allow-host", &WebCmd{Publish: true, PublishPort: 80, AllowHost: []string{"example.test"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := tc.cmd.publishConduitConfig(context.Background(), publishTestConn(nil)); err == nil {
				t.Fatalf("%s with --publish-in-conduit should error: it binds no local port", tc.name)
			}
		})
	}
	// The default addr is not "explicit", so it must be accepted.
	c := &WebCmd{Publish: true, PublishPort: 80, Addr: defaultWebAddr}
	publishCfg(t, c, publishTestConn(nil))
}

func TestPublishRejectsBadPort(t *testing.T) {
	for _, port := range []int{0, -1, 65536} {
		c := &WebCmd{Publish: true, PublishPort: port}
		if _, _, err := c.publishConduitConfig(context.Background(), publishTestConn(nil)); err == nil {
			t.Errorf("--publish-port %d should error", port)
		}
	}
}

// Naming an address PINS it: the caller asked for exactly this proxy, so discovery
// must not move them somewhere else. This is the flag's whole contract, and without
// it every pinned publish would silently land wherever something else already ran.
func TestPublishHonoursAPinnedAddress(t *testing.T) {
	c := &WebCmd{Publish: true, PublishPort: 80, Conduit: "socks5://127.0.0.1:11080"}
	cfg := publishCfg(t, c, publishTestConn(nil))
	if cfg.Socks5Listen != "127.0.0.1:11080" {
		t.Errorf("Socks5Listen = %q, want the pinned 127.0.0.1:11080", cfg.Socks5Listen)
	}
}

// A pinned SUFFIX is a pin too — the caller named the namespace their UI must
// answer in, so they must not be moved to a conduit serving a different one.
func TestPublishHonoursAPinnedSuffix(t *testing.T) {
	c := &WebCmd{Publish: true, PublishPort: 80, Conduit: "socks5://?suffix=.demo.internal"}
	cfg := publishCfg(t, c, publishTestConn(nil))
	if cfg.Socks5Suffix != ".demo.internal" {
		t.Errorf("Socks5Suffix = %q, want the pinned .demo.internal", cfg.Socks5Suffix)
	}
}

// The profile's ingress settings must reach the conduit config, so a conduit this
// call CREATES is one a later compose session can share rather than collide with.
func TestPublishCarriesProfileIngress(t *testing.T) {
	c := &WebCmd{Publish: true, PublishPort: 80}
	cfg := publishCfg(t, c, publishTestConn(&clientconfig.Conduit{
		Mode:    clientconduit.ModeSocks5,
		Ingress: &clientconfig.Ingress{Mode: "emulate"},
	}))
	if cfg.Ingress == nil {
		t.Fatal("the profile's ingress settings did not reach the conduit config")
	}
	if cfg.Ingress.Mode != clientconduit.IngressEmulate {
		t.Errorf("ingress mode = %q, want emulate", cfg.Ingress.Mode)
	}
}

// --local-root is validated where it is now used (the BFF config), not shipped to
// another process. An unusable one must still be refused rather than surfacing as a
// missing mount in the UI.
func TestLocalRootsAreValidated(t *testing.T) {
	if _, err := parseLocalRoots([]string{"label=/definitely/not/here/at/all"}); err == nil {
		t.Fatal("an unusable --local-root should be refused")
	}
}

// --allow-non-loopback authorizes the CONDUIT's bind, and must be usable here: this
// command may be the first participant at the address, in which case it binds the
// proxy itself.
//
// It was refused outright on the grounds that publishing binds no local port — true
// while the UI could only be hosted by the agent, and false since it hosts its own.
// The result was a catch-22 with no way through: pinning a non-loopback address was
// rejected by the conduit's own validation without the flag, and by this command
// with it.
func TestPublishAcceptsAllowNonLoopbackForThePinnedBind(t *testing.T) {
	c := &WebCmd{Publish: true, PublishPort: 80, Conduit: "socks5://[::]:10080", AllowNonLoopback: true}
	cfg := publishCfg(t, c, publishTestConn(nil))
	if cfg.Socks5Listen != "[::]:10080" {
		t.Errorf("Socks5Listen = %q, want the pinned [::]:10080", cfg.Socks5Listen)
	}
	if !cfg.Socks5AllowNonLoopback {
		t.Error("the opt-in did not reach the conduit config, so the bind will be refused")
	}
	// And the conduit itself accepts that configuration, which is the half the flag
	// exists for.
	if err := cfg.Validate(); err != nil {
		t.Errorf("the resulting config is still refused: %v", err)
	}
}

// Without the opt-in a pinned non-loopback address must still be refused: the caller
// is asking to create an unauthenticated proxy reachable off-host.
func TestPublishStillRefusesAnUnconsentedNonLoopbackBind(t *testing.T) {
	c := &WebCmd{Publish: true, PublishPort: 80, Conduit: "socks5://[::]:10080"}
	cfg := publishCfg(t, c, publishTestConn(nil))
	if cfg.Socks5AllowNonLoopback {
		t.Fatal("the exposure opt-in was set without the flag")
	}
	if err := cfg.Validate(); err == nil {
		t.Error("a non-loopback bind without --allow-non-loopback must be refused")
	}
}
