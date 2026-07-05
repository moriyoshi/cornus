//go:build linux

package incushost

// Self-inspection for a cornus running AS an incus instance on the very incusd it
// drives.
//
// That topology is the one place a containerized incus server has NO routing
// problem, which is what makes recognizing it worth code. Instances are networked
// by incusd onto its own bridge in the host's netns; a cornus in a docker container
// has no route there and cannot acquire one (it is not an instance, so the docker
// self-attach has no analogue). But a cornus that IS an instance sits on that same
// bridge alongside the workloads, so port-forward, tunnels and caretaker dial-backs
// all just work — with neither host networking nor a per-instance companion. It is
// the incus counterpart of dockerhost's self-attach: not a repair cornus performs,
// but a topology it can now identify and stop warning about.
//
// Measured in an incus application container (recorded in JOURNAL.md):
//
//   - the instance's hostname IS its instance name, so GetInstance(hostname) is
//     the whole lookup;
//   - /dev/incus/sock is present, which is what pkg/hostenv now uses to notice an
//     instance at all (before, `cornus daemon preflight` inside one reported
//     "cornus runs on the host");
//   - the incusd socket must be exposed with a PROXY device, not a disk device: a
//     disk device is idmap-shifted to nobody:nobody and cornus fails with
//     "connect: permission denied". With a proxy device it connects.
//
// There is deliberately no path translation here. This backend hands incusd no
// cornus-built path at all — every path it passes is either the operator's own bind
// source (a host path by definition) or a container-side target — so the mounts
// reported below serve only to CONFIRM identity, never to build a map. See
// pkg/hostcheck's handsDataDirToRuntime rationale.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	incus "github.com/lxc/incus/v6/client"

	"cornus/pkg/hostenv"
)

// SelfIDCandidates returns this process's own instance name, if it looks like we
// are inside an incus instance at all.
//
// The hostname is the candidate because incus sets an instance's hostname to its
// name. hostenv's own miners cannot produce it: they recognize 64-hex container ids
// and the docker/CRI cgroup spellings, and an instance name is an arbitrary label.
//
// Gated on the guest socket so a cornus on the HOST does not offer its hostname as
// an instance name — that would be a candidate the daemon might genuinely hold (an
// instance named after the host is not far-fetched), and confirming it would derive
// an identity from a coincidence.
func SelfIDCandidates() []string {
	if _, err := os.Stat(devIncusSocket); err != nil {
		return nil
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		return nil
	}
	return []string{name}
}

// devIncusSocket is the guest API socket incus exposes inside every instance. Only
// its PRESENCE is used, as the "we are an instance" marker; cornus speaks to the
// full API over the daemon socket instead.
const devIncusSocket = "/dev/incus/sock"

// SelfInspector returns a hostenv.Inspector backed by the incus daemon this cornus
// drives, so a server running as an instance can confirm which instance it is.
//
// It resolves the socket and project exactly as New does, so self-inspection cannot
// end up asking a different daemon (or a different project) than the deploy path.
func SelfInspector(cfg Config) (hostenv.Inspector, error) {
	cfg = cfg.resolve()
	return func(ctx context.Context, name string) (hostenv.SelfInspect, error) {
		srv, err := incus.ConnectIncusUnix(cfg.Socket, nil)
		if err != nil {
			return hostenv.SelfInspect{}, fmt.Errorf("incus: connecting to daemon at %s: %w", cfg.Socket, err)
		}
		defer srv.Disconnect()
		conn := &realConn{srv: srv.UseProject(cfg.Project)}
		return selfInspect(conn, name)
	}, nil
}

// selfInspect is SelfInspector's testable core, over the same seam the backend uses.
func selfInspect(conn incusConn, name string) (hostenv.SelfInspect, error) {
	inst, err := conn.Instance(name)
	if err != nil {
		return hostenv.SelfInspect{}, err
	}
	if inst == nil {
		return hostenv.SelfInspect{}, fmt.Errorf("incus: no instance named %q", name)
	}
	return hostenv.SelfInspect{
		ID: inst.Name,
		// An instance's hostname is its name, which is what lets confirmSelf accept
		// this candidate when the instance carries no host-path disk device.
		Hostname: inst.Name,
		Mounts:   instanceDiskMounts(inst.ExpandedDevices),
		// NetworkMode is left empty deliberately. An instance does NOT share the
		// host's network namespace — it has its own, on incusd's bridge — so
		// claiming "host" here would be false. What matters for routing is that it
		// is a peer of the workloads, and that is carried by SelfID being set at
		// all (see WithSelfInstance), not by pretending to be the host.
	}, nil
}

// instanceDiskMounts reports the instance's disk devices that correspond to a real
// host path, for confirmSelf to match against our own mount table.
//
// Pool-backed devices (the `root` disk, and every custom storage volume) are
// skipped: their `source` is a volume NAME, not a path, so treating it as one would
// offer confirmSelf a destination whose "host path" is meaningless.
// Sorted by device name, because ExpandedDevices is a map and the result feeds
// hostenv's path map: entries of equal prefix length are resolved in the order they
// arrive, so a map range here would be a mapping that differs between restarts.
// Same defect pickIPv4 had, and cheaper to avoid than to diagnose.
func instanceDiskMounts(devices map[string]map[string]string) []hostenv.MountPoint {
	names := make([]string, 0, len(devices))
	for name := range devices {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []hostenv.MountPoint
	for _, name := range names {
		dev := devices[name]
		if dev["type"] != "disk" {
			continue
		}
		source, path := dev["source"], dev["path"]
		if dev["pool"] != "" || !filepath.IsAbs(source) || !filepath.IsAbs(path) {
			continue
		}
		out = append(out, hostenv.MountPoint{Source: source, Destination: path})
	}
	return out
}
