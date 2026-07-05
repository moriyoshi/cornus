package sqliteab

import (
	"io"
	"path"
	"sync/atomic"

	"github.com/hugelgupf/p9/p9"
	"github.com/psanford/sqlite3vfs"
)

// blockVFS is a sqlite3vfs.VFS whose files live under a block-proxy p9 root.
// SQLite's OS calls (open/read/write/truncate/sync/delete/access) are mapped
// onto p9 client operations, which flow through ServeBlockProxy over the yamux
// transport to the authoritative directory. All files are flat basenames under
// the attach root (SQLite derives "-journal"/"-wal" siblings by suffixing the
// name FullPathname returns, so keeping names flat keeps them under the root).
type blockVFS struct {
	root p9.File
	tmp  atomic.Uint64
}

var _ sqlite3vfs.VFS = (*blockVFS)(nil)

func (v *blockVFS) FullPathname(name string) string { return name }

func (v *blockVFS) Access(name string, _ sqlite3vfs.AccessFlag) (bool, error) {
	_, f, err := v.root.Walk([]string{path.Base(name)})
	if err != nil {
		return false, nil
	}
	_ = f.Close()
	return true, nil
}

func (v *blockVFS) Delete(name string, _ bool) error {
	// SQLite deletes journals routinely and tolerates a missing target; unlink
	// best-effort and never surface ENOENT as a failure.
	_ = v.root.UnlinkAt(path.Base(name), 0)
	return nil
}

func (v *blockVFS) Open(name string, flags sqlite3vfs.OpenFlag) (sqlite3vfs.File, sqlite3vfs.OpenFlag, error) {
	bn := path.Base(name)
	if name == "" {
		bn = "sqlite-tmp-" + u64(v.tmp.Add(1))
	}

	// Existing file?
	_, wf, werr := v.root.Walk([]string{bn})
	exists := werr == nil
	if exists && flags&sqlite3vfs.OpenExclusive != 0 {
		_ = wf.Close()
		return nil, 0, sqlite3vfs.CantOpenError
	}
	if exists {
		mode := p9.ReadWrite
		if flags&sqlite3vfs.OpenReadOnly != 0 && flags&sqlite3vfs.OpenReadWrite == 0 {
			mode = p9.ReadOnly
		}
		if _, _, err := wf.Open(mode); err != nil {
			_ = wf.Close()
			return nil, 0, err
		}
		return &blockFile{f: wf}, flags, nil
	}
	if flags&sqlite3vfs.OpenCreate == 0 {
		return nil, 0, sqlite3vfs.CantOpenError
	}

	// Create under a fresh clone of the root (Create consumes the walked fid).
	_, nf, err := v.root.Walk(nil)
	if err != nil {
		return nil, 0, err
	}
	created, _, _, err := nf.Create(bn, p9.ReadWrite, 0o644, 0, 0)
	if err != nil {
		_ = nf.Close()
		return nil, 0, err
	}
	return &blockFile{f: created}, flags, nil
}

// blockFile is one open SQLite file, backed by an opened p9 client file.
type blockFile struct {
	f p9.File
}

var _ sqlite3vfs.File = (*blockFile)(nil)

func (b *blockFile) Close() error { return b.f.Close() }

// ReadAt fills p from the file. SQLite requires either a full read or a
// short-read signalled by a non-nil error (psanford zero-fills the remainder).
func (b *blockFile) ReadAt(p []byte, off int64) (int, error) {
	total := 0
	for total < len(p) {
		n, err := b.f.ReadAt(p[total:], off+int64(total))
		total += n
		if err != nil {
			if err == io.EOF {
				break
			}
			return total, err
		}
		if n == 0 {
			break
		}
	}
	if total < len(p) {
		return total, io.EOF
	}
	return total, nil
}

// WriteAt writes all of p; a short write must return a non-nil error.
func (b *blockFile) WriteAt(p []byte, off int64) (int, error) {
	total := 0
	for total < len(p) {
		n, err := b.f.WriteAt(p[total:], off+int64(total))
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

func (b *blockFile) Truncate(size int64) error {
	return b.f.SetAttr(p9.SetAttrMask{Size: true}, p9.SetAttr{Size: uint64(size)})
}

// Sync issues a p9 FSync, which the block proxy forwards as a Tfsync to the
// authoritative owner — the durable write-through path a database relies on.
func (b *blockFile) Sync(_ sqlite3vfs.SyncType) error { return b.f.FSync() }

func (b *blockFile) FileSize() (int64, error) {
	_, _, attr, err := b.f.GetAttr(p9.AttrMask{Size: true})
	if err != nil {
		return 0, err
	}
	return int64(attr.Size), nil
}

// Locking is a no-op: the benchmark drives a single connection
// (db.SetMaxOpenConns(1)), so there is exactly one writer and no cross-connection
// contention to arbitrate.
func (b *blockFile) Lock(sqlite3vfs.LockType) error   { return nil }
func (b *blockFile) Unlock(sqlite3vfs.LockType) error { return nil }
func (b *blockFile) CheckReservedLock() (bool, error) { return false, nil }
func (b *blockFile) SectorSize() int64                { return 4096 }
func (b *blockFile) DeviceCharacteristics() sqlite3vfs.DeviceCharacteristic {
	return 0
}

func u64(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
