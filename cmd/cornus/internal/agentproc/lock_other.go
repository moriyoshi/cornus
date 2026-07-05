//go:build !unix

package agentproc

// withLock has no flock on non-unix hosts; spawns are best-effort serialized by
// the ping re-check. The agent is a unix-only feature (daemonize is too).
func withLock(_ string, fn func() error) error { return fn() }
