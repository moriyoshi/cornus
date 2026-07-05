package deploy

import (
	"context"
	"testing"
)

// incusMap is the map an incus instance actually reported, read from
// volatile.idmap.current on the live daemon in the E2E runner:
//
//	[{"Isuid":true,"Isgid":false,"Hostid":1000000,"Nsid":0,"Maprange":1000000000},
//	 {"Isuid":false,"Isgid":true,"Hostid":1000000,"Nsid":0,"Maprange":1000000000}]
//
// The fixture is the measurement rather than an invented shape, because the
// arithmetic is only worth anything if it matches what a real daemon says.
var incusMap = IDMap{
	{ContainerID: 0, HostID: 1000000, Count: 1000000000, UIDs: true},
	{ContainerID: 0, HostID: 1000000, Count: 1000000000, GIDs: true},
}

func TestHostIDsAgainstTheRealIncusMap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      int
		wantUID int
		wantOK  bool
	}{
		{"container root", 0, 1000000, true},
		// The case that made this facility necessary: a workload running as 1000
		// needs 1001000, NOT the range base. A file owned by container-root is
		// exactly as unreadable to it as an unmapped one.
		{"a non-root workload", 1000, 1001000, true},
		{"the last id in range", 999999999, 1000999999, true},
		{"one past the end", 1000000000, 0, false},
		{"negative", -1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := incusMap.HostUID(tc.id)
			if ok != tc.wantOK {
				t.Fatalf("HostUID(%d) ok = %v, want %v", tc.id, ok, tc.wantOK)
			}
			if ok && got != tc.wantUID {
				t.Fatalf("HostUID(%d) = %d, want %d", tc.id, got, tc.wantUID)
			}
		})
	}
}

// TestRangeKindIsHonoured: a uid range must not answer a gid question. incus
// reports the two separately and podman can map them differently, so conflating
// them would produce a file whose group is silently wrong.
func TestRangeKindIsHonoured(t *testing.T) {
	uidOnly := IDMap{{ContainerID: 0, HostID: 1000000, Count: 65536, UIDs: true}}
	if _, ok := uidOnly.HostUID(0); !ok {
		t.Fatal("a uid range did not answer a uid")
	}
	if _, ok := uidOnly.HostGID(0); ok {
		t.Fatal("a uid-only range answered a GID question; the group would be silently wrong")
	}
}

// TestEmptyMapIsIdentity pins the equivalence the caller depends on: "this
// backend does not implement IDMapper" and "this backend reports no ranges" must
// mean the same thing, or the two spellings of "no remapping" diverge.
func TestEmptyMapIsIdentity(t *testing.T) {
	var none IDMap
	for _, id := range []int{0, 1000, 65534} {
		if got, ok := none.HostUID(id); !ok || got != id {
			t.Fatalf("empty map HostUID(%d) = %d,%v, want %d,true", id, got, ok, id)
		}
	}
}

// TestSecondRangeIsSearched: a map with several ranges must consult all of them.
// Rootless podman reports exactly this shape — root mapped alone, then the
// subuid range — and stopping at the first would map every non-root id to
// nothing.
func TestSecondRangeIsSearched(t *testing.T) {
	m := IDMap{
		{ContainerID: 0, HostID: 1000, Count: 1, UIDs: true, GIDs: true},
		{ContainerID: 1, HostID: 100000, Count: 65536, UIDs: true, GIDs: true},
	}
	if got, ok := m.HostUID(0); !ok || got != 1000 {
		t.Fatalf("HostUID(0) = %d,%v, want 1000,true", got, ok)
	}
	if got, ok := m.HostUID(1); !ok || got != 100000 {
		t.Fatalf("HostUID(1) = %d,%v, want 100000,true", got, ok)
	}
	if got, ok := m.HostUID(65536); !ok || got != 165535 {
		t.Fatalf("HostUID(65536) = %d,%v, want 165535,true", got, ok)
	}
	if _, ok := m.HostUID(65537); ok {
		t.Fatal("an id past every range resolved anyway")
	}
}

// TestZeroCountRangeNeverMatches: a malformed range must be skipped rather than
// match its start id, which would map one id to a host id nothing intended.
func TestZeroCountRangeNeverMatches(t *testing.T) {
	m := IDMap{{ContainerID: 0, HostID: 999, Count: 0, UIDs: true}}
	if _, ok := m.HostUID(0); ok {
		t.Fatal("a zero-length range matched")
	}
}

// --- HostIDsFor ---

type noMapBackend struct{ Backend }

type mapBackend struct {
	Backend
	m   IDMap
	err error
}

func (b mapBackend) IDMap(context.Context, string) (IDMap, error) { return b.m, b.err }

// TestHostIDsForWithoutTheCapabilityIsIdentity: the backends that do not remap
// (rootful dockerhost, containerd, bare) must be completely unaffected, which is
// what makes this safe to put on the shared write path.
func TestHostIDsForWithoutTheCapabilityIsIdentity(t *testing.T) {
	uid, gid, err := HostIDsFor(context.Background(), noMapBackend{}, "web", 1000, 2000)
	if err != nil || uid != 1000 || gid != 2000 {
		t.Fatalf("= %d,%d,%v; a backend with no IDMapper must be the identity", uid, gid, err)
	}
}

func TestHostIDsForTranslates(t *testing.T) {
	uid, gid, err := HostIDsFor(context.Background(), mapBackend{m: incusMap}, "web", 1000, 1000)
	if err != nil {
		t.Fatalf("HostIDsFor: %v", err)
	}
	if uid != 1001000 || gid != 1001000 {
		t.Fatalf("= %d:%d, want 1001000:1001000", uid, gid)
	}
}

// TestHostIDsForRefusesAnUnmappedID is the half that keeps this from re-creating
// the bug. Falling back to the identity for an id the backend does not map is
// precisely how an unreadable file gets written while everything looks fine.
func TestHostIDsForRefusesAnUnmappedID(t *testing.T) {
	small := IDMap{{ContainerID: 0, HostID: 1000000, Count: 100, UIDs: true, GIDs: true}}
	if _, _, err := HostIDsFor(context.Background(), mapBackend{m: small}, "web", 5000, 0); err == nil {
		t.Fatal("an unmapped uid resolved anyway; the file would be written owned by something " +
			"the workload cannot see as its own")
	}
}
