//go:build linux

package barehost

// Host-reboot recovery. The runc state root (/run/cornus/bare-runc) and the
// pinned network namespaces (/run/cornus/netns) live on tmpfs, so a host reboot
// wipes them — exactly the "running" view we want cleared. But the persistent
// truth (the instance records, the image content + snapshot chain, the bundle
// config.json, the CNI conflists + subnet allocation, and the hosts/resolv files)
// all survive on disk under DataDir. After a reboot the startup reconcile finds a
// desired-running instance whose netns pin is gone and its rootfs unmounted; it
// cannot just restartInstance (the bundle's config.json points at a dead netns and
// there is no mounted rootfs). recoverInstance rebuilds those two ephemeral pieces
// — re-mount the rootfs off the surviving snapshot chain, and rebuild the netns +
// CNI attachment + pin, repointing the bundle spec at the fresh path — so the
// subsequent restartInstance launches cleanly. This mirrors containerdhost's
// repairNetns, except cornus IS the restart monitor here, so reconcile relaunches
// directly rather than leaving a repaired spec for a daemon to pick up.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"

	"cornus/pkg/deploy/internal/hostrun"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// needsRebootRecovery decides whether a not-running, desired-running record needs
// the full reboot rebuild (rootfs + netns) before it can be restarted, versus a
// plain in-place restart (a mere crash, with the rootfs still mounted and the
// netns pin intact). alive is the netns liveness probe (netnsAlive in production;
// injected by tests). Companions are excluded: they JOIN their app instance's
// netns rather than owning one, so net.setup (which recoverInstance calls) would
// wrongly mint them a private netns — their reboot recovery is a follow-up.
func needsRebootRecovery(rec *instanceRecord, alive func(string) bool) bool {
	if isCompanionRec(rec) {
		return false
	}
	if rec.NetNS == "" {
		return false
	}
	return !alive(rec.NetNS)
}

// companionRecoverable reports whether a companion caretaker can be brought back
// after a host reboot, BY ROLE. A reboot destroys every client connection the
// server held, so the answer is not about the container — it is about whether the
// caretaker's peer still exists:
//
//   - roleTelemetryCaretaker is self-contained. It receives the app's OTLP on
//     shared-netns loopback and exports outward on its own, without relaying
//     through the cornus server (telemetry_linux.go), so a fresh one is as good
//     as the old one.
//   - roleMountCaretaker and roleEgressCaretaker both terminate at the CLIENT
//     that requested the deployment — the 9P mount relay streams from the
//     caller's filesystem, and client-side egress routes back out through the
//     caller. That peer is gone after a reboot, so resurrecting the caretaker
//     yields one that cannot serve: the mount would hang rather than fail.
//
// An UNKNOWN role is not recovered. The failure modes are asymmetric — a
// self-contained companion left stopped loses telemetry, while a client-tethered
// one wrongly resurrected can hang the app's mounts — so a role added later must
// opt IN here deliberately rather than inherit a resurrection it cannot honour.
func companionRecoverable(role string) bool {
	switch role {
	case roleTelemetryCaretaker:
		return true
	case roleMountCaretaker, roleEgressCaretaker:
		return false
	default:
		return false
	}
}

// appInstanceFor finds the app instance a companion accompanies: same deployment,
// same replica, and an app record rather than another companion. Returns nil when
// the app instance is gone (a companion outliving its app is orphaned; the caller
// leaves it stopped for Delete to reap).
func appInstanceFor(recs []*instanceRecord, comp *instanceRecord) *instanceRecord {
	for _, r := range recs {
		if !isCompanionRec(r) && r.App == comp.App && r.Replica == comp.Replica {
			return r
		}
	}
	return nil
}

// companionNeedsRebootRecovery reports whether a companion's bundle still points
// at a netns that is not its app instance's live one.
//
// The companion record deliberately leaves NetNS EMPTY — it joins the app's netns
// and does not own one (companion_linux.go) — so unlike an app instance there is
// no recorded pin to probe. The persisted truth is in the companion's own
// config.json, and the comparison that matters is against the app's CURRENT pin:
// after reboot recovery the app holds a freshly-allocated netns path, so a
// companion still naming the old one has to be repointed. A plain crash leaves
// the two equal and needs no rebuild, which is what makes this safe to run on
// every reconcile pass rather than only after a reboot.
func companionNeedsRebootRecovery(comp, app *instanceRecord, alive func(string) bool) (bool, error) {
	if !isCompanionRec(comp) || app == nil || app.NetNS == "" || !alive(app.NetNS) {
		return false, nil
	}
	cur, err := bundleNetnsPath(comp.BundleDir)
	if err != nil {
		return false, err
	}
	return cur != app.NetNS, nil
}

// recoverCompanion rebuilds what a host reboot wiped for one companion caretaker:
// its rootfs mount, and the netns path its bundle names — repointed at the app
// instance's freshly-recovered pin rather than a private namespace of its own.
// It does NOT touch CNI or hosts/resolv state, which a companion never owns.
func (b *Backend) recoverCompanion(ctx context.Context, comp, app *instanceRecord) error {
	if app == nil || app.NetNS == "" {
		return fmt.Errorf("bare: recover companion %s: no app netns to rejoin", comp.ID)
	}
	if b.img != nil && comp.SnapshotKey != "" && comp.RootfsDir != "" {
		chainID, err := digest.Parse(comp.ChainID)
		if err != nil {
			return fmt.Errorf("bare: recover companion %s: bad chainID %q: %w", comp.ID, comp.ChainID, err)
		}
		if err := b.img.prepareRootfs(ctx, comp.SnapshotKey, chainID, comp.RootfsDir); err != nil {
			return err
		}
	}
	return rewriteNetnsPath(comp.BundleDir, app.NetNS)
}

