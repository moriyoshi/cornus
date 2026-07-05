//go:build !linux

package builder

import (
	"context"
	"errors"
	"time"

	"cornus/pkg/build/buildprog"
)

// ErrUnsupported is returned by the build engine on non-Linux platforms, where
// the in-process BuildKit solver (runc executor, overlayfs) is unavailable.
var ErrUnsupported = errors.New("builder: in-process BuildKit solver is only supported on Linux")

// Engine is a non-functional placeholder on non-Linux platforms.
type Engine struct{}

// New returns ErrUnsupported on non-Linux platforms.
func New(cfg Config) (*Engine, error) { return nil, ErrUnsupported }

// Close is a no-op.
func (e *Engine) Close() error { return nil }

// PruneLocalCache returns ErrUnsupported on non-Linux platforms. The package-level
// PruneLocalCache free function is cross-platform; the server uses that so it can
// reclaim the localcache dir without an engine.
func (e *Engine) PruneLocalCache(olderThan time.Duration) (int, error) { return 0, ErrUnsupported }

// Build returns ErrUnsupported on non-Linux platforms.
func (e *Engine) Build(ctx context.Context, req Request, progress buildprog.Sink) (*Result, error) {
	return nil, ErrUnsupported
}

// Solve returns ErrUnsupported on non-Linux platforms.
func (e *Engine) Solve(ctx context.Context, in SolveInput, progress buildprog.Sink) (*Result, error) {
	return nil, ErrUnsupported
}
