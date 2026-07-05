//go:build windows

package main

// notifyResize is a no-op on windows, which has no SIGWINCH-style terminal
// resize signal; the initial size sent before the stream starts still applies.
func notifyResize(func()) (stop func()) { return func() {} }
