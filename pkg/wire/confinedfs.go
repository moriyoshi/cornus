package wire

import (
	"errors"
	gofs "io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/hugelgupf/p9/fsimpl/localfs"
	"github.com/hugelgupf/p9/fsimpl/templatefs"
	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"github.com/moby/patternmatcher"
)

// ConfinedAttacher exports root over 9P read-only and confined to root: a remote
// 9P client (the cornus build server) cannot escape root via ".." components
// or symlinks that point outside it, and every mutating operation is refused.
// When ignore is non-nil, paths it matches (a .dockerignore matcher) are hidden,
// so ignored files never leave the caller's machine.
//
// This is the security boundary of a remote build: the build server is a 9P
// client and may send arbitrary Twalk/Topen/Twrite; localfs alone does not
// confine it (localfs.Local.Walk joins names with no root clamp, and exposes
// Create/WriteAt/UnlinkAt), so the policy must live here, ahead of localfs.
func ConfinedAttacher(root string, ignore *patternmatcher.PatternMatcher) (p9.Attacher, error) {
	return confinedAttacherCounted(root, ignore, nil)
}

// confinedAttacherCounted is ConfinedAttacher with an optional read-byte counter
// (for measuring how much content a build actually pulls).
func confinedAttacherCounted(root string, ignore *patternmatcher.PatternMatcher, reads *atomic.Int64) (p9.Attacher, error) {
	// Resolve symlinks in root once so containment checks compare real paths.
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(real)
	if err != nil {
		return nil, err
	}
	return &confinedAttach{g: &guard{root: abs, ignore: ignore, reads: reads}}, nil
}

// guard holds the policy shared by every node of one confined export.
type guard struct {
	root   string // resolved, absolute export root
	ignore *patternmatcher.PatternMatcher
	reads  *atomic.Int64 // bytes served via ReadAt, for measurement; may be nil
}

// ignored reports whether rel (slash-separated, relative to root) is excluded by
// the .dockerignore matcher. The .dockerignore file itself is always served so
// the build engine can read it too.
func (g *guard) ignored(rel string) bool {
	if g.ignore == nil || rel == "" || rel == ".dockerignore" {
		return false
	}
	m, err := g.ignore.MatchesOrParentMatches(filepath.FromSlash(rel))
	return err == nil && m
}

// confinedFollow returns nil if rel — fully resolved, following a final-component
// symlink — stays within root, else EACCES. Used at Open time: actually reading
// through an escaping symlink to its target is denied. A non-existent leaf under
// an in-root parent is allowed (localfs then reports the real ENOENT).
func (g *guard) confinedFollow(rel string) error {
	if rel == "" {
		return nil
	}
	return g.within(filepath.Join(g.root, filepath.FromSlash(rel)))
}

// confinedParent returns nil if rel's parent directory resolves within root,
// without following a final-component symlink. Used at Walk time: a symlink may
// be reached and read as a symlink (Lstat/Readlink — metadata only, harmless and
// needed for docker-parity), but walking *through* an intermediate symlink that
// escapes root is denied. ".." cannot appear here (validComponent rejects it), so
// the final element can only name a child of an in-root directory.
func (g *guard) confinedParent(rel string) error {
	if rel == "" {
		return nil
	}
	full := filepath.Join(g.root, filepath.FromSlash(rel))
	return g.within(filepath.Dir(full))
}

func (g *guard) within(full string) error {
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if errors.Is(err, gofs.ErrNotExist) {
			parent := filepath.Dir(full)
			if parent == full {
				return err
			}
			return g.within(parent)
		}
		return err
	}
	if resolved == g.root || strings.HasPrefix(resolved, g.root+string(os.PathSeparator)) {
		return nil
	}
	return linux.EACCES
}

type confinedAttach struct{ g *guard }

// Attach implements p9.Attacher.
func (a *confinedAttach) Attach() (p9.File, error) {
	inner, err := localfs.Attacher(a.g.root).Attach()
	if err != nil {
		return nil, err
	}
	return &confinedFile{g: a.g, inner: inner}, nil
}

// confinedFile decorates a localfs node, enforcing the guard's policy. The
// embedded ReadOnlyFile denies every mutating operation; the read operations
// below delegate to localfs after a policy check. DefaultWalkGetAttr routes
// WalkGetAttr through our Walk and GetAttr.
type confinedFile struct {
	templatefs.ReadOnlyFile
	p9.DefaultWalkGetAttr

	g     *guard
	inner p9.File
	rel   string // path from the export root, slash-separated, "" = root
}

