//go:build linux

package incushost

import (
	"context"
	"encoding/json"
	"fmt"

	"cornus/pkg/deploy"
)

var _ deploy.IDMapper = (*Backend)(nil)

// idmapConfigKey is where incus records the map it ACTUALLY applied to an
// instance, as opposed to what was requested. Reading the applied map is the
// point: `raw.idmap` and the daemon's allocation policy both feed it, so
// deriving the map from configuration would be a second opinion about a fact
// incus has already settled.
const idmapConfigKey = "volatile.idmap.current"

// idmapNextConfigKey is where incus records the map an instance WILL be given,
// from the moment it is created. It is the answer for an instance that exists but
// has never been started, where volatile.idmap.current is absent.
//
// That state is not exotic — it is what a deploy is in between creating an
// instance and starting it, which is exactly when a credential file's ownership
// has to be decided. Reading only .current there returns "" and, under the
// absent-means-identity rule below, reports NO REMAPPING for an instance that is
// about to be remapped: files owned as the server itself, unreadable inside, and
// silent until the workload complains.
//
// Measured on a live daemon (2026-08-08), not taken from documentation:
//
//   - created, not started:  .current absent, .next holds the ranges
//   - after first start:     .current holds exactly what .next predicted
//   - privileged instance:   BOTH are "[]" — the empty map, i.e. the identity
//
// The privileged case is why this needs no security.privileged check. "[]" is an
// empty range set, which parseIncusIDMap turns into an empty IDMap, which IDMap
// already treats as the identity. A fallback that had to special-case privileged
// would be a second opinion about a fact the daemon has already stated.
const idmapNextConfigKey = "volatile.idmap.next"

// incusIDRange is one entry of volatile.idmap.current, whose JSON is written by
// incus's own idmapset type:
//
//	[{"Isuid":true,"Isgid":false,"Hostid":1000000,"Nsid":0,"Maprange":1000000000}, ...]
//
// Read from a live daemon rather than from documentation; the field names are
// capitalized exactly as shown.
type incusIDRange struct {
	Isuid    bool `json:"Isuid"`
	Isgid    bool `json:"Isgid"`
	Hostid   int  `json:"Hostid"`
	Nsid     int  `json:"Nsid"`
	Maprange int  `json:"Maprange"`
}

// IDMap implements deploy.IDMapper.
//
// It reads replica 0's map. The map is a property of how incusd allocates id
// ranges rather than of any one instance's workload, so replicas of one
// deployment share it; reading the first is enough and saves a lookup per file.
//
// An instance with NO recorded map is the identity, not an error. That is the
// privileged case (`security.privileged=true`), where incus applies no user
// namespace at all and a host uid means what it says. Treating it as unknown
// would refuse credential delivery to exactly the instances that need no
// translation.
//
// The APPLIED map wins whenever it exists; .next is consulted only for an
// instance that has not started yet (see idmapNextConfigKey). The order matters
// rather than being a formality: incus keeps .next populated after start too, so
// reading it first would answer with a prediction where a fact was available.
func (b *Backend) IDMap(ctx context.Context, name string) (deploy.IDMap, error) {
	id := instanceName(name, 0)
	in, err := b.conn.Instance(id)
	if err != nil {
		return nil, fmt.Errorf("incus: read id map for %s: %w", id, err)
	}
	if in == nil {
		return nil, fmt.Errorf("incus: instance %s not found", id)
	}
	return parseIncusIDMap(instanceIDMapRaw(in.Config))
}

// instanceIDMapRaw picks the id map an instance's config actually carries: the
// APPLIED map when it exists, the predicted one otherwise. Shared with the
// credential path, which reads other replicas' configs directly, so the two
// cannot come to different conclusions about the same instance.
func instanceIDMapRaw(config map[string]string) string {
	if raw := config[idmapConfigKey]; raw != "" {
		return raw
	}
	return config[idmapNextConfigKey]
}

// parseIncusIDMap converts the volatile.idmap.current JSON into the neutral
// form. Empty (or absent) is the identity — see IDMap.
func parseIncusIDMap(raw string) (deploy.IDMap, error) {
	if raw == "" {
		return nil, nil
	}
	var ranges []incusIDRange
	if err := json.Unmarshal([]byte(raw), &ranges); err != nil {
		// A map we cannot read is NOT the identity: answering "no remapping"
		// here would write files owned by ids the workload cannot see, which is
		// the failure this whole facility exists to prevent.
		return nil, fmt.Errorf("incus: %s is not a readable id map (%q): %w", idmapConfigKey, raw, err)
	}
	out := make(deploy.IDMap, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, deploy.IDRange{
			ContainerID: r.Nsid,
			HostID:      r.Hostid,
			Count:       r.Maprange,
			UIDs:        r.Isuid,
			GIDs:        r.Isgid,
		})
	}
	return out, nil
}
