package remotecompanion

import (
	"net"
	"testing"

	"github.com/hashicorp/yamux"
)

// fakeSession returns a usable (but otherwise inert) *yamux.Session for
// registry identity tests — its contents are never read/written.
func fakeSession(t *testing.T) *yamux.Session {
	t.Helper()
	c1, c2 := net.Pipe()
	t.Cleanup(func() { c1.Close(); c2.Close() })
	sess, err := yamux.Client(c1, nil)
	if err != nil {
		t.Fatalf("yamux.Client: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func TestInstanceKey(t *testing.T) {
	if got, want := InstanceKey("web", 0), "web/0"; got != want {
		t.Errorf("InstanceKey = %q, want %q", got, want)
	}
	if got, want := InstanceKey("web", 3), "web/3"; got != want {
		t.Errorf("InstanceKey = %q, want %q", got, want)
	}
}

func TestRegistryPutGetRemove(t *testing.T) {
	r := NewRegistry()
	if got := r.Get("web/0"); got != nil {
		t.Fatal("Get on an empty registry should return nil")
	}

	sess := fakeSession(t)
	r.Put("web/0", sess)
	if got := r.Get("web/0"); got != sess {
		t.Fatalf("Get after Put = %v, want %v", got, sess)
	}

	r.Remove("web/0", sess)
	if got := r.Get("web/0"); got != nil {
		t.Fatal("Get after Remove should return nil")
	}
}

func TestRegistryReplacesEarlierRegistration(t *testing.T) {
	r := NewRegistry()
	first, second := fakeSession(t), fakeSession(t)

	r.Put("web/0", first)
	r.Put("web/0", second)
	if got := r.Get("web/0"); got != second {
		t.Fatal("a later Put should replace an earlier registration for the same instance")
	}

	// A stale Remove for the FIRST (already-superseded) session must not
	// clobber the second, still-live registration.
	r.Remove("web/0", first)
	if got := r.Get("web/0"); got != second {
		t.Fatal("a stale Remove for a superseded session must not remove the current one")
	}

	r.Remove("web/0", second)
	if got := r.Get("web/0"); got != nil {
		t.Fatal("Remove for the current session should remove it")
	}
}

func TestRegistryEmptyInstanceIsNoop(t *testing.T) {
	r := NewRegistry()
	sess := fakeSession(t)
	r.Put("", sess) // no ?instance= declared — must be a no-op, not a crash
	if got := r.Get(""); got != nil {
		t.Fatal("Put/Get with an empty instance key must be a no-op")
	}
	r.Remove("", sess) // must not panic
}
