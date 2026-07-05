//go:build linux

package containerdhost

import (
	"context"
	"os"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy/internal/hostrun"
)

func TestReplicaIndex(t *testing.T) {
	for id, want := range map[string]int{
		"cornus-web-0":         0,
		"cornus-web-12":        12,
		"cornus-a-b-3":         3,
		"no-trailing-x":        int(^uint(0) >> 1),
		"nodashatall":          int(^uint(0) >> 1),
		instanceName("web", 1): 1,
	} {
		if got := replicaIndex(id); got != want {
			t.Errorf("replicaIndex(%q) = %d, want %d", id, got, want)
		}
	}
}

// TestSyncHostsAcrossDeployments drives the full flow over the fakes: two
// services sharing a network see each other (and aliases) in their managed
// hosts blocks, replicas resolve the service name to replica 0's IP, and a
// delete drops the departed peer everywhere.
func TestSyncHostsAcrossDeployments(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	ctx := context.Background()

	// web gets 2 replicas: setup order hands web-0 10.4.0.9 and web-1 10.4.0.10.
	if _, err := b.Apply(ctx, api.DeploySpec{
		Name: "web", Image: "img", Replicas: 2,
		Networks: []api.NetworkAttachment{{Name: "front", Aliases: []string{"www"}}},
	}); err != nil {
		t.Fatalf("Apply web: %v", err)
	}
	// db joins later with 10.4.0.11.
	if _, err := b.Apply(ctx, api.DeploySpec{
		Name: "db", Image: "img",
		Networks: []api.NetworkAttachment{{Name: "front"}},
	}); err != nil {
		t.Fatalf("Apply db: %v", err)
	}

	readHosts := func(id string) string {
		t.Helper()
		data, err := os.ReadFile(b.hosts.Path(id))
		if err != nil {
			t.Fatalf("read hosts of %s: %v", id, err)
		}
		return string(data)
	}
	// Every member sees both services; web resolves to replica 0's IP with its
	// alias; apps are sorted so the block is deterministic.
	wantBlock := hostrun.HostsMarkerBegin + "\n10.4.0.11\tdb\n10.4.0.9\tweb www\n" + hostrun.HostsMarkerEnd + "\n"
	for _, id := range []string{"cornus-web-0", "cornus-web-1", "cornus-db-0"} {
		if content := readHosts(id); !strings.Contains(content, wantBlock) {
			t.Fatalf("hosts of %s missing peer block:\n%s\nwant block:\n%s", id, content, wantBlock)
		}
	}
	// Seed self entry uses the instance's own IP even on replica 1.
	if content := readHosts("cornus-web-1"); !strings.Contains(content, "10.4.0.10\tcornus-web-1\n") {
		t.Fatalf("replica self entry missing:\n%s", content)
	}

	// Deleting db removes its hosts file and drops it from the peers.
	if err := b.Delete(ctx, "db"); err != nil {
		t.Fatalf("Delete db: %v", err)
	}
	if _, err := os.Stat(b.hosts.Path("cornus-db-0")); !os.IsNotExist(err) {
		t.Fatal("db hosts file not reaped")
	}
	content := readHosts("cornus-web-0")
	if strings.Contains(content, "\tdb\n") {
		t.Fatalf("deleted peer still resolvable:\n%s", content)
	}
	if !strings.Contains(content, "10.4.0.9\tweb www\n") {
		t.Fatalf("surviving service lost its entry:\n%s", content)
	}
}

// TestSyncHostsDisjointNetworks asserts isolation: services on different
// networks do not appear in each other's hosts files.
func TestSyncHostsDisjointNetworks(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	ctx := context.Background()
	if _, err := b.Apply(ctx, api.DeploySpec{
		Name: "a", Image: "img", Networks: []api.NetworkAttachment{{Name: "front"}},
	}); err != nil {
		t.Fatalf("Apply a: %v", err)
	}
	if _, err := b.Apply(ctx, api.DeploySpec{
		Name: "b", Image: "img", Networks: []api.NetworkAttachment{{Name: "back"}},
	}); err != nil {
		t.Fatalf("Apply b: %v", err)
	}
	data, err := os.ReadFile(b.hosts.Path("cornus-a-0"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\tb\n") {
		t.Fatalf("cross-network peer leaked into hosts:\n%s", data)
	}
	if !strings.Contains(string(data), "\ta\n") {
		t.Fatalf("own service entry missing:\n%s", data)
	}
}
