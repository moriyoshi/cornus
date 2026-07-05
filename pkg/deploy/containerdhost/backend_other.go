//go:build !linux

package containerdhost

import (
	"errors"

	"cornus/pkg/deploy"
)

// ErrUnsupported is returned by New on platforms without containerd support.
var ErrUnsupported = errors.New("containerd: the containerd deploy backend requires linux")

// New is unsupported on this platform.
func New(cfg Config, opts ...Option) (deploy.Backend, error) {
	return nil, ErrUnsupported
}
