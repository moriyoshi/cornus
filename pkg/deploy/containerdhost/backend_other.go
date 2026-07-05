//go:build !linux

package containerdhost

import (
	"errors"

	"cornus/pkg/deploy"
	"cornus/pkg/hostenv"
)

// ErrUnsupported is returned by New on platforms without containerd support.
var ErrUnsupported = errors.New("containerd: the containerd deploy backend requires linux")

// New is unsupported on this platform.
func New(cfg Config, opts ...Option) (deploy.Backend, error) {
	return nil, ErrUnsupported
}

// SelfInspector is unsupported on this platform. pkg/server calls it
// unconditionally (it is not build-tagged) and already treats a construction
// failure as "detection has less to go on" rather than an error, so returning
// ErrUnsupported here leaves the preflight exactly as it was before
// self-inspection existed.
func SelfInspector(cfg Config) (hostenv.Inspector, error) {
	return nil, ErrUnsupported
}

// SelfIDCandidates is empty on this platform: the cgroup layout it reads is
// linux's.
func SelfIDCandidates() []string { return nil }
