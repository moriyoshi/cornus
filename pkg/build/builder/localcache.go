package builder

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/distribution/reference"
)

// localCacheRoot is the directory under the engine's data Root that holds
// managed `type=local` build caches, one subdirectory per key.
func localCacheRoot(engineRoot string) string {
	return filepath.Join(engineRoot, "localcache")
}

// resolveLocalCacheOpts rewrites `type=local` cache options so their caller-supplied
// dest/src value is treated as an opaque *key* into engine-managed storage under
// root, rather than an arbitrary filesystem path. This lets a caller (local or
// remote build) name a cache without knowing the build engine's on-disk layout.
//
// attr is the key-bearing attr for the direction: "dest" for exports
// (--cache-to), "src" for imports (--cache-from). When it is absent, the key is
// derived from the target image's repository path, so exports and imports for the
// same image share a directory and round-trip without an explicit name.
//
// Non-local options and all other attrs (e.g. mode=max) pass through untouched.
// The input options and their Attrs maps are never mutated; changed entries are
// returned as clones.
func resolveLocalCacheOpts(opts []CacheOption, root, target, attr string) ([]CacheOption, error) {
	if len(opts) == 0 {
		return opts, nil
	}
	out := make([]CacheOption, len(opts))
	for i, o := range opts {
		if o.Type != "local" {
			out[i] = o
			continue
		}
		key := o.Attrs[attr]
		if key == "" {
			derived, err := deriveCacheKey(target)
			if err != nil {
				return nil, fmt.Errorf("local cache: no %q key and %w", attr, err)
			}
			key = derived
		}
		dir, err := safeCacheDir(root, key)
		if err != nil {
			return nil, fmt.Errorf("local cache: %w", err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("local cache: create %s: %w", dir, err)
		}
		attrs := make(map[string]string, len(o.Attrs)+1)
		for k, v := range o.Attrs {
			attrs[k] = v
		}
		attrs[attr] = dir
		out[i] = CacheOption{Type: o.Type, Attrs: attrs}
	}
	return out, nil
}

// PruneLocalCache removes stale managed `type=local` build-cache entries under
// <root>/localcache, reclaiming disk left behind by builds that named a cache but
// never come back. root is the engine's data Root (typically config.CacheDir());
// the localcache tree is plain files, so this is safe to call whether or not a
// BuildKit engine is initialised — the server prunes it without constructing the
// (privileged) engine.
//
// Policy: each immediate child of the localcache root is one prune unit and is
// removed (RemoveAll) only when the newest modification anywhere in its subtree
// predates now-olderThan. Using the subtree's *newest* mtime means a fresh cache
// is never deleted even when it shares a path prefix with a stale sibling (cache
// keys are repository paths and may nest, e.g. localcache/team/app and
// localcache/team/web) — the safe direction is to retain, not over-delete.
//
// A missing localcache dir is not an error (returns 0). Deletion never steps
// outside the localcache root. freed is the number of top-level entries removed;
// a non-nil error reports the first read/remove failure but does not stop the
// sweep of the remaining entries.
func PruneLocalCache(root string, olderThan time.Duration) (freed int, err error) {
	if olderThan <= 0 {
		return 0, fmt.Errorf("local cache prune: olderThan must be positive, got %s", olderThan)
	}
	base := localCacheRoot(root)
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("local cache prune: %w", err)
	}
	cutoff := time.Now().Add(-olderThan)
	var firstErr error
	for _, e := range entries {
		child := filepath.Join(base, e.Name())
		// Defense in depth: never step outside the localcache root.
		if child != base && !isUnder(base, child) {
			continue
		}
		newest, nerr := newestModTime(child)
		if nerr != nil {
			if firstErr == nil {
				firstErr = nerr
			}
			continue
		}
		if newest.Before(cutoff) {
			if rerr := os.RemoveAll(child); rerr != nil {
				if firstErr == nil {
					firstErr = rerr
				}
				continue
			}
			freed++
		}
	}
	return freed, firstErr
}

// newestModTime returns the most recent modification time of path or anything in
// its subtree (path may itself be a file or a directory).
func newestModTime(path string) (time.Time, error) {
	var newest time.Time
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest, err
}

// deriveCacheKey turns a target image reference into a cache key: its repository
// path without the registry host or tag/digest (e.g. "localhost:5000/team/app:v1"
// -> "team/app"). Used when a `type=local` cache omits an explicit key.
func deriveCacheKey(target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("target image reference is empty")
	}
	named, err := reference.ParseNormalizedNamed(target)
	if err != nil {
		return "", fmt.Errorf("cannot derive key from target %q: %w", target, err)
	}
	return reference.Path(named), nil
}

// safeCacheDir joins key onto root while confining the result under root: the
// "/"+Clean idiom collapses any ".." or absolute components so a key cannot escape
// the cache root. It rejects empty keys and, as defense in depth, any result that
// is not actually under root.
func safeCacheDir(root, key string) (string, error) {
	clean := filepath.Clean("/" + key)
	if clean == "/" {
		return "", fmt.Errorf("empty cache key")
	}
	dir := filepath.Join(root, clean)
	if dir != root && !isUnder(root, dir) {
		return "", fmt.Errorf("cache key %q escapes cache root", key)
	}
	return dir, nil
}

// isUnder reports whether dir is root or a descendant of it.
func isUnder(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	return rel != ".." && !hasDotDotPrefix(rel)
}

// hasDotDotPrefix reports whether rel starts with a ".." path component.
func hasDotDotPrefix(rel string) bool {
	return rel == ".." || (len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && os.IsPathSeparator(rel[2]))
}
