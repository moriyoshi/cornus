// Package lazyctx builds the artifacts that let a large `RUN --mount=type=bind`
// context be served to a remote build lazily (read on demand) instead of eagerly
// copied into a snapshot. It produces a content-identity manifest of a directory
// subtree (the cache key) and the synthetic OCI image whose single layer the
// cornus remote snapshotter mounts from a 9p view. See
// .agents/docs/LTM/lazy-bind-mounts.md.
package lazyctx

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencontainers/go-digest"
)

// Entry is one path's identity in a metadata manifest: everything except the file
// contents. Under mtime/size trust (the same trust BuildKit's local incremental
// sync already relies on) this identifies the file without reading it — which is
// the whole point, since reading contents is what we are avoiding.
type Entry struct {
	Path     string // slash-separated, relative to the subtree root
	Mode     uint32 // os.FileMode bits
	Size     int64  // 0 for non-regular files
	ModTime  int64  // unix nanoseconds
	Linkname string // symlink target, else ""
}

// Manifest is a deterministic description of a directory subtree used as the
// content-addressed cache key for a lazy bind context. Entries are sorted by Path.
type Manifest struct {
	Entries []Entry
}

// Ignore reports whether a slash-separated relative path is excluded (e.g. a
// .dockerignore matcher). May be nil.
type Ignore func(rel string) bool

// Build walks root and records each path's metadata (honoring ignore), producing
// a deterministic manifest. It never opens a file, so it is cheap on a large
// tree. Directories are recorded (so an empty dir affects identity); symlinks are
// recorded by target, not followed.
func Build(root string, ignore Ignore) (*Manifest, error) {
	var entries []Entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil // the root itself is not an entry
		}
		if ignore != nil && ignore(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := Entry{
			Path:    rel,
			Mode:    uint32(info.Mode()),
			ModTime: info.ModTime().UnixNano(),
		}
		if info.Mode().IsRegular() {
			e.Size = info.Size()
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if target, lerr := os.Readlink(p); lerr == nil {
				e.Linkname = target
			}
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return &Manifest{Entries: entries}, nil
}

// Bytes returns the canonical serialization the digest is taken over: one
// NUL-delimited record per entry, newline-terminated, in Path order. A fixed
// field order and delimiter keep it stable across Go versions and platforms.
func (m *Manifest) Bytes() []byte {
	var b strings.Builder
	for _, e := range m.Entries {
		fmt.Fprintf(&b, "%s\x00%d\x00%d\x00%d\x00%s\n", e.Path, e.Mode, e.Size, e.ModTime, e.Linkname)
	}
	return []byte(b.String())
}

// Digest is the content-identity of the subtree: sha256 of the canonical bytes.
// Equal digest => (under mtime/size trust) equal tree => RUN cache hit.
func (m *Manifest) Digest() digest.Digest {
	sum := sha256.Sum256(m.Bytes())
	return digest.NewDigestFromBytes(digest.SHA256, sum[:])
}
