// Package daemonize re-execs the current binary as a detached background
// process. The `cornus daemon *` services run in the foreground by default;
// their -d/--daemon flag uses this package to spawn the same invocation
// (minus the flag) as a session-leader child and return immediately.
package daemonize

import (
	"os"
	"strings"
)

// SelfArgs returns os.Args[1:] with the -d/--daemon flag stripped, so a
// daemonizing command can re-exec itself and have the child run the same
// invocation in the foreground.
func SelfArgs() []string { return stripDaemonFlag(os.Args[1:]) }

func stripDaemonFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "-d" || a == "--daemon" || strings.HasPrefix(a, "--daemon=") {
			continue
		}
		out = append(out, a)
	}
	return out
}
