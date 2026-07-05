package sshclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withConfig points the ssh_config resolver at a fixture written from body and
// restores the previous finder afterward.
func withConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	prev := configFinder
	configFinder = func() string { return path }
	t.Cleanup(func() { configFinder = prev })
}

func TestResolveFromConfig(t *testing.T) {
	withConfig(t, `
Host devbox
  HostName 10.0.0.5
  User ops
  Port 2222
  StrictHostKeyChecking no
`)
	opts, err := Resolve("devbox", Options{NoAgent: true}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.Addr != "10.0.0.5:2222" {
		t.Errorf("Addr = %q, want 10.0.0.5:2222", opts.Addr)
	}
	if opts.User != "ops" {
		t.Errorf("User = %q, want ops", opts.User)
	}
	if !opts.Insecure {
		t.Errorf("StrictHostKeyChecking no should map to Insecure")
	}
}

func TestResolveProfileOverridesConfig(t *testing.T) {
	withConfig(t, `
Host devbox
  HostName 10.0.0.5
  User ops
`)
	opts, err := Resolve("devbox", Options{User: "root", NoAgent: true}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.User != "root" {
		t.Errorf("explicit profile User should win: got %q", opts.User)
	}
}

func TestResolveNoConfig(t *testing.T) {
	// useConfig=false ignores ssh_config entirely.
	withConfig(t, "Host h\n  HostName should-not-be-used\n")
	opts, err := Resolve("h:2200", Options{NoAgent: true, Insecure: true}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.Addr != "h:2200" {
		t.Errorf("Addr = %q, want h:2200 (literal, config ignored)", opts.Addr)
	}
}

func TestResolveProxyCommandDetected(t *testing.T) {
	withConfig(t, `
Host viacmd
  HostName internal
  ProxyCommand corkscrew proxy 8080 %h %p
`)
	opts, err := Resolve("viacmd", Options{NoAgent: true}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if opts.ProxyCommand == "" {
		t.Fatal("ProxyCommand not detected")
	}
	// %h/%p are expanded.
	if want := "corkscrew proxy 8080 internal 22"; opts.ProxyCommand != want {
		t.Errorf("ProxyCommand = %q, want %q", opts.ProxyCommand, want)
	}
}

func TestResolveProxyJumpParsed(t *testing.T) {
	withConfig(t, `
Host target
  HostName 10.0.0.9
  ProxyJump bastion
Host bastion
  HostName jump.example
  Port 2222
  User jumper
  StrictHostKeyChecking no
`)
	opts, err := Resolve("target", Options{NoAgent: true}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(opts.ProxyJump) != 1 {
		t.Fatalf("ProxyJump len = %d, want 1", len(opts.ProxyJump))
	}
	h := opts.ProxyJump[0]
	if h.Addr != "jump.example:2222" || h.User != "jumper" || !h.Insecure {
		t.Errorf("jump hop = %+v", h)
	}
}

// TestDialerProxyJumpRoundTrip proves the pure-Go hop-chaining dials through a
// two-hop chain (jump -> target -> echo).
func TestDialerProxyJumpRoundTrip(t *testing.T) {
	noAgentEnv(t)
	_, clientPub, keyPath := genKeyPair(t)
	jumpSrv, jumpAddr := newFakeSSHServer(t, clientPub)
	_ = jumpSrv
	_, targetAddr := newFakeSSHServer(t, clientPub)
	echo := startEcho(t)

	jumpHost, jumpPort := splitHostPort(jumpAddr, "22")
	targetHost, targetPort := splitHostPort(targetAddr, "22")

	withConfig(t, fmt.Sprintf(`
Host target
  HostName %s
  Port %s
  User tester
  IdentityFile %s
  StrictHostKeyChecking no
  ProxyJump bastion
Host bastion
  HostName %s
  Port %s
  User tester
  IdentityFile %s
  StrictHostKeyChecking no
`, targetHost, targetPort, keyPath, jumpHost, jumpPort, keyPath))

	opts, err := Resolve("target", Options{NoAgent: true, Timeout: 5 * time.Second}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(opts.ProxyJump) != 1 {
		t.Fatalf("expected 1 jump hop, got %d", len(opts.ProxyJump))
	}
	d, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("Dial through jump: %v", err)
	}
	defer d.Close()
	roundTrip(t, d, echo)
}
