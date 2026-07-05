//go:build linux

package hostrun

import "golang.org/x/sys/unix"

// NetnsAlive reports whether path is a live pinned network namespace: the bind
// target must exist and still be backed by an nsfs mount. After a host reboot the
// pin is gone entirely; after a manual unmount the path is a leftover empty
// regular file — both count as dead. Shared by the host backends' netns-repair /
// reboot-recovery passes.
func NetnsAlive(path string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return false
	}
	return st.Type == unix.NSFS_MAGIC
}
