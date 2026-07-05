package main

import (
	"path/filepath"
	"testing"

	"cornus/pkg/clientconfig"
)

func TestHubRoleFromFlags(t *testing.T) {
	role, err := hubRoleFromFlags("ws://hub:5000", "laptop",
		[]string{"api=127.0.0.1:3000"},
		[]string{"db=127.0.0.9:5432"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if role.Server != "ws://hub:5000" || role.Identity != "laptop" {
		t.Errorf("server/identity = %q/%q", role.Server, role.Identity)
	}
	if len(role.Register) != 1 || role.Register[0].Name != "api" || role.Register[0].Target != "127.0.0.1:3000" || role.Register[0].Addr != "" {
		t.Errorf("register = %+v, want delivery api->127.0.0.1:3000", role.Register)
	}
	if len(role.Reach) != 1 || role.Reach[0].Name != "db" || role.Reach[0].Listen != "127.0.0.9" || len(role.Reach[0].Ports) != 1 || role.Reach[0].Ports[0] != 5432 {
		t.Errorf("reach = %+v, want db listen 127.0.0.9:5432", role.Reach)
	}
}

func TestHubRoleFromFlagsErrors(t *testing.T) {
	for _, tc := range []struct{ name, reg, reach string }{
		{"nothing", "", ""},
		{"bad-register", "noeq", ""},
		{"register-missing-port", "api=host", ""},
		{"reach-bad-port", "", "db=127.0.0.1:notaport"},
	} {
		var reg, reach []string
		if tc.reg != "" {
			reg = []string{tc.reg}
		}
		if tc.reach != "" {
			reach = []string{tc.reach}
		}
		if _, err := hubRoleFromFlags("ws://h", "", reg, reach); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
}

// TestHubCaretakerConfigFromProfile verifies that with no --server flag the hub
// resolves its connection from the selected connection profile: the profile's
// server becomes the hub endpoint and its token rides the caretaker config (and
// so the WebSocket handshake).
func TestHubCaretakerConfigFromProfile(t *testing.T) {
	t.Setenv("CORNUS_TOKEN", "")
	cli := writeConfig(t, &clientconfig.File{
		CurrentContext: "prod",
		Contexts: map[string]*clientconfig.Context{
			"prod": {Server: "https://prod.example.com", Token: "profile-tok"},
		},
	})
	cmd := &HubCmd{Identity: "laptop", Register: []string{"api=127.0.0.1:3000"}}

	cfg, cleanup, err := cmd.caretakerConfig(cli)
	if err != nil {
		t.Fatalf("caretakerConfig: %v", err)
	}
	defer cleanup()
	if cfg.Hub == nil {
		t.Fatal("cfg.Hub = nil")
	}
	if cfg.Hub.Server != "https://prod.example.com" {
		t.Errorf("Hub.Server = %q, want the profile server", cfg.Hub.Server)
	}
	if cfg.Token != "profile-tok" {
		t.Errorf("Token = %q, want profile-tok", cfg.Token)
	}
	if cfg.Hub.Identity != "laptop" || len(cfg.Hub.Register) != 1 || cfg.Hub.Register[0].Name != "api" || cfg.Hub.Register[0].Target != "127.0.0.1:3000" {
		t.Errorf("role = %+v, want identity laptop with delivery api->127.0.0.1:3000", cfg.Hub)
	}
}

// TestHubCaretakerConfigExplicitServerWins verifies the explicit --server flag
// overrides the profile's server (endpoint precedence) while the profile's
// credentials still apply — the same semantics as the other commands.
func TestHubCaretakerConfigExplicitServerWins(t *testing.T) {
	t.Setenv("CORNUS_TOKEN", "")
	cli := writeConfig(t, &clientconfig.File{
		CurrentContext: "prod",
		Contexts: map[string]*clientconfig.Context{
			"prod": {Server: "https://prod.example.com", Token: "profile-tok"},
		},
	})
	cmd := &HubCmd{Server: "ws://explicit:5000", Reach: []string{"db=127.0.0.9:5432"}}

	cfg, cleanup, err := cmd.caretakerConfig(cli)
	if err != nil {
		t.Fatalf("caretakerConfig: %v", err)
	}
	defer cleanup()
	if cfg.Hub == nil || cfg.Hub.Server != "ws://explicit:5000" {
		t.Fatalf("Hub.Server = %+v, want ws://explicit:5000", cfg.Hub)
	}
	if cfg.Token != "profile-tok" {
		t.Errorf("Token = %q, want profile-tok (profile credentials still apply)", cfg.Token)
	}
}

// TestHubCaretakerConfigErrors covers the failure modes of the resolver wiring:
// no server anywhere, and flag errors surfacing before connection resolution.
func TestHubCaretakerConfigErrors(t *testing.T) {
	t.Setenv("CORNUS_TOKEN", "")

	// No --server and no config file: requireConn must error.
	cli := &CLI{Config: filepath.Join(t.TempDir(), "none.yaml")}
	cmd := &HubCmd{Register: []string{"api=127.0.0.1:3000"}}
	if _, _, err := cmd.caretakerConfig(cli); err == nil {
		t.Error("no server anywhere: expected an error")
	}

	// Bad flags fail before connection resolution, even with a valid profile.
	cli = writeConfig(t, &clientconfig.File{
		CurrentContext: "prod",
		Contexts:       map[string]*clientconfig.Context{"prod": {Server: "https://prod.example.com"}},
	})
	if _, _, err := (&HubCmd{}).caretakerConfig(cli); err == nil {
		t.Error("no register/reach: expected an error")
	}
}

// TestHubCaretakerConfigTLS verifies a profile's TLS material is passed through
// to the caretaker config (Config.TLSClientConfig) instead of being refused: the
// resolved *tls.Config reaches the caretaker's WebSocket dial.
func TestHubCaretakerConfigTLS(t *testing.T) {
	t.Setenv("CORNUS_TOKEN", "")
	cli := writeConfig(t, &clientconfig.File{
		CurrentContext: "tls",
		Contexts: map[string]*clientconfig.Context{
			"tls": {Server: "https://prod.example.com", TLS: &clientconfig.TLS{InsecureSkipVerify: true}},
		},
	})
	cmd := &HubCmd{Register: []string{"api=127.0.0.1:3000"}}

	cfg, cleanup, err := cmd.caretakerConfig(cli)
	if err != nil {
		t.Fatalf("caretakerConfig: %v", err)
	}
	defer cleanup()
	if cfg.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig = nil, want the profile's TLS config passed through")
	}
	if !cfg.TLSClientConfig.InsecureSkipVerify {
		t.Error("TLSClientConfig.InsecureSkipVerify = false, want the profile's setting to land")
	}
}
