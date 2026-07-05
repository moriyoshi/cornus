//go:build linux

package builder

import (
	"testing"

	dockerconfigfile "github.com/docker/cli/cli/config/configfile"
	dockertypes "github.com/docker/cli/cli/config/types"
)

func TestSeedRegistryAuthOverridesOnlyNamedHosts(t *testing.T) {
	cfg := dockerconfigfile.New("")
	cfg.AuthConfigs["own.example:5000"] = dockertypes.AuthConfig{Username: "old", Password: "old"}
	cfg.AuthConfigs["third.example"] = dockertypes.AuthConfig{Username: "ambient", Password: "ambient"}
	seedRegistryAuth(cfg, map[string]RegistryCredential{
		"own.example:5000": {Username: "cornus-internal", Password: "short-lived"},
		"":                 {Username: "ignored", Password: "ignored"},
	})
	if got := cfg.AuthConfigs["own.example:5000"]; got.Username != "cornus-internal" || got.Password != "short-lived" || got.ServerAddress != "own.example:5000" {
		t.Fatalf("own-host credential = %+v", got)
	}
	if got := cfg.AuthConfigs["third.example"]; got.Username != "ambient" || got.Password != "ambient" {
		t.Fatalf("third-party credential changed: %+v", got)
	}
	if _, ok := cfg.AuthConfigs[""]; ok {
		t.Fatal("empty registry host was seeded")
	}
}
