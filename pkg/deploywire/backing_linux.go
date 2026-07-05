//go:build linux

package deploywire

import "golang.org/x/sys/unix"

// Mount9P kernel-9p-mounts the backing unix socket at target. Options mirror the
// proven build-path mount (trans=unix so the "device" is the unix socket path,
// msize=1MiB, 9p2000.L). readOnly is honored; Pass 1 always mounts read-only.
// This is used both by the server-side MountManager (dockerhost) and by the pod
// mount-agent sidecar (kubernetes) — the mount happens in whatever mount
// namespace the caller runs in, never forced onto a node host by this code.
func Mount9P(sock, target string, readOnly, writeback bool) error {
	// writeback selects cache=mmap: "read-ahead + writeback file cache" (kernel
	// 9p.rst), the documented mode that buffers container writes in the page cache
	// and flushes them asynchronously via writeback — exactly what the writable
	// block-proxy mount wants. It is NOT cache=loose (0b1111): loose also caches
	// meta-data non-coherently and is documented only for exclusive, read-only
	// mounts where the server never modifies the fs; using it for a writable,
	// server-authoritative mount desyncs the kernel's size/mtime and wedges
	// syncfs/writeback in an unrecoverable D state. The default (cache=none) keeps
	// every read/write a synchronous 9P round trip.
	opts := "trans=unix,version=9p2000.L,msize=1048576"
	if writeback {
		opts += ",cache=mmap"
	}
	var flags uintptr
	if readOnly {
		flags = unix.MS_RDONLY
	}
	return unix.Mount(sock, target, "9p", flags, opts)
}

// Unmount9P unmounts target, falling back to a lazy detach if it is busy.
func Unmount9P(target string) {
	if err := unix.Unmount(target, 0); err != nil {
		_ = unix.Unmount(target, unix.MNT_DETACH)
	}
}

func kernelMount9P(sock, mountpoint string, readOnly, writeback bool) error {
	return Mount9P(sock, mountpoint, readOnly, writeback)
}
func unmount9P(mountpoint string) { Unmount9P(mountpoint) }
