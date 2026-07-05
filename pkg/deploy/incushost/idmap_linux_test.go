//go:build linux

package incushost

import (
	"context"
	"testing"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/deploy"
)

// TestParseIncusIDMapReadsTheRealShape uses the exact JSON a live incusd wrote
// to volatile.idmap.current, capitalization and all. The field names are not
// Go-idiomatic and are not documented anywhere cornus controls, so a fixture
// copied from the daemon is the only thing that keeps the tags honest.
func TestParseIncusIDMapReadsTheRealShape(t *testing.T) {
	const raw = `[{"Isuid":true,"Isgid":false,"Hostid":1000000,"Nsid":0,"Maprange":1000000000},` +
		`{"Isuid":false,"Isgid":true,"Hostid":1000000,"Nsid":0,"Maprange":1000000000}]`

	m, err := parseIncusIDMap(raw)
	if err != nil {
		t.Fatalf("parseIncusIDMap: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("ranges = %+v, want 2", m)
	}
	// The measured behaviour this must reproduce: a workload running as uid 1000
	// needs its file owned by host 1001000. Owning it as the range BASE (host
	// 1000000, i.e. container root) leaves it exactly as unreadable as before.
	if got, ok := m.HostUID(1000); !ok || got != 1001000 {
		t.Fatalf("HostUID(1000) = %d,%v, want 1001000,true", got, ok)
	}
	if got, ok := m.HostGID(1000); !ok || got != 1001000 {
		t.Fatalf("HostGID(1000) = %d,%v, want 1001000,true", got, ok)
	}
	// The uid entry must not answer for groups and vice versa — incus reports
	// them as separate entries precisely because they can differ.
	uidOnly := deploy.IDMap{m[0]}
	if _, ok := uidOnly.HostGID(0); ok {
		t.Fatal("the Isuid entry answered a GID question")
	}
}

// TestParseIncusIDMapEmptyIsIdentity: a privileged instance has no user
// namespace and records no map. That is "no remapping", and refusing it would
// deny credential files to the instances that need no translation at all.
func TestParseIncusIDMapEmptyIsIdentity(t *testing.T) {
	m, err := parseIncusIDMap("")
	if err != nil {
		t.Fatalf("empty map: %v", err)
	}
	if got, ok := m.HostUID(1000); !ok || got != 1000 {
		t.Fatalf("HostUID(1000) on an empty map = %d,%v, want the identity", got, ok)
	}
}

// TestParseIncusIDMapRefusesGarbage is the direction that matters. A map cornus
// cannot read must NOT degrade to the identity: that would write files owned by
// ids the workload cannot see, which is the exact failure this facility exists
// to remove, and it would do it while reporting success.
func TestParseIncusIDMapRefusesGarbage(t *testing.T) {
	if _, err := parseIncusIDMap("{not json"); err == nil {
		t.Fatal("an unreadable id map was accepted; falling back to the identity here writes " +
			"unreadable files and calls it success")
	}
}

// The fixtures below are copied verbatim from a live incusd (2026-08-08), because
// which KEY holds the map in which lifecycle state is the whole contract here and
// documentation does not settle it.
const (
	idmapRangesFixture = `[{"Isuid":true,"Isgid":false,"Hostid":1000000,"Nsid":0,"Maprange":1000000000},` +
		`{"Isuid":false,"Isgid":true,"Hostid":1000000,"Nsid":0,"Maprange":1000000000}]`
	// A privileged instance carries this in BOTH keys: an empty range set, which
	// is the identity, not a missing answer.
	idmapEmptyFixture = `[]`
)

// idmapBackend builds a backend whose single instance carries the given
// volatile.idmap.* config, so IDMap can be driven end to end rather than only its
// parser. Driving the parser alone is what let the .current-only read survive:
// the parser was always right about the string it was handed, and the defect was
// in WHICH string it was handed.
func idmapBackend(t *testing.T, cfg map[string]string) *Backend {
	t.Helper()
	f := newFakeConn()
	f.insts[instanceName("app", 0)] = &incusapi.Instance{
		Name:        instanceName("app", 0),
		InstancePut: incusapi.InstancePut{Config: cfg},
	}
	return testBackend(f)
}

// TestIDMapReadsTheNotYetStartedInstance is the regression this was written for.
//
// An instance that has been created but never started has NO
// volatile.idmap.current and DOES have volatile.idmap.next. Reading only .current
// there reports the identity — "this runtime does not remap" — for an instance
// about to be remapped, so a credential file gets owned as the server itself and
// is unreadable inside. That is silent: the deploy succeeds and the application
// fails later with its own permission error.
//
// It is not a hypothetical state. It is precisely where a deploy sits between
// creating an instance and starting it, which is when file ownership must be
// decided.
func TestIDMapReadsTheNotYetStartedInstance(t *testing.T) {
	b := idmapBackend(t, map[string]string{idmapNextConfigKey: idmapRangesFixture})
	m, err := b.IDMap(context.Background(), "app")
	if err != nil {
		t.Fatalf("IDMap: %v", err)
	}
	got, ok := m.HostUID(1000)
	if !ok || got != 1001000 {
		t.Fatalf("HostUID(1000) = %d,%v, want 1001000,true — a created-but-not-started instance "+
			"reported no remapping, so a credential file would be owned by the server's own uid "+
			"and unreadable to the workload", got, ok)
	}
}

// TestIDMapPrefersTheAppliedMap: incus keeps .next populated after start too, so
// the order is load-bearing. A started instance must answer from .current — the
// map it ACTUALLY got — not from a prediction that may have been superseded.
func TestIDMapPrefersTheAppliedMap(t *testing.T) {
	const applied = `[{"Isuid":true,"Isgid":true,"Hostid":500000,"Nsid":0,"Maprange":65536}]`
	b := idmapBackend(t, map[string]string{
		idmapConfigKey:     applied,
		idmapNextConfigKey: idmapRangesFixture,
	})
	m, err := b.IDMap(context.Background(), "app")
	if err != nil {
		t.Fatalf("IDMap: %v", err)
	}
	got, ok := m.HostUID(1000)
	if !ok || got != 501000 {
		t.Fatalf("HostUID(1000) = %d,%v, want 501000,true — the applied map (.current) must win "+
			"over the prediction (.next), which incus leaves populated after start", got, ok)
	}
}

// TestIDMapPrivilegedIsIdentity pins why the fallback needs no
// security.privileged check. A privileged instance applies no user namespace and
// carries "[]" in both keys; "[]" is an empty range set, which IDMap already
// treats as the identity. Falling back to .next must not turn that into an
// invented mapping — the same silent-unreadable bug pointed the other way.
func TestIDMapPrivilegedIsIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  map[string]string
	}{
		{"created, not started", map[string]string{idmapNextConfigKey: idmapEmptyFixture}},
		{"started", map[string]string{idmapConfigKey: idmapEmptyFixture, idmapNextConfigKey: idmapEmptyFixture}},
		{"no keys at all", map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := idmapBackend(t, tc.cfg)
			m, err := b.IDMap(context.Background(), "app")
			if err != nil {
				t.Fatalf("IDMap: %v", err)
			}
			got, ok := m.HostUID(1000)
			if !ok || got != 1000 {
				t.Fatalf("HostUID(1000) = %d,%v, want 1000,true — a privileged instance performs no "+
					"remapping, so a host uid means what it says", got, ok)
			}
		})
	}
}
