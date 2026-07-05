//go:build linux

package incushost

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

	"cornus/pkg/api"
)

// Device mapping for an Incus application container.
//
// Everything cornus attaches to an instance beyond its root disk is an Incus
// DEVICE: a published port is a `proxy` device, and every filesystem this
// backend puts in front of the workload — the remote-mode agent volume, a
// server-host bind, a tmpfs, /dev/shm — is a `disk` device. The disk shapes are
// all built by the one function in incusd that turns a disk device into a mount,
// internal/server/device/disk.go:
//
//   - a NON-pool `source` is a plain host bind: disk.go:1109-1175 assembles
//     `ro` (from `readonly`), `bind`/`rbind` (from `recursive`) and `propagation`
//     into the mount options and hands the result to the instance;
//   - `source: "tmpfs:"` is a tmpfs: disk.go:954-1064 turns `size` into the
//     mount's `size=` option;
//   - a `pool` + volume-name `source` is a custom storage volume, which is what
//     agentVolumeDeviceFor already builds.
//
// The container-side path of every disk device must be unique: incusd counts
// the instance's own disk devices per path and rejects the whole create on a
// collision ("More than one disk device uses the same path",
// disk.go:485-493). buildDevices therefore claims paths as it goes and drops
// (loudly) a later device that would collide, rather than letting a duplicate
// path in a compose file turn into a failed deploy.

// buildDevices renders replica i's device map: published ports (replica 0 only,
// per the cross-backend one-DNAT-target-per-host-port contract), the remote-mode
// shared agent volume, server-host bind mounts, tmpfs mounts and /dev/shm.
// Entries an Incus disk device cannot express are warned about per entry, never
// dropped in silence. Returns nil (not an empty map) when there is nothing to
// attach.
func (b *Backend) buildDevices(ctx context.Context, log *slog.Logger, spec api.DeploySpec, i int) map[string]map[string]string {
	devices := map[string]map[string]string{}
	// Container path -> the device name that owns it.
	claimed := map[string]string{}
	claim := func(dev map[string]string, name string) bool {
		path := dev["path"]
		if prev, taken := claimed[path]; taken {
			log.WarnContext(ctx, "backend ignores a second filesystem on the same container path: incus rejects a create in which two disk devices share a path, so the first one wins",
				"path", path, "kept", prev, "dropped", name)
			return false
		}
		claimed[path] = name
		devices[name] = dev
		return true
	}

	// Published ports on replica 0 only.
	if i == 0 {
		for pi, pm := range spec.Ports {
			dev, name := proxyDevice(pi, pm)
			if dev != nil {
				devices[name] = dev
			}
		}
	}
	// In remote mode the app instance mounts the same shared agent volume its
	// companion does, at the same path. That is what puts the companion's
	// agent-relay socket inside the workload, so the SSH_AUTH_SOCK the server
	// injects into an exec (remotecompanion.AgentSocketPath) resolves to a socket
	// something is actually listening on. Every replica gets its own volume: one
	// shared across replicas would let a forwarded agent reach the wrong instance.
	//
	// It is claimed FIRST so a compose file that happens to name the agent scratch
	// directory loses to it: ssh-agent forwarding breaking silently is worse than
	// one dropped mount, and the drop is warned about either way.
	if b.remote {
		claim(b.agentVolumeDeviceFor(spec.Name, i), agentVolumeDevice)
	}
	// Server-host bind mounts (compose `volumes:` with a host source). By the time
	// a Mount reaches this backend it is always a SERVER-HOST path: the
	// client-local flavor never gets here, because the server refuses it for a
	// backend that is neither a MountingBackend nor a co-located host backend
	// (pkg/server/deploy_attach.go, "client-local mounts are not supported by the
	// %s backend"). The source has already been gated by hostpolicy in Apply.
	for mi, m := range spec.Mounts {
		if dev, name, ok := bindDevice(ctx, log, mi, m); ok {
			claim(dev, name)
		}
	}
	// Managed volumes (compose `volumes:` with no host source) — provisioned
	// storage rather than an existing path. See volumes_linux.go.
	b.volumeDevices(ctx, log, spec, i, claim)
	// tmpfs mounts (compose `tmpfs:`).
	for ti, t := range spec.Tmpfs {
		if dev, name, ok := tmpfsDevice(ctx, log, ti, t); ok {
			claim(dev, name)
		}
	}
	// /dev/shm sizing (compose `shm_size`) — the same tmpfs disk device, at the
	// path the kernel's POSIX shared memory lives on. Mounting into /dev works
	// because a disk device's mount is emitted as an ordinary lxc.mount.entry
	// (driver_lxc.go:2260-2290), the same mechanism incus's own unix-char devices
	// use to place a node under /dev. It also WINS over an image-declared
	// /dev/shm: the application-container branch collects the mount targets
	// already configured and skips any tmpfs the image asks for on one of them
	// (driver_lxc.go:2447-2476).
	if spec.ShmSize > 0 {
		claim(map[string]string{
			"type":   "disk",
			"source": diskSourceTmpfs,
			"path":   "/dev/shm",
			"size":   strconv.FormatInt(spec.ShmSize, 10),
		}, "cornus-shm")
	}

	if len(devices) == 0 {
		return nil
	}
	return devices
}

