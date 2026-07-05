//go:build linux

package builder

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sweepPrefixes are the os.MkdirTemp prefixes used by the lazy/9P/ssh backings in
// pkg/build/buildwire (dirserve.go, ninep_backing.go, ssh.go). Those dirs are
// cleaned only by a deferred Close(), so a SIGKILL/crash leaks them on disk. The
// startup sweep reaps the stale ones a previous, dead engine left behind.
var sweepPrefixes = []string{
	"cornus-9p-",     // buildwire/dirserve.go
	"cornus-9pback-", // buildwire/ninep_backing.go
	"cornus-ssh-",    // buildwire/ssh.go
}

// sweepStaleWindow is how recently a temp dir may have been modified and still be
// considered "live". The temp backing dirs are NOT lock-guarded (only the engine
// data dir is, via engine.lock), so we cannot tell a concurrently-running peer's
// dirs apart from our own dead leftovers by ownership alone. Instead we gate
// deletion on mtime age: a dir touched within this window is assumed to belong to
// an actively-running engine and is left alone; only older dirs (whose owner has
// almost certainly exited without cleaning up) are reaped. This is deliberately
// pid-free — parsing pids out of dir names would be fragile and the buildwire
// creation code (which this file must not touch) does not embed them.
const sweepStaleWindow = 5 * time.Minute

// sweepStaleTempDirs removes stale lazy/9P/ssh backing dirs under root (normally
// os.TempDir()). It is best-effort: any error is swallowed so a failed sweep can
// never block engine startup. root is a parameter so tests can point it at a fake
// temp root instead of the real /tmp.
func sweepStaleTempDirs(root string) {
	sweepStaleTempDirsAt(root, time.Now())
}

// sweepStaleTempDirsAt is the testable core: now is injected so a test can assert
// the mtime-age boundary deterministically.
func sweepStaleTempDirsAt(root string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return // best-effort: unreadable temp root, nothing to do
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !hasSweepPrefix(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Keep anything modified within the staleness window: it may belong to a
		// concurrently-running cornus whose backing dir is still live.
		if now.Sub(info.ModTime()) < sweepStaleWindow {
			continue
		}
		dir := filepath.Join(root, e.Name())
		// mtime alone is not enough: buildwire/wire create each backing dir and its
		// socket ONCE at build start and never touch the dir again, so a build that
		// runs longer than sweepStaleWindow has a live dir with a stale-looking
		// mtime. Probe the socket for a live listener before reaping — a build still
		// in flight keeps its socket bound, whereas a dead process's leftover socket
		// refuses the connection. Never delete a dir whose socket is still live.
		if dirHasLiveSocket(dir) {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

// dirHasLiveSocket reports whether dir contains a unix socket that a process is
// still listening on. Each buildwire/wire backing dir holds exactly one socket
// created at build start (ssh: <id>.sock, 9p/9pback: ctx.sock) and held open for
// the whole build. A dial that connects proves the owning build is alive; a
// socket left behind by a dead process refuses the connection (ECONNREFUSED).
// This is the liveness signal the directory mtime cannot provide, since the
// socket is created once and never re-touched. Best-effort: any read/dial error
// is treated as "not live".
func dirHasLiveSocket(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type()&os.ModeSocket == 0 {
			continue
		}
		c, err := net.DialTimeout("unix", filepath.Join(dir, e.Name()), 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
	}
	return false
}

func hasSweepPrefix(name string) bool {
	for _, p := range sweepPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
