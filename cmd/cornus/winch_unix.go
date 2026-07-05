//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize invokes onResize on every terminal window-size change
// (SIGWINCH) until the returned stop function is called.
func notifyResize(onResize func()) (stop func()) {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			onResize()
		}
	}()
	return func() {
		signal.Stop(winch)
		close(winch)
	}
}
