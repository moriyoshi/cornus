//go:build !linux

package incushost

import (
	"errors"

	"cornus/pkg/deploy"
)

// ErrUnsupported is returned by New on platforms without Incus support.
var ErrUnsupported = errors.New("incus: the incus deploy backend requires linux")

// New is unsupported on this platform.
func New(cfg Config, opts ...Option) (deploy.Backend, error) {
	return nil, ErrUnsupported
}
