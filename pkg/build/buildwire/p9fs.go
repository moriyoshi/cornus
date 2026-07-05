package buildwire

import (
	"context"
	"errors"
	"fmt"
	"io"
	gofs "io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"github.com/moby/buildkit/session/secrets"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
)

// mapErr maps a p9 "no such file" error to fs.ErrNotExist so callers
// (BuildKit/fsutil probing optional files like .dockerignore) recognize it as a
// missing file rather than a hard error.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var errno linux.Errno
	if errors.As(err, &errno) && errno == linux.ENOENT {
		return gofs.ErrNotExist
	}
	return err
}

// p9FS adapts a subtree of a remote 9P export (served by the build caller) to
// BuildKit's fsutil.FS, so the caller's directory streams over 9P/WebSocket on
// demand. root is the path, from the 9P attach root, that this FS is rooted at
// (e.g. {"context"} or {"ctx","data"}).
type p9FS struct {
	client *p9.Client
	root   []string
}

// node walks to root+rel and returns an independent file fid (caller closes).
func (f *p9FS) node(rel string) (p9.File, error) {
	r, err := f.client.Attach("")
	if err != nil {
		return nil, err
	}
	defer r.Close()
	names := append(append([]string{}, f.root...), splitRel(rel)...)
	_, file, err := r.Walk(names) // nil/empty names returns a clone of root
	return file, mapErr(err)
}

// Open implements fsutil.FS.
func (f *p9FS) Open(name string) (io.ReadCloser, error) {
	file, err := f.node(cleanRel(name))
	if err != nil {
		return nil, err
	}
	if _, _, err := file.Open(p9.ReadOnly); err != nil {
		file.Close()
		return nil, err
	}
	return &p9Reader{f: file}, nil
}

// Walk implements fsutil.FS, enumerating the subtree contents (not the root
// itself), directories before their children, in lexical order.
func (f *p9FS) Walk(ctx context.Context, target string, fn gofs.WalkDirFunc) error {
	return f.walk(cleanRel(target), fn)
}

func (f *p9FS) walk(rel string, fn gofs.WalkDirFunc) error {
	file, err := f.node(rel)
	if err != nil {
		// WalkDir semantics: SkipDir returned for the root means stop, not error.
		if werr := fn(rel, nil, err); werr != nil && werr != gofs.SkipDir {
			return werr
		}
		return nil
	}
	_, _, attr, err := file.GetAttr(p9.AttrMask{Mode: true, Size: true, MTime: true, UID: true, GID: true})
	if err != nil {
		file.Close()
		return fn(rel, nil, err)
	}
	isDir := attr.Mode.IsDir()
	var linkname string
	if attr.Mode.IsSymlink() {
		// Carry the symlink's target so fsutil reconstructs it as a symlink
		// rather than a broken/empty link (BuildKit transmits symlinks via
		// Linkname; it never follows them during the context sync).
		linkname, _ = file.Readlink()
	}
	file.Close()

	if rel != "" { // skip the FS root itself
		// fsutil requires each entry's Info().Sys() to be a *fstypes.Stat.
		stat := &fstypes.Stat{
			Path:     rel,
			Mode:     uint32(osMode(attr.Mode)),
			Uid:      uint32(attr.UID),
			Gid:      uint32(attr.GID),
			Size:     int64(attr.Size),
			ModTime:  time.Unix(int64(attr.MTimeSeconds), int64(attr.MTimeNanoSeconds)).UnixNano(),
			Linkname: linkname,
		}
		if err := fn(rel, &fsutil.DirEntryInfo{Stat: stat}, nil); err != nil {
			if err == gofs.SkipDir {
				return nil
			}
			return err
		}
	}
	if !isDir {
		return nil
	}
	names, err := f.readdir(rel)
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, n := range names {
		if err := f.walk(joinRel(rel, n), fn); err != nil {
			return err
		}
	}
	return nil
}

// maxDirEntries caps how many entries readdir accumulates for one directory
// before giving up. The caller's 9P server is untrusted in a hosted build; this
// bounds memory even if the peer streams an implausibly long listing with
// strictly-advancing offsets (which the offset check alone would not stop).
const maxDirEntries = 1 << 20

func (f *p9FS) readdir(rel string) ([]string, error) {
	file, err := f.node(rel)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if _, _, err := file.Open(p9.ReadOnly); err != nil {
		return nil, err
	}
	var names []string
	var off uint64
	for {
		ents, err := file.Readdir(off, 1<<16)
		if err != nil {
			return nil, err
		}
		if len(ents) == 0 {
			break
		}
		for _, e := range ents {
			if e.Name != "." && e.Name != ".." {
				names = append(names, e.Name)
			}
		}
		if len(names) > maxDirEntries {
			return nil, fmt.Errorf("buildwire: readdir %q: too many entries (>%d)", rel, maxDirEntries)
		}
		// The peer's 9P server is untrusted (a hosted build's caller). Require
		// the resume cookie to strictly advance so a fixed or rewinding Offset on
		// a non-empty batch cannot spin this loop forever, growing names without
		// bound and pinning CPU/memory on the shared build host.
		next := ents[len(ents)-1].Offset
		if next <= off {
			return nil, fmt.Errorf("buildwire: readdir %q: non-advancing offset %d", rel, next)
		}
		off = next
	}
	return names, nil
}

// p9Reader streams a p9 file opened read-only.
type p9Reader struct {
	f   p9.File
	off int64
}

func (r *p9Reader) Read(p []byte) (int, error) {
	n, err := r.f.ReadAt(p, r.off)
	r.off += int64(n)
	return n, err
}

func (r *p9Reader) Close() error { return r.f.Close() }

func osMode(m p9.FileMode) gofs.FileMode {
	mode := gofs.FileMode(m.Permissions())
	switch {
	case m.IsDir():
		mode |= gofs.ModeDir
	case m.IsSymlink():
		mode |= gofs.ModeSymlink
	}
	return mode
}

// p9SecretStore reads secret values from the caller's /secrets/<id> 9P tree.
type p9SecretStore struct {
	client *p9.Client
}

// GetSecret implements buildkit's secrets.SecretStore.
func (s p9SecretStore) GetSecret(ctx context.Context, id string) ([]byte, error) {
	r, err := s.client.Attach("")
	if err != nil {
		return nil, err
	}
	defer r.Close()
	_, file, err := r.Walk([]string{"secrets", id})
	if err != nil {
		return nil, fmt.Errorf("%w: secret %s", secrets.ErrNotFound, id)
	}
	defer file.Close()
	if _, _, err := file.Open(p9.ReadOnly); err != nil {
		return nil, err
	}
	return io.ReadAll(&p9Reader{f: file})
}

// --- path helpers -----------------------------------------------------------

func cleanRel(p string) string {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "." {
		return ""
	}
	return p
}

func splitRel(rel string) []string {
	rel = cleanRel(rel)
	if rel == "" {
		return nil
	}
	return strings.Split(rel, "/")
}

func joinRel(base, name string) string {
	if base == "" {
		return name
	}
	return base + "/" + name
}
