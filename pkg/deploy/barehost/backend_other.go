//go:build !linux

package barehost

import (
	"errors"

	"cornus/pkg/deploy"
)

// ErrUnsupported is returned by New on platforms without OCI-runtime support.
var ErrUnsupported = errors.New("bare: the bare-metal OCI-runtime deploy backend requires linux")

// New is unsupported on this platform.
func New(cfg Config, opts ...Option) (deploy.Backend, error) {
	return nil, ErrUnsupported
}

// ShimConfig mirrors the linux type so cmd/cornus/bareshim.go compiles on all
// platforms; RunShim is unsupported off linux.
type ShimConfig struct {
	ID      string
	DataDir string
	Runtime string
	Systemd bool
}

// RunShim is unsupported on this platform.
func RunShim(cfg ShimConfig) error {
	return ErrUnsupported
}
