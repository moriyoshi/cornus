//go:build linux

package hostrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/pkg/api"
)

func testCNIManager(t *testing.T) *CNIManager {
	t.Helper()
	return NewCNIManager(t.TempDir(), "test", "test")
}

func TestSubnetAllocator(t *testing.T) {
	m := testCNIManager(t)
	m.mu.Lock()
	defer m.mu.Unlock()

	if idx, err := m.allocateSubnet(DefaultNetwork); err != nil || idx != 0 {
		t.Fatalf("default subnet = %d, %v (want 0)", idx, err)
	}
	a, err := m.allocateSubnet("front")
	if err != nil || a != 1 {
		t.Fatalf("first user subnet = %d, %v (want 1)", a, err)
	}
	b, err := m.allocateSubnet("back")
	if err != nil || b != 2 {
		t.Fatalf("second user subnet = %d, %v (want 2)", b, err)
	}
	// Idempotent: same name returns the same index.
	if again, _ := m.allocateSubnet("front"); again != a {
		t.Fatalf("re-allocation of front = %d, want %d", again, a)
	}
	// Release then re-allocate reuses the freed index.
	if err := m.releaseSubnet("front"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if c, _ := m.allocateSubnet("other"); c != 1 {
		t.Fatalf("reused subnet = %d, want 1", c)
	}
	// State persists across manager instances.
	m2 := &CNIManager{confDir: m.confDir, state: m.state}
	if idx, _ := m2.allocateSubnet("back"); idx != 2 {
		t.Fatalf("persisted subnet for back = %d, want 2", idx)
	}
}

func TestEnsureNetworksWritesConflist(t *testing.T) {
	m := testCNIManager(t)
	if err := m.EnsureNetworks([]string{DefaultNetwork, "front"}); err != nil {
		t.Fatalf("EnsureNetworks: %v", err)
	}
	// Idempotent.
	if err := m.EnsureNetworks([]string{"front"}); err != nil {
		t.Fatalf("EnsureNetworks again: %v", err)
	}

	data, err := os.ReadFile(m.conflistPath("front"))
	if err != nil {
		t.Fatalf("read conflist: %v", err)
	}
	var got struct {
		CNIVersion string `json:"cniVersion"`
		Name       string `json:"name"`
		Plugins    []struct {
			Type   string `json:"type"`
			Bridge string `json:"bridge"`
			IPAM   *struct {
				Type   string `json:"type"`
				Ranges [][]struct {
					Subnet string `json:"subnet"`
				} `json:"ranges"`
			} `json:"ipam"`
			Capabilities map[string]bool `json:"capabilities"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse conflist: %v", err)
	}
	if got.Name != "cornus-front" || got.CNIVersion != "1.0.0" {
		t.Fatalf("conflist header = %+v", got)
	}
	if len(got.Plugins) < 2 || got.Plugins[0].Type != "bridge" || got.Plugins[1].Type != "portmap" {
		t.Fatalf("plugins = %+v", got.Plugins)
	}
	if got.Plugins[0].Bridge != bridgeIfName("front") || len(got.Plugins[0].Bridge) > 15 {
		t.Fatalf("bridge name = %q", got.Plugins[0].Bridge)
	}
	if got.Plugins[0].IPAM.Ranges[0][0].Subnet != "10.4.1.0/24" {
		t.Fatalf("subnet = %q (front should get index 1 after default)", got.Plugins[0].IPAM.Ranges[0][0].Subnet)
	}
	if !got.Plugins[1].Capabilities["portMappings"] {
		t.Fatal("portmap plugin must declare the portMappings capability")
	}

	// The default network landed on index 0.
	data, _ = os.ReadFile(m.conflistPath(DefaultNetwork))
	if !strings.Contains(string(data), "10.4.0.0/24") {
		t.Fatalf("default conflist missing subnet 10.4.0.0/24: %s", data)
	}
}

func TestConflistRendersBridgePortmap(t *testing.T) {
	data, err := conflist("mynet", "10.4.7.0/24", false)
	if err != nil {
		t.Fatalf("conflist: %v", err)
	}
	s := string(data)
	for _, want := range []string{`"type": "bridge"`, `"type": "portmap"`, `"10.4.7.0/24"`, `"cornus-mynet"`} {
		if !strings.Contains(s, want) {
			t.Errorf("conflist missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, `"firewall"`) {
		t.Error("firewall plugin should be absent when not requested")
	}
}

func TestSubnetBaseOverride(t *testing.T) {
	t.Setenv("CORNUS_CNI_SUBNET_BASE", "10.99")
	m := testCNIManager(t)
	if err := m.EnsureNetworks([]string{"x"}); err != nil {
		t.Fatalf("EnsureNetworks: %v", err)
	}
	data, _ := os.ReadFile(m.conflistPath("x"))
	if !strings.Contains(string(data), "10.99.1.0/24") {
		t.Fatalf("subnet base override not applied: %s", data)
	}
}

func TestRemoveNetwork(t *testing.T) {
	m := testCNIManager(t)
	if err := m.EnsureNetworks([]string{"gone"}); err != nil {
		t.Fatalf("EnsureNetworks: %v", err)
	}
	if err := m.RemoveNetwork("gone"); err != nil {
		t.Fatalf("RemoveNetwork: %v", err)
	}
	if _, err := os.Stat(m.conflistPath("gone")); !os.IsNotExist(err) {
		t.Fatal("conflist should be removed")
	}
	// Removing again is fine.
	if err := m.RemoveNetwork("gone"); err != nil {
		t.Fatalf("RemoveNetwork twice: %v", err)
	}
	// The default network's conflist is never removed.
	if err := m.EnsureNetworks([]string{DefaultNetwork}); err != nil {
		t.Fatalf("EnsureNetworks default: %v", err)
	}
	if err := m.RemoveNetwork(DefaultNetwork); err != nil {
		t.Fatalf("RemoveNetwork default: %v", err)
	}
	if _, err := os.Stat(m.conflistPath(DefaultNetwork)); err != nil {
		t.Fatal("default network conflist must survive RemoveNetwork")
	}
}

func TestPluginDirsResolution(t *testing.T) {
	t.Setenv("CORNUS_CNI_BIN_DIR", "")
	t.Setenv("CNI_PATH", "")
	if got := pluginDirs(); len(got) != 1 || got[0] != "/opt/cni/bin" {
		t.Fatalf("default plugin dirs = %v", got)
	}
	t.Setenv("CNI_PATH", "/a:/b")
	if got := pluginDirs(); len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Fatalf("CNI_PATH dirs = %v", got)
	}
	t.Setenv("CORNUS_CNI_BIN_DIR", "/custom")
	if got := pluginDirs(); len(got) != 1 || got[0] != "/custom" {
		t.Fatalf("CORNUS_CNI_BIN_DIR dirs = %v", got)
	}
}

func TestCheckPluginsActionableError(t *testing.T) {
	m := testCNIManager(t)
	dir := t.TempDir()
	// Provide only bridge; the error must name the missing ones.
	if err := os.WriteFile(filepath.Join(dir, "bridge"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := m.checkPlugins([]string{dir})
	if err == nil {
		t.Fatal("missing plugins must error")
	}
	for _, want := range []string{"portmap", "host-local", "loopback", "CORNUS_CNI_BIN_DIR"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should mention %s", err, want)
		}
	}
	// All present passes.
	for _, p := range []string{"portmap", "host-local", "loopback"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.checkPlugins([]string{dir}); err != nil {
		t.Fatalf("all plugins present should pass: %v", err)
	}
}

func TestToCNIPorts(t *testing.T) {
	got := toCNIPorts([]api.PortMapping{
		{Host: 8080, Container: 80},
		{Container: 9090},                            // unpublished: skipped
		{Host: 5353, Container: 53, Protocol: "udp"}, // udp preserved
	})
	if len(got) != 2 {
		t.Fatalf("ports = %+v", got)
	}
	if got[0].HostPort != 8080 || got[0].ContainerPort != 80 || got[0].Protocol != "tcp" {
		t.Fatalf("tcp mapping = %+v", got[0])
	}
	if got[1].Protocol != "udp" || got[1].HostPort != 5353 {
		t.Fatalf("udp mapping = %+v", got[1])
	}
}

func TestUnsupportedNetworkFeatures(t *testing.T) {
	if got := UnsupportedNetworkFeatures(api.DeploySpec{}); len(got) != 0 {
		t.Fatalf("empty spec = %v", got)
	}
	// Plain named networks, an explicit bridge driver, and aliases are all realized
	// by the backend (aliases resolve via the hosts-file sync): nothing to warn about.
	ok := api.DeploySpec{Networks: []api.NetworkAttachment{
		{Name: "front", Aliases: []string{"web"}},
		{Name: "back", Driver: "bridge"},
	}}
	if got := UnsupportedNetworkFeatures(ok); len(got) != 0 {
		t.Fatalf("supported spec = %v", got)
	}
	spec := api.DeploySpec{Networks: []api.NetworkAttachment{
		{Name: "front", Aliases: []string{"web"}},
		{Name: "back", Driver: "macvlan", DriverOpts: map[string]string{"parent": "eth0"}},
	}}
	got := UnsupportedNetworkFeatures(spec)
	if len(got) != 2 || got[0] != "driver" || got[1] != "driverOpts" {
		t.Fatalf("unsupported features = %v", got)
	}
}

func TestInstanceNetworks(t *testing.T) {
	if got := InstanceNetworks(api.DeploySpec{}); len(got) != 1 || got[0] != DefaultNetwork {
		t.Fatalf("implicit networks = %v", got)
	}
	spec := api.DeploySpec{Networks: []api.NetworkAttachment{{Name: "a"}, {Name: "b"}}}
	if got := InstanceNetworks(spec); len(got) != 2 || got[0] != "a" {
		t.Fatalf("explicit networks = %v", got)
	}
}
