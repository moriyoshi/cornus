package deploywire

// CanMountLocal reports whether this host can kernel-9p-mount caller-local bind
// mounts: it must be Linux, have the privilege to call mount(2) (CAP_SYS_ADMIN /
// root), and have the 9p filesystem available. The server calls it before
// preparing mounts so the caller sees a clear error instead of a cryptic mount
// failure. It cannot detect a subtler failure — mount propagation to the Docker
// daemon when the cornus server itself runs in a container — which must be
// arranged operationally (see ARCHITECTURE.md "Privilege posture").
func CanMountLocal() error { return canMountLocal() }
