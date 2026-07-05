package dockerhost

import "cornus/pkg/deploy/hostpolicy"

// Policy gates the host-privilege surface a DeploySpec may request. The logic
// is shared with other host-level backends in pkg/deploy/hostpolicy; the zero
// value is default-deny (no privileged containers, no host bind mounts).
type Policy = hostpolicy.Policy

// PermissivePolicy allows privileged containers and any absolute bind source.
// It is for the local `cornus deploy` CLI, where the caller already has direct
// Docker access on their own host, so gating adds friction with no security gain.
func PermissivePolicy() Policy { return hostpolicy.Permissive() }

// PolicyFromEnv builds a Policy from CORNUS_ALLOW_PRIVILEGED and
// CORNUS_ALLOW_BIND_SOURCES (see hostpolicy.FromEnv).
func PolicyFromEnv() Policy { return hostpolicy.FromEnv() }
