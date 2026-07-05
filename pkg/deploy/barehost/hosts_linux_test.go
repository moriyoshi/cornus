//go:build linux

package barehost

import (
	"cornus/pkg/deploy/internal/hostrun"
	"os"
	"strings"
	"testing"
)

// seedNetworked writes a record with network fields and creates its hosts file,
// as createInstance would, so syncHosts has peers to resolve.
func seedNetworked(t *testing.T, b *Backend, app string, replica int, ip string, aliases map[string][]string) {
	t.Helper()
	id := instanceName(app, replica)
	rec := &instanceRecord{
		ID: id, App: app, Replica: replica, Image: "reg/" + app,
		Networks: []string{hostrun.DefaultNetwork},
		IP:       ip,
		NetIPs:   map[string]string{hostrun.DefaultNetwork: ip},
		Aliases:  aliases,
	}
	if err := b.writeRecord(rec); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}
	if _, err := b.hosts.Create(id, id, ip); err != nil {
		t.Fatalf("hosts.create: %v", err)
	}
}

func TestSyncHostsResolvesPeers(t *testing.T) {
	b, _ := newTestBackend(t)
	seedNetworked(t, b, "web", 0, "10.4.0.2", nil)
	seedNetworked(t, b, "cache", 0, "10.4.0.3", map[string][]string{hostrun.DefaultNetwork: {"redis"}})

	if err := b.syncHosts(); err != nil {
		t.Fatalf("syncHosts: %v", err)
	}

	webHosts, err := os.ReadFile(b.hosts.Path(instanceName("web", 0)))
	if err != nil {
		t.Fatalf("read web hosts: %v", err)
	}
	s := string(webHosts)
	// web resolves cache (by name + alias) and itself.
	if !strings.Contains(s, "10.4.0.3\tcache redis") {
		t.Errorf("web hosts missing cache+alias line:\n%s", s)
	}
	if !strings.Contains(s, "10.4.0.2\tweb") {
		t.Errorf("web hosts missing web self line:\n%s", s)
	}

	cacheHosts, _ := os.ReadFile(b.hosts.Path(instanceName("cache", 0)))
	if !strings.Contains(string(cacheHosts), "10.4.0.2\tweb") {
		t.Errorf("cache hosts missing web line:\n%s", cacheHosts)
	}
}

func TestSyncHostsMultiReplicaPicksReplicaZero(t *testing.T) {
	b, _ := newTestBackend(t)
	// web has two replicas; peers should resolve the app name to replica 0's IP.
	seedNetworked(t, b, "web", 0, "10.4.0.2", nil)
	seedNetworked(t, b, "web", 1, "10.4.0.9", nil)
	seedNetworked(t, b, "client", 0, "10.4.0.5", nil)

	if err := b.syncHosts(); err != nil {
		t.Fatalf("syncHosts: %v", err)
	}
	clientHosts, _ := os.ReadFile(b.hosts.Path(instanceName("client", 0)))
	s := string(clientHosts)
	if !strings.Contains(s, "10.4.0.2\tweb") {
		t.Errorf("client should resolve web to replica 0 (10.4.0.2):\n%s", s)
	}
	if strings.Contains(s, "10.4.0.9\tweb") {
		t.Errorf("client should NOT resolve web to replica 1 (10.4.0.9):\n%s", s)
	}
}
