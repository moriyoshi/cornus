//go:build !linux

package incushost

import (
	"errors"

	"cornus/pkg/deploy"
	"cornus/pkg/hostenv"
)

// ErrUnsupported is returned by New on platforms without Incus support.
var ErrUnsupported = errors.New("incus: the incus deploy backend requires linux")

// New is unsupported on this platform.
func New(cfg Config, opts ...Option) (deploy.Backend, error) {
	return nil, ErrUnsupported
}

// SelfInspector is unsupported on this platform. pkg/server calls it
// unconditionally and already treats a construction failure as "detection has less
// to go on", so this leaves the preflight exactly as it was.
func SelfInspector(cfg Config) (hostenv.Inspector, error) {
	return nil, ErrUnsupported
}

// SelfIDCandidates is empty on this platform: there are no incus instances here.
func SelfIDCandidates() []string { return nil }