// bundleNetnsPath reads the network-namespace path a bundle's config.json names.
// It is the read half of rewriteNetnsPath, and reports the same "no network
// namespace" error for a spec that has none, so a malformed bundle is a loud
// failure in both directions rather than a silent empty string.
func bundleNetnsPath(bundleDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(bundleDir, "config.json"))
	if err != nil {
		return "", fmt.Errorf("bare: read config.json: %w", err)
	}
	var s specs.Spec
	if err := json.Unmarshal(data, &s); err != nil {
		return "", fmt.Errorf("bare: parse config.json: %w", err)
	}
	if s.Linux == nil {
		return "", fmt.Errorf("bare: config.json %s has no linux section", bundleDir)
	}
	for _, ns := range s.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			return ns.Path, nil
		}
	}
	return "", fmt.Errorf("bare: config.json %s has no network namespace", bundleDir)
}

// recoverInstance rebuilds the ephemeral state a host reboot wiped for one
// instance — its rootfs mount and its netns pin — updating and persisting the
// record with the freshly-allocated netns/IP. After it returns nil the bundle's
// config.json points at a live netns and the rootfs is mounted, so restartInstance
// can launch the instance normally. The caller re-syncs hosts + DNS once, after
// all recoveries, because the fresh IPs change peer resolution.
func (b *Backend) recoverInstance(ctx context.Context, rec *instanceRecord) error {
	// 1) Re-mount the rootfs off the surviving snapshot chain (the mount is gone;
	// the committed chain + content persist on disk). prepareRootfs is a
	// no-op-safe rebuild: an already-prepared snapshot key is remounted.
	if b.img != nil && rec.SnapshotKey != "" && rec.RootfsDir != "" {
		chainID, err := digest.Parse(rec.ChainID)
		if err != nil {
			return fmt.Errorf("bare: recover %s: bad chainID %q: %w", rec.ID, rec.ChainID, err)
		}
		if err := b.img.prepareRootfs(ctx, rec.SnapshotKey, chainID, rec.RootfsDir); err != nil {
			return err
		}
	}
	// 2) Rebuild the netns + CNI attachment + pin, and repoint the bundle spec.
	return b.repairNetns(ctx, rec)
}

// repairNetns rebuilds an instance's dead netns pin: it clears any stale bind
// target, re-realizes the CNI attachment on a fresh pinned netns (re-publishing
// replica-0 host ports), rewrites the bundle's config.json to point at the new
// netns path, and persists the fresh netns/IP into the record. A no-op when the
// pin is already alive.
func (b *Backend) repairNetns(ctx context.Context, rec *instanceRecord) (retErr error) {
	if rec.NetNS == "" || hostrun.NetnsAlive(rec.NetNS) {
		return nil
	}
	networks := rec.Networks
	if len(networks) == 0 {
		networks = []string{hostrun.DefaultNetwork}
	}
	// Release the stale CNI attachment for this id before re-realizing it. host-local
	// keeps its IP reservations in a PERSISTENT store (/var/lib/cni), which a reboot
	// does not clear, so re-running setup for the same container id would fail
	// "duplicate allocation". teardown's cni.Remove runs ipam-del (freeing the IP even
	// though the netns is already gone) AND removes the leftover dead bind target.
	// Best-effort: only the ipam release matters here.
	b.net.Teardown(ctx, rec.ID, rec.NetNS, networks, rec.Ports)
	if err := b.net.EnsureNetworks(networks); err != nil {
		return err
	}
	att, err := b.net.Setup(ctx, rec.ID, networks, rec.Ports)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			b.net.Teardown(ctx, rec.ID, att.Netns, networks, rec.Ports)
		}
	}()
	if err := rewriteNetnsPath(rec.BundleDir, att.Netns); err != nil {
		return err
	}
	// Persist the fresh attachment onto a re-read of the record under the
	// per-record lock: recovery runs at startup, when a shim that outlived the
	// previous server may still be amending its own fields (exit code, restart
	// tally). Amending only the network fields keeps both sides' writes.
	apply := func(r *instanceRecord) error {
		r.NetNS = att.Netns
		if att.IP != "" {
			r.IP = att.IP
		}
		if len(att.IPs) > 0 {
			r.NetIPs = att.IPs
		}
		return nil
	}
	if _, err := b.updateRecord(rec.ID, apply); err != nil {
		return err
	}
	// Keep the caller's copy in step — reconcile logs the new IP and relaunches
	// from this record.
	return apply(rec)
}

// rewriteNetnsPath repoints the network-namespace path in a bundle's config.json
// (which a reboot left pointing at a dead pin) at netnsPath, preserving the rest
// of the spec. It reuses writeBundleConfig so the file is written the same way
// createInstance wrote it.
func rewriteNetnsPath(bundleDir, netnsPath string) error {
	data, err := os.ReadFile(filepath.Join(bundleDir, "config.json"))
	if err != nil {
		return fmt.Errorf("bare: read config.json: %w", err)
	}
	var s specs.Spec
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("bare: parse config.json: %w", err)
	}
	if s.Linux == nil {
		return fmt.Errorf("bare: config.json %s has no linux section", bundleDir)
	}
	found := false
	for i, ns := range s.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			s.Linux.Namespaces[i].Path = netnsPath
			found = true
		}
	}
	if !found {
		return fmt.Errorf("bare: config.json %s has no network namespace to repoint", bundleDir)
	}
	return writeBundleConfig(bundleDir, &s)
}
