//go:build windows

package composecli

// notifyResize is a no-op on windows, which has no SIGWINCH-style terminal
// resize signal. Mirrors package main's windows stub for `cornus exec`.
func notifyResize(func()) (stop func()) { return func() {} }
