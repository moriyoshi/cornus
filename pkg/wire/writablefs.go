package wire

import (
	pathpkg "path"
	"path/filepath"

	"github.com/hugelgupf/p9/fsimpl/localfs"
	"github.com/hugelgupf/p9/fsimpl/templatefs"
	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/unix"
)

// stripAppend clears O_APPEND from a 9P open/create flag set. localfs serves
// writes with pwrite (os.File.WriteAt), which Go refuses on a fd opened with
// O_APPEND ("invalid use of WriteAt on file opened with O_APPEND") — so serving
// an append-mode open unchanged makes every write to that file fail with EIO,
// breaking any workload that appends to a file in the mount (log files, etc.).
// It is safe to drop: a cache=none 9P client already issues each append write at
// the current end-of-file offset, so a plain pwrite at that offset appends
// correctly without the host fd carrying O_APPEND. 9p2000.L open flags are Linux
// open flags, so O_APPEND is the same bit here.
func stripAppend(flags p9.OpenFlags) p9.OpenFlags {
	return flags &^ p9.OpenFlags(unix.O_APPEND)
}

// writableConfinedAttacher exports root over 9P read-WRITE, confined to root with
// the same containment as confinedAttacher (no ".." traversal, no walking through
// symlinks that escape root), but mutating operations (create, write, mkdir,
// unlink, rename, setattr, ...) are delegated to localfs instead of refused. It
// backs read-write client-local deploy mounts.
//
// Containment holds for writes because every mutation is reached only through a
// confined Walk/Open, and each create-family op validates the new name is a
// single non-traversing component (validComponent) rooted at an already-confined
// parent — so a hostile 9P client cannot create/write/rename outside root.
func writableConfinedAttacher(root string) (p9.Attacher, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(real)
	if err != nil {
		return nil, err
	}
	return &writableConfinedAttach{g: &guard{root: abs}}, nil
}

type writableConfinedAttach struct{ g *guard }

// Attach implements p9.Attacher.
func (a *writableConfinedAttach) Attach() (p9.File, error) {
	inner, err := localfs.Attacher(a.g.root).Attach()
	if err != nil {
		return nil, err
	}
	return &writableConfinedFile{g: a.g, inner: inner}, nil
}

// writableConfinedFile decorates a localfs node with the guard's containment
// policy, delegating both reads and writes to localfs after a policy check.
// NoopFile supplies defaults for the ops we don't handle (mknod, lock, xattr
// set, old-style rename) — the same base localfs itself uses.
type writableConfinedFile struct {
	templatefs.NoopFile
	p9.DefaultWalkGetAttr

	g     *guard
	inner p9.File
	rel   string // path from the export root, slash-separated, "" = root
}

var _ p9.File = (*writableConfinedFile)(nil)

func (f *writableConfinedFile) child(inner p9.File, rel string) *writableConfinedFile {
	return &writableConfinedFile{g: f.g, inner: inner, rel: rel}
}

// Walk implements p9.File.Walk, applying the same ".."/symlink jail as the
// read-only export.
func (f *writableConfinedFile) Walk(names []string) ([]p9.QID, p9.File, error) {
	if len(names) == 0 {
		_, c, err := f.inner.Walk(nil)
		if err != nil {
			return nil, nil, err
		}
		return nil, f.child(c, f.rel), nil
	}
	rel := f.rel
	for _, name := range names {
		if !validComponent(name) {
			return nil, nil, linux.EACCES
		}
		rel = pathpkg.Join(rel, name)
	}
	if err := f.g.confinedParent(rel); err != nil {
		return nil, nil, err
	}
	qids, c, err := f.inner.Walk(names)
	if err != nil {
		return nil, nil, err
	}
	return qids, f.child(c, rel), nil
}

// Open allows read and write modes (the export is writable), after confining a
// final-component symlink like the read-only path.
func (f *writableConfinedFile) Open(mode p9.OpenFlags) (p9.QID, uint32, error) {
	if err := f.g.confinedFollow(f.rel); err != nil {
		return p9.QID{}, 0, err
	}
	return f.inner.Open(stripAppend(mode))
}

func (f *writableConfinedFile) ReadAt(p []byte, offset int64) (int, error) {
	return f.inner.ReadAt(p, offset)
}

func (f *writableConfinedFile) WriteAt(p []byte, offset int64) (int, error) {
	return f.inner.WriteAt(p, offset)
}

