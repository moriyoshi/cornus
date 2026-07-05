//go:build !imbh

package obsstore

// Compiled reports that the observability store is NOT linked into this build
// (built without the `imbh` tag).
func Compiled() bool { return false }

// Open returns ErrNotCompiled: the store was not linked into this build. The
// signature matches the real implementation so callers compile unconditionally;
// the server logs the absence and skips the routes and the log recorder rather
// than silently recording nothing.
func Open(Config) (Store, error) { return nil, ErrNotCompiled }
