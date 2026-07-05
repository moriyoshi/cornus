package hub

import "testing"

func TestRegistryLookupRoundRobin(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Lookup("web"); ok {
		t.Fatal("empty registry should miss")
	}
	r.Register("c1", "web", "10.0.0.1:80", "")
	r.Register("c2", "web", "10.0.0.2:80", "")

	// Two replicas → Lookup rotates across both.
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		tgt, ok := r.Lookup("web")
		if !ok {
			t.Fatal("web should resolve")
		}
		seen[tgt.Addr]++
	}
	if seen["10.0.0.1:80"] != 2 || seen["10.0.0.2:80"] != 2 {
		t.Fatalf("round-robin uneven: %v", seen)
	}
}

func TestRegistryRemoveConn(t *testing.T) {
	r := NewRegistry()
	r.Register("c1", "web", "10.0.0.1:80", "")
	r.Register("c2", "web", "10.0.0.2:80", "")
	r.Register("c1", "db", "10.0.0.3:5432", "")

	r.RemoveConn("c1")
	if _, ok := r.Lookup("db"); ok {
		t.Error("db (only c1) should vanish when c1 disconnects")
	}
	tgt, ok := r.Lookup("web")
	if !ok || tgt.Addr != "10.0.0.2:80" {
		t.Errorf("web should resolve to the surviving c2 replica, got %q ok=%v", tgt.Addr, ok)
	}
}

// TestRegistryDeliverTarget confirms a delivery registration yields a Target with
// no Addr (the hub must deliver via the spoke, not dial).
func TestRegistryDeliverTarget(t *testing.T) {
	r := NewRegistry()
	r.RegisterDeliver("c1", "svc", nil) // nil session stands in; only the shape matters here
	tgt, ok := r.Lookup("svc")
	if !ok || tgt.Addr != "" {
		t.Fatalf("delivery target should have empty Addr, got %+v ok=%v", tgt, ok)
	}
}
