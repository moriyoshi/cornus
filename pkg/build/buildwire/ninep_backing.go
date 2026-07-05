package buildwire

import (
	"path/filepath"

	"github.com/moby/patternmatcher"
	"github.com/opencontainers/go-digest"

	"cornus/pkg/blockcache"
	"cornus/pkg/build/internal/lazyctx"
	"cornus/pkg/wire"
)

// ignoreFunc adapts a .dockerignore matcher to lazyctx.Ignore, matching the
// confinedfs export's rule exactly (the .dockerignore file itself is kept) so the
// digested/manifested set is identical to what is served over 9P.
func ignoreFunc(m *patternmatcher.PatternMatcher) lazyctx.Ignore {
	if m == nil {
		return nil
	}
	return func(rel string) bool {
		if rel == "" || rel == ".dockerignore" {
			return false
		}
		matched, err := m.MatchesOrParentMatches(filepath.FromSlash(rel))
		return err == nil && matched
	}
}

// LazyBackings builds the lazy contexts declared in the build spec: for each, a
// 9P backing socket (proxying on-demand reads to the caller's export over the
// session) and a synthetic oci-layout image whose contenthash is seeded from the
// caller's precomputed digests. Returns the contexts, a cleanup func, and an
// error. The server passes the contexts to engine.Solve as SolveInput.LazyContexts.
//
// When cache is non-nil the on-demand reads are served through the server-side
// block cache: a build context is immutable for the build's lifetime, so caching
// its chunks is safe and lets repeated builds of the same context skip re-pulling
// bytes over the wire.
func (s *ServerSession) LazyBackings(cache *blockcache.Cache) ([]*lazyctx.LazyContext, func(), error) {
	var cleanups []func()
	cleanup := func() {
		for _, c := range cleanups {
			c()
		}
	}
	out := make([]*lazyctx.LazyContext, 0, len(s.Spec.LazyContexts))
	for _, ls := range s.Spec.LazyContexts {
		sock, cl, err := wire.Backing9PSocketCached(s.sess, ls.Name, nil, nil, cache)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		cleanups = append(cleanups, cl)
		// A 9P lazy backing is per-build: its socket is served over THIS build's
		// client session and is torn down when the build ends. BuildKit, however,
		// content-addresses the synthetic lazy layer by its digest and caches the
		// committed remote snapshot across builds — so a second build of an
		// IDENTICAL context (same LayerDigest) would reuse the first build's
		// snapshot and try to mount its already-closed socket (ENOENT at the COPY /
		// RUN --mount step). Fold this build's unique socket path into the synthetic
		// layer digest so every build gets its own snapshot bound to a LIVE socket.
		// This intentionally forgoes BuildKit's cross-build reuse of the lazy layer
		// (which was never valid for a per-build socket anyway); byte-level reuse
		// across builds still comes from the server-side block cache, which keys on
		// file identity (mount/path/size/mtime), not this digest.
		perBuildDigest := digest.FromString(ls.LayerDigest + "\x00" + sock)
		lc, err := lazyctx.FromRemote(ls.Name, perBuildDigest, ls.LayerSize, "9p:"+sock, ls.Digests)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		out = append(out, lc)
	}
	return out, cleanup, nil
}
