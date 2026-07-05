package deploy

import (
	"context"
	"fmt"
)

// ID mapping: translating a workload's own uid/gid into the host ids a file must
// be owned by for that workload to read it.
//
// Cornus writes files a workload has to read — rendered credential files today,
// 9P mountpoints next — and chowns them to the uid it took from `spec.User`.
// That is a CONTAINER-side id. Where the runtime remaps ids (incus, rootless
// podman, docker with userns-remap), writing it straight to the host filesystem
// produces a file the workload cannot use, and the failure is unusually opaque:
// the kernel reports the owner as 65534, which is not an owner at all but the
// OVERFLOW uid, shown because the real owner is not mapped into the workload's
// user namespace. Nor can mode bits rescue it — a userns root holds
// CAP_DAC_OVERRIDE only over ids INSIDE its map, so an unmapped owner is outside
// its authority entirely and `chmod 0666` changes nothing.
//
// Measured on a live incusd: a host directory owned by uid 0, bound into an
// instance whose map is host 1000000 -> ns 0, reads as 65534 inside and refuses
// writes. Chowning the host side to 1000000 — and nothing else — makes it root
// inside and writable. (`raw.idmap "both 0 0"` also works, by dragging host root
// INTO the map, which is why it costs the isolation the instance was providing.
// The requirement is only that the owner be inside the map, not that it be root.)

// IDRange is one contiguous mapping from container-side ids onto host ids.
//
// It is shaped to carry both backends' wire forms without translation loss:
// incus reports `{Isuid, Isgid, Hostid, Nsid, Maprange}` per range, podman
// reports `{container_id, host_id, size}` per kind. A range that covers only
// gids must not answer a uid question, which is why the two flags are separate
// rather than one "kind".
type IDRange struct {
	// ContainerID is the first id as the WORKLOAD sees it (incus Nsid).
	ContainerID int
	// HostID is the id ContainerID maps to on the host (incus Hostid).
	HostID int
	// Count is how many consecutive ids the range covers (incus Maprange).
	Count int
	// UIDs and GIDs select which kind(s) this range applies to. Both may be
	// true; neither is a range that answers nothing, which is not an error but
	// simply never matches.
	UIDs bool
	GIDs bool
}

// IDMap is a backend's complete id mapping for one workload.
//
// An EMPTY map means no remapping — the identity. That is the same answer a
// backend gives by not implementing IDMapper at all, and it keeps the two
// spellings of "this runtime does not remap" from meaning different things.
type IDMap []IDRange

// HostUID maps a container-side uid to the host uid a file must be owned by.
// ok is false when no range covers it, which is a refusal rather than a guess:
// an unmapped id is exactly the case that produces an unreadable file, so
// silently falling back to the identity would reintroduce the bug this exists
// to remove.
func (m IDMap) HostUID(uid int) (int, bool) { return m.host(uid, true) }

// HostGID is HostUID for group ids.
func (m IDMap) HostGID(gid int) (int, bool) { return m.host(gid, false) }

func (m IDMap) host(id int, wantUID bool) (int, bool) {
	if len(m) == 0 {
		return id, true // no remapping
	}
	if id < 0 {
		return 0, false
	}
	for _, r := range m {
		if wantUID && !r.UIDs {
			continue
		}
		if !wantUID && !r.GIDs {
			continue
		}
		if r.Count <= 0 {
			continue
		}
		if id < r.ContainerID || id >= r.ContainerID+r.Count {
			continue
		}
		return r.HostID + (id - r.ContainerID), true
	}
	return 0, false
}

// IDMapper is an optional Backend capability: a backend whose runtime remaps ids
// reports the mapping, so the server can own a file as the WORKLOAD rather than
// as itself.
//
// A backend that does not implement it is stating that its runtime performs no
// remapping — true of rootful dockerhost, containerd and bare, where cornus
// either is the runtime or drives one that shares the host's id space. The
// kubernetes backend does not implement it either, for a different reason: its
// caretaker writes INSIDE the pod, so the ids it uses are already container-side
// and there is nothing to translate.
type IDMapper interface {
	Backend
	// IDMap reports the id mapping applied to the named deployment's workload.
	// An empty map means no remapping.
	//
	// It takes a deployment name because the mapping can be per-instance: incus
	// records the applied map on the instance itself (volatile.idmap.current),
	// not on the daemon.
	IDMap(ctx context.Context, name string) (IDMap, error)
}

// HostIDsFor resolves the host uid/gid a file for this workload must be owned
// by, given the container-side ids the spec asked for.
//
// It is the one place callers go, so the "backend does not implement IDMapper"
// and "backend reports an empty map" paths cannot diverge: both are the
// identity. An id the backend DOES map but which falls outside every range is an
// error, because that is the case that silently produces an unreadable file.
func HostIDsFor(ctx context.Context, backend Backend, name string, uid, gid int) (hostUID, hostGID int, err error) {
	m, ok := backend.(IDMapper)
	if !ok {
		return uid, gid, nil
	}
	idmap, err := m.IDMap(ctx, name)
	if err != nil {
		return 0, 0, err
	}
	hu, uok := idmap.HostUID(uid)
	if !uok {
		return 0, 0, fmt.Errorf("uid %d is not mapped into %s's id range, so a file owned by it "+
			"would be unreadable to the workload", uid, name)
	}
	hg, gok := idmap.HostGID(gid)
	if !gok {
		return 0, 0, fmt.Errorf("gid %d is not mapped into %s's id range, so a file owned by it "+
			"would be unreadable to the workload", gid, name)
	}
	return hu, hg, nil
}
