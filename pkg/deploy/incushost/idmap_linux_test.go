//go:build linux

package incushost

import (
	"testing"

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
