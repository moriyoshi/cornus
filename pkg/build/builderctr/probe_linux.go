//go:build linux

package builderctr

import (
	"os"
	"syscall"
)

// CanMount reports whether this process may call mount(2) — the capability the
// in-process BuildKit engine actually needs, since it mounts every snapshot it
// reads or executes.
//
// It probes the real syscall rather than inferring from euid, because privilege
// is not the same question as uid: a process can be root yet blocked (seccomp, a
// restrictive container), or non-root yet capable (CAP_SYS_ADMIN, or root inside
// a user namespace, which is exactly how rootless BuildKit works). A bind mount
// of a temp dir onto itself is the cheapest faithful probe; it is undone
// immediately and leaves nothing behind.
func CanMount() bool {
	dir, err := os.MkdirTemp("", "cornus-mountprobe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)

	if err := syscall.Mount(dir, dir, "", syscall.MS_BIND, ""); err != nil {
		return false
	}
	// Best effort: if the unmount somehow fails the dir stays mounted, so do not
	// remove it out from under the kernel — RemoveAll above simply fails then.
	_ = syscall.Unmount(dir, 0)
	return true
}
