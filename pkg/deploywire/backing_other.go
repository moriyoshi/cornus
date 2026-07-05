//go:build !linux

package deploywire

import "errors"

// Mount9P is unsupported off Linux; client-local mounts require kernel-9p.
func Mount9P(sock, target string, readOnly, writeback bool) error {
	return errors.New("deploywire: client-local mounts require Linux (kernel 9p)")
}

// Unmount9P is a no-op off Linux.
func Unmount9P(target string) {}

func kernelMount9P(sock, mountpoint string, readOnly, writeback bool) error {
	return Mount9P(sock, mountpoint, readOnly, writeback)
}
func unmount9P(mountpoint string) {}
