//go:build unix

package composecli

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize invokes onResize on every terminal window-size change (SIGWINCH)
// until the returned stop function is called. It feeds execdrive's resize
// forwarding for `cornus compose exec`, mirroring the identical watcher package
// main injects into `cornus exec`.
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
