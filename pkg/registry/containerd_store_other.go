//go:build !linux

package registry

import "errors"

// NewContainerdStore returns an error on non-linux platforms: the containerd
// deploy backend (and thus host-native re-export of its store) is linux-only.
func NewContainerdStore(address, namespace, stagingDir string) (Store, error) {
	return nil, errors.New("registry: the containerd store requires linux")
}
