package deploy

import "context"

// CrossNamespaceMounter is an optional Backend capability: the runtime consumes
// client-local mounts from a DIFFERENT mount namespace than the one this server
// mounts in.
//
// It exists because the obvious precondition — "refuse when the mounts directory
// does not have shared propagation" — is wrong for most backends. Rootful docker
// works perfectly well with private propagation, because its daemon shares this
// server's mount namespace and no propagation step is involved at all. Refusing
// there would break a working configuration.
//
// What decides it is whether the mount has to CROSS a namespace boundary to reach
// the runtime. It does for rootless podman, whose containers live in a mount
// namespace held by its pause process; it does not for a daemon co-located with
// this server. Only when it crosses does propagation become load-bearing, and only
// then is a non-shared mounts directory a defect rather than a detail.
//
// Not implementing it means the runtime shares this server's mount namespace,
// which is true of rootful dockerhost, containerd and bare. The kubernetes backend
// does not implement it either, for a different reason: a caretaker performs the
// mount INSIDE the pod's own namespaces, so nothing has to propagate across.
type CrossNamespaceMounter interface {
	Backend
	// MountsCrossNamespace reports whether this runtime reaches client-local
	// mounts from a different mount namespace than this server's.
	//
	// It takes a context because the answer can require asking the daemon: podman
	// is rootless or not depending on the socket this server was pointed at, which
	// is a property of the connection rather than of the binary.
	MountsCrossNamespace(ctx context.Context) bool
}
