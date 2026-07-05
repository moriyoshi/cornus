package wire

import (
	"testing"

	"github.com/hugelgupf/p9/p9"
)

// The block proxy rewrites the ownership it reports so a workload on a remapping
// runtime sees the mount as its own. Measured on rootless podman with the rewrite
// disabled, the workload sees uid 65534 (the overflow id) and cannot write; with
// it enabled, uid 0 and writes succeed.
//
// These test reportOwner directly rather than through a live proxy, because what
// can go wrong here is the rewrite policy — when it applies and to which fields —
// not the block protocol, which the proxy's own tests cover.

func TestReportOwnerReplacesOwnership(t *testing.T) {
	a := &blockAttach{ownerUID: 1001, ownerGID: 1001, ownerSet: true}
	mask := p9.AttrMask{UID: true, GID: true, Mode: true, Size: true}
	in := p9.Attr{UID: 0, GID: 0, Size: 13}

	got := a.reportOwner(mask, in)
	if got.UID != p9.UID(1001) || got.GID != p9.GID(1001) {
		t.Fatalf("reportOwner = uid %d gid %d, want 1001/1001: an untranslated id lands "+
			"outside the container's map and the workload sees 65534", got.UID, got.GID)
	}
	if got.Size != 13 {
		t.Errorf("reportOwner changed Size to %d; it must rewrite ownership and nothing else", got.Size)
	}
}

// TestReportOwnerUnsetPassesThrough is the half that keeps this off every backend
// whose runtime does NOT remap ids. There the caller's real ownership must stay
// visible, which is the long-standing behaviour on rootful docker, bare and
// containerd — and the server only sets an owner when the map actually translates.
func TestReportOwnerUnsetPassesThrough(t *testing.T) {
	a := &blockAttach{} // ownerSet false
	mask := p9.AttrMask{UID: true, GID: true}
	in := p9.Attr{UID: 4242, GID: 4343}

	got := a.reportOwner(mask, in)
	if got.UID != p9.UID(4242) || got.GID != p9.GID(4343) {
		t.Fatalf("reportOwner rewrote ownership with no owner set (uid %d gid %d, want 4242/4343); "+
			"that would change what every non-remapping backend reports", got.UID, got.GID)
	}
}

// TestReportOwnerHonoursTheMask: an attribute the client did not ask for carries
// no meaningful value, so writing ownership into a response that never asked about
// ownership would be inventing data. uid 0 is a real id, so a zero UID here is not
// evidence by itself — the fixture uses a distinctive value that must survive.
func TestReportOwnerHonoursTheMask(t *testing.T) {
	a := &blockAttach{ownerUID: 1001, ownerGID: 1001, ownerSet: true}
	mask := p9.AttrMask{UID: false, GID: true}
	in := p9.Attr{UID: 7777, GID: 8888}

	got := a.reportOwner(mask, in)
	if got.UID != p9.UID(7777) {
		t.Errorf("reportOwner rewrote UID though the mask did not request it (got %d, want 7777)", got.UID)
	}
	if got.GID != p9.GID(1001) {
		t.Errorf("reportOwner = gid %d, want 1001: the mask DID request GID", got.GID)
	}
}
