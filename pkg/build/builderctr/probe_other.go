//go:build !linux

package builderctr

// CanMount reports whether this process may mount(2). The in-process build
// engine is Linux-only (pkg/build/builder is //go:build linux), so elsewhere
// there is no local engine to be capable of running.
func CanMount() bool { return false }