var _ p9.File = (*confinedFile)(nil)

// validComponent rejects anything that is not a single, non-traversing path
// element. This is what stops "../../etc/passwd"-style walks at the door.
func validComponent(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	return !strings.ContainsAny(s, "/\\\x00")
}

// Walk implements p9.File.Walk.
func (f *confinedFile) Walk(names []string) ([]p9.QID, p9.File, error) {
	if len(names) == 0 {
		_, c, err := f.inner.Walk(nil)
		if err != nil {
			return nil, nil, err
		}
		return nil, &confinedFile{g: f.g, inner: c, rel: f.rel}, nil
	}
	rel := f.rel
	for _, name := range names {
		if !validComponent(name) {
			return nil, nil, linux.EACCES
		}
		rel = pathpkg.Join(rel, name)
	}
	if f.g.ignored(rel) {
		return nil, nil, linux.ENOENT
	}
	if err := f.g.confinedParent(rel); err != nil {
		return nil, nil, err
	}
	qids, c, err := f.inner.Walk(names)
	if err != nil {
		return nil, nil, err
	}
	return qids, &confinedFile{g: f.g, inner: c, rel: rel}, nil
}

// Open implements p9.File.Open. Writes are refused; reads follow the path fully
// and are denied if that escapes root — so a symlink reachable by Walk still
// cannot be opened to read a file outside the export.
func (f *confinedFile) Open(mode p9.OpenFlags) (p9.QID, uint32, error) {
	if mode.Mode() != p9.ReadOnly {
		return p9.QID{}, 0, linux.EROFS
	}
	if err := f.g.confinedFollow(f.rel); err != nil {
		return p9.QID{}, 0, err
	}
	return f.inner.Open(mode)
}

// ReadAt implements p9.File.ReadAt.
func (f *confinedFile) ReadAt(p []byte, offset int64) (int, error) {
	n, err := f.inner.ReadAt(p, offset)
	if f.g.reads != nil && n > 0 {
		f.g.reads.Add(int64(n))
	}
	return n, err
}

// GetAttr implements p9.File.GetAttr.
func (f *confinedFile) GetAttr(req p9.AttrMask) (p9.QID, p9.AttrMask, p9.Attr, error) {
	return f.inner.GetAttr(req)
}

// Readlink implements p9.File.Readlink. Only reachable for symlinks Walk has
// already confined to root.
func (f *confinedFile) Readlink() (string, error) { return f.inner.Readlink() }

// StatFS implements p9.File.StatFS.
func (f *confinedFile) StatFS() (p9.FSStat, error) { return f.inner.StatFS() }

// ListXattrs reports no extended attributes (the read-only export has none that
// matter). It returns an empty list rather than the embedded ENOSYS: a kernel-9p
// client's listxattr(2) otherwise fails with EINVAL, which breaks BuildKit's
// contenthash (setUnixOpt → LListxattr) on the very first file.
func (f *confinedFile) ListXattrs() ([]string, error) { return nil, nil }

// GetXattr reports no such attribute, so a stray getxattr (e.g. an ACL probe)
// fails gracefully instead of erroring the walk.
func (f *confinedFile) GetXattr(string) ([]byte, error) { return nil, linux.ENODATA }

// Close implements p9.File.Close.
func (f *confinedFile) Close() error { return f.inner.Close() }

// Readdir implements p9.File.Readdir, dropping ignored entries. It refills from
// the underlying directory so a page that is entirely ignored does not look like
// end-of-directory to the client.
func (f *confinedFile) Readdir(offset uint64, count uint32) (p9.Dirents, error) {
	if f.g.ignore == nil {
		return f.inner.Readdir(offset, count)
	}
	out := make(p9.Dirents, 0, count)
	cur := offset
	for uint32(len(out)) < count {
		ents, err := f.inner.Readdir(cur, count)
		if err != nil {
			return nil, err
		}
		if len(ents) == 0 {
			break
		}
		for _, e := range ents {
			cur = e.Offset
			if f.g.ignored(pathpkg.Join(f.rel, e.Name)) {
				continue
			}
			out = append(out, e)
			if uint32(len(out)) >= count {
				break
			}
		}
	}
	return out, nil
}