// diskSourceTmpfs is the magic `source` value that makes an Incus disk device a
// tmpfs rather than a bind or a pool volume (internal/server/device/disk.go:52,
// matched at disk.go:954 and again in validateConfig at disk.go:456 and :475).
const diskSourceTmpfs = "tmpfs:"

// bindDevice renders one server-host bind mount as an Incus disk device,
// returning ok=false (having warned) for a mount Incus cannot express.
//
// The gates are incusd's own, so a device this returns is a device the create
// cannot be rejected over: a local source must be absolute ("Source path must be
// absolute for local sources", disk.go:495-501), and `path` may not be "/",
// which names the ROOT disk and must then carry a pool and no source at all
// (disk.go:465-471).
func bindDevice(ctx context.Context, log *slog.Logger, idx int, m api.Mount) (map[string]string, string, bool) {
	name := fmt.Sprintf("cornus-mount-%d", idx)
	if strings.TrimSpace(m.Source) == "" {
		// An empty source is a managed-volume reference that never got routed to
		// spec.Volumes (hostpolicy treats it the same way). Incus needs either a
		// host path or a storage pool to build a disk device from.
		log.WarnContext(ctx, "backend ignores a mount with no source: an empty source names a managed volume rather than a host path, and an incus disk device needs a host source or a storage pool; nothing is mounted at the target",
			"target", m.Target)
		return nil, "", false
	}
	if !filepath.IsAbs(m.Source) {
		log.WarnContext(ctx, "backend ignores a mount with a relative source: incus requires an absolute path for a local disk source, and a relative one would resolve against the daemon's working directory; nothing is mounted at the target",
			"source", m.Source, "target", m.Target)
		return nil, "", false
	}
	target := filepath.Clean(m.Target)
	if !filepath.IsAbs(target) || target == "/" {
		log.WarnContext(ctx, "backend ignores a mount without an absolute non-root target: an incus disk path must be absolute, and \"/\" names the instance's ROOT disk, which may carry no source; nothing is mounted",
			"source", m.Source, "target", m.Target)
		return nil, "", false
	}
	if m.SELinux != "" {
		log.WarnContext(ctx, "backend ignores the SELinux relabel requested on a mount: an incus disk device has no relabel option; the path is bind-mounted with its existing labels",
			"target", m.Target, "selinux", m.SELinux)
	}
	dev := map[string]string{
		"type":   "disk",
		"source": m.Source,
		"path":   target,
	}
	if m.ReadOnly {
		// disk.go:1109-1111 turns this into the mount's "ro" option. It is left
		// absent rather than written as "false" so the device carries only what was
		// asked for.
		dev["readonly"] = "true"
	}
	return dev, name, true
}