func (f *writableConfinedFile) GetAttr(req p9.AttrMask) (p9.QID, p9.AttrMask, p9.Attr, error) {
	return f.inner.GetAttr(req)
}

// SetAttr confines like Open before delegating: SetAttr(size) truncates through a
// final-component symlink (localfs uses os.Truncate), so a fid pointing at a
// symlink whose target escapes root must be refused, or a hostile client could
// truncate files outside the export.
func (f *writableConfinedFile) SetAttr(valid p9.SetAttrMask, attr p9.SetAttr) error {
	if err := f.g.confinedFollow(f.rel); err != nil {
		return err
	}
	return f.inner.SetAttr(valid, attr)
}

func (f *writableConfinedFile) FSync() error { return f.inner.FSync() }

// Lock delegates advisory file locking to localfs (flock on the server-side fd),
// rather than inheriting NoopFile's NotLockable stub which returns ENOSYS. A
// writable client-local mount must honor locks: workloads that acquire a POSIX/
// flock lock on a file in the mount (e.g. Next.js/Turbopack's .next/dev/lock,
// SQLite, package managers) otherwise fail on the very first lock with "Function
// not implemented". The fid is already confined at Open time, so no extra guard
// is needed here. The lock is held on the host file, so it is coherent across all
// 9P clients of the same mount — the intended single-writer semantics.
func (f *writableConfinedFile) Lock(pid int, locktype p9.LockType, flags p9.LockFlags, start, length uint64, client string) (p9.LockStatus, error) {
	return f.inner.Lock(pid, locktype, flags, start, length, client)
}

func (f *writableConfinedFile) Readlink() (string, error) { return f.inner.Readlink() }

func (f *writableConfinedFile) StatFS() (p9.FSStat, error) { return f.inner.StatFS() }

func (f *writableConfinedFile) ListXattrs() ([]string, error) { return nil, nil }

func (f *writableConfinedFile) GetXattr(string) ([]byte, error) { return nil, linux.ENODATA }

func (f *writableConfinedFile) Close() error { return f.inner.Close() }

func (f *writableConfinedFile) Readdir(offset uint64, count uint32) (p9.Dirents, error) {
	return f.inner.Readdir(offset, count)
}

// Create makes a new child in this (confined) directory; the name must be a
// single non-traversing component so the child stays within root.
func (f *writableConfinedFile) Create(name string, flags p9.OpenFlags, perm p9.FileMode, uid p9.UID, gid p9.GID) (p9.File, p9.QID, uint32, error) {
	if !validComponent(name) {
		return nil, p9.QID{}, 0, linux.EACCES
	}
	c, qid, ioUnit, err := f.inner.Create(name, stripAppend(flags), perm, uid, gid)
	if err != nil {
		return nil, p9.QID{}, 0, err
	}
	return f.child(c, pathpkg.Join(f.rel, name)), qid, ioUnit, nil
}

func (f *writableConfinedFile) Mkdir(name string, perm p9.FileMode, uid p9.UID, gid p9.GID) (p9.QID, error) {
	if !validComponent(name) {
		return p9.QID{}, linux.EACCES
	}
	return f.inner.Mkdir(name, perm, uid, gid)
}

// Symlink creates a symlink named newName pointing at oldName. oldName is stored
// verbatim (arbitrary link text is harmless — following an escaping symlink is
// denied at Open/Walk time), but newName must be a single component.
func (f *writableConfinedFile) Symlink(oldName, newName string, uid p9.UID, gid p9.GID) (p9.QID, error) {
	if !validComponent(newName) {
		return p9.QID{}, linux.EACCES
	}
	return f.inner.Symlink(oldName, newName, uid, gid)
}

func (f *writableConfinedFile) Link(target p9.File, newName string) error {
	if !validComponent(newName) {
		return linux.EACCES
	}
	t, ok := target.(*writableConfinedFile)
	if !ok {
		return linux.EACCES
	}
	return f.inner.Link(t.inner, newName)
}

func (f *writableConfinedFile) UnlinkAt(name string, flags uint32) error {
	if !validComponent(name) {
		return linux.EACCES
	}
	return f.inner.UnlinkAt(name, flags)
}

func (f *writableConfinedFile) RenameAt(oldName string, newDir p9.File, newName string) error {
	if !validComponent(oldName) || !validComponent(newName) {
		return linux.EACCES
	}
	nd, ok := newDir.(*writableConfinedFile)
	if !ok {
		return linux.EACCES
	}
	return f.inner.RenameAt(oldName, nd.inner, newName)
}