// tmpfsDevice renders one compose `tmpfs:` entry ("path" or "path:opt,opt") as
// an Incus tmpfs disk device.
//
// Incus expresses exactly one of the mount options a tmpfs can take: `size`
// (disk.go:960-968). Every other option is dropped with a warning — the same
// call the kubernetes backend makes for an emptyDir — rather than silently, and
// rather than refusing the mount, because a tmpfs at the right path with kernel
// default options is much closer to what was asked for than no tmpfs at all.
func tmpfsDevice(ctx context.Context, log *slog.Logger, idx int, entry string) (map[string]string, string, bool) {
	name := fmt.Sprintf("cornus-tmpfs-%d", idx)
	rawPath, opts, _ := strings.Cut(entry, ":")
	path := filepath.Clean(rawPath)
	if !filepath.IsAbs(path) || path == "/" {
		log.WarnContext(ctx, "backend ignores a tmpfs entry without an absolute non-root path: an incus disk path must be absolute, and \"/\" names the instance's ROOT disk; no tmpfs is mounted",
			"tmpfs", entry)
		return nil, "", false
	}
	dev := map[string]string{
		"type":   "disk",
		"source": diskSourceTmpfs,
		"path":   path,
	}
	size, dropped := tmpfsSize(opts)
	if size != "" {
		dev["size"] = size
	}
	if len(dropped) > 0 {
		log.WarnContext(ctx, "backend ignores tmpfs mount options it cannot express: an incus tmpfs disk device takes a size and nothing else; the tmpfs is mounted with the kernel defaults for these",
			"tmpfs", entry, "dropped", strings.Join(dropped, ","))
	}
	return dev, name, true
}

// tmpfsSize splits a compose tmpfs option string into the byte count Incus's
// `size` property takes and the options that have no Incus equivalent.
//
// The returned size is a plain decimal byte count, which is what
// units.ParseByteSizeString — the parser behind the `size` property's IsSize
// validator (shared/validate/validate.go:218-225, shared/units/units.go:23-57) —
// reads as a suffix-less value with a multiplier of 1. Rendering it ourselves
// avoids handing incus a mount(8)-flavoured suffix ("64m") that its own unit
// table does not contain.
func tmpfsSize(opts string) (size string, dropped []string) {
	for _, opt := range strings.Split(opts, ",") {
		opt = strings.TrimSpace(opt)
		if opt == "" {
			continue
		}
		k, v, _ := strings.Cut(opt, "=")
		if k != "size" {
			dropped = append(dropped, opt)
			continue
		}
		n, ok := parseTmpfsBytes(v)
		if !ok {
			// An unparseable size is reported as dropped rather than guessed at: the
			// tmpfs still gets mounted, with the kernel's default of half of RAM.
			dropped = append(dropped, opt)
			continue
		}
		size = strconv.FormatInt(n, 10)
	}
	return size, dropped
}

// parseTmpfsBytes parses a tmpfs `size=` value in the spelling mount(8) accepts
// — a byte count with an optional binary k/m/g suffix. Anything else (notably
// the "%" of-RAM form, which has no fixed byte count to give Incus) is refused.
func parseTmpfsBytes(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	mult := int64(1)
	switch v[len(v)-1] {
	case 'k', 'K':
		mult = 1 << 10
	case 'm', 'M':
		mult = 1 << 20
	case 'g', 'G':
		mult = 1 << 30
	}
	digits := v
	if mult != 1 {
		digits = v[:len(v)-1]
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	// Refuse a value that would wrap; incus stores the byte count as an int64 too.
	if mult > 1 && n > (1<<62)/mult {
		return 0, false
	}
	return n * mult, true
}
