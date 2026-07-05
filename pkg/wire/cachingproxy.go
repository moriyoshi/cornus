package wire

import (
	"net"
	pathpkg "path"

	"github.com/hugelgupf/p9/fsimpl/templatefs"
	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/blockcache"
)

// The caching 9P proxy. On the kernel-9p mount paths the cornus server otherwise
// blindly pipes 9P frames between the kernel-mounted unix socket and the caller's
// export (tunnel9P). This proxy instead terminates 9P in userspace: our p9.Server
// answers the kernel while a p9.Client speaks to the caller, and file reads are
// served from a fixed-chunk block cache so a chunk pulled once is not re-fetched
// over the WebSocket on subsequent reads (across reads, mounts, and restarts).
//
// It is only used for read-only, cacheable mounts (immutable build contexts and
// deploy mounts explicitly flagged immutable). Writable mounts keep the blind
// pipe. See ServeCachingProxy.

// remoteMessageSize is the 9P message size negotiated with the caller. It matches
// the kernel mount's msize (1 MiB) and the cache chunk size so one kernel Tread
// maps to one chunk maps to (on a miss) a minimal number of client Treads. The
// p9 client default is only 64 KiB, which would turn each 1 MiB chunk fetch into
// ~16 round trips.
const remoteMessageSize = 1 << 20

// ServeCachingProxy terminates the kernel-9p session on kernelConn in userspace
// and proxies it to the caller's export over remoteStream, serving reads through
// cache. mount is the logical mount/context name, used to keep cache keys of
// distinct exports disjoint. It blocks until the kernel session ends. The caller
// is responsible for closing kernelConn and remoteStream (as tunnel9P already
// does); this also closes the p9 client on return.
func ServeCachingProxy(kernelConn, remoteStream net.Conn, cache *blockcache.Cache, mount string) {
	client, err := p9.NewClient(remoteStream, p9.WithMessageSize(remoteMessageSize))
	if err != nil {
		return
	}
	defer client.Close()
	attacher := &cachingAttach{client: client, cache: cache, mount: mount}
	_ = p9.NewServer(attacher).Handle(kernelConn, kernelConn)
}

// cachingAttach attaches the caller's export via the p9 client and wraps the root
// fid in a cachingFile.
type cachingAttach struct {
	client *p9.Client
	cache  *blockcache.Cache
	mount  string
}

// Attach implements p9.Attacher.
func (a *cachingAttach) Attach() (p9.File, error) {
	root, err := a.client.Attach("")
	if err != nil {
		return nil, err
	}
	return &cachingFile{a: a, inner: root}, nil
}

// cachingFile is a transparent p9.File proxy over a remote (client) file. Every
// operation delegates to the remote; ReadAt additionally serves from the block
// cache once the file has been Opened read-only (which freezes its FileID). The
// embedded ReadOnlyFile denies mutating and directory-creation ops — the export
// is read-only anyway, and the kernel mounts these read-only.
type cachingFile struct {
	templatefs.ReadOnlyFile

	a     *cachingAttach
	inner p9.File
	rel   string // path from the export root, slash-separated, "" = root

	// Frozen at Open for a regular file; drives cache keying for ReadAt.
	cacheable bool
	id        blockcache.FileID
	size      int64

	// Attributes observed via WalkGetAttr/GetAttr, used to freeze the validator
	// without an extra round trip at Open when possible.
	haveAttr   bool
	obsSize    int64
	obsMTime   int64
	obsRegular bool
}

var _ p9.File = (*cachingFile)(nil)

func (f *cachingFile) child(inner p9.File, rel string) *cachingFile {
	return &cachingFile{a: f.a, inner: inner, rel: rel}
}

func joinNames(base string, names []string) string {
	rel := base
	for _, n := range names {
		rel = pathpkg.Join(rel, n)
	}
	return rel
}

// Walk implements p9.File.Walk. The p9 server calls it with zero or one element;
// an empty names clones the current fid.
func (f *cachingFile) Walk(names []string) ([]p9.QID, p9.File, error) {
	qids, c, err := f.inner.Walk(names)
	if err != nil {
		return nil, nil, err
	}
	return qids, f.child(c, joinNames(f.rel, names)), nil
}

// WalkGetAttr implements p9.File.WalkGetAttr, delegating to the remote so the
// kernel's frequent Twalkgetattr costs a single round trip (falling back to
// Walk+GetAttr only where the remote itself does). The returned attrs seed the
// validator.
func (f *cachingFile) WalkGetAttr(names []string) ([]p9.QID, p9.File, p9.AttrMask, p9.Attr, error) {
	qids, c, mask, attr, err := f.inner.WalkGetAttr(names)
	if err != nil {
		return nil, nil, p9.AttrMask{}, p9.Attr{}, err
	}
	cf := f.child(c, joinNames(f.rel, names))
	cf.observe(mask, attr)
	return qids, cf, mask, attr, nil
}

// observe records size/mtime/regularity from an attr result so Open can freeze
// the validator without a further GetAttr.
func (f *cachingFile) observe(mask p9.AttrMask, attr p9.Attr) {
	if !mask.Size || !mask.MTime || !mask.Mode {
		return
	}
	f.haveAttr = true
	f.obsSize = int64(attr.Size)
	f.obsMTime = int64(attr.MTimeSeconds)*1e9 + int64(attr.MTimeNanoSeconds)
	f.obsRegular = attr.Mode.IsRegular()
}

// GetAttr implements p9.File.GetAttr.
func (f *cachingFile) GetAttr(req p9.AttrMask) (p9.QID, p9.AttrMask, p9.Attr, error) {
	qid, mask, attr, err := f.inner.GetAttr(req)
	if err == nil {
		f.observe(mask, attr)
	}
	return qid, mask, attr, err
}

// Open implements p9.File.Open. For a read-only open of a regular file it freezes
// the FileID (mount, path, size, mtime) so every subsequent ReadAt on this fid is
// cache-keyed against that immutable snapshot.
func (f *cachingFile) Open(mode p9.OpenFlags) (p9.QID, uint32, error) {
	qid, iounit, err := f.inner.Open(mode)
	if err != nil {
		return qid, iounit, err
	}
	if f.a.cache != nil && mode.Mode() == p9.ReadOnly {
		if !f.haveAttr {
			if _, mask, attr, gerr := f.inner.GetAttr(p9.AttrMask{Size: true, MTime: true, Mode: true}); gerr == nil {
				f.observe(mask, attr)
			}
		}
		if f.haveAttr && f.obsRegular {
			f.id = blockcache.FileID{Mount: f.a.mount, Path: f.rel, Size: f.obsSize, MTimeNs: f.obsMTime}
			f.size = f.obsSize
			f.cacheable = true
		}
	}
	return qid, iounit, nil
}

// ReadAt implements p9.File.ReadAt, serving from the block cache for cacheable
// (opened, regular) files and delegating directly otherwise.
func (f *cachingFile) ReadAt(p []byte, offset int64) (int, error) {
	if f.cacheable {
		return f.a.cache.ReadAt(f.id, f.size, f.fetch, p, offset)
	}
	return f.inner.ReadAt(p, offset)
}

// fetch reads chunk-aligned bytes from the remote on a cache miss.
func (f *cachingFile) fetch(off int64, buf []byte) (int, error) {
	return f.inner.ReadAt(buf, off)
}

// Readdir implements p9.File.Readdir (directory pages are not cached).
func (f *cachingFile) Readdir(offset uint64, count uint32) (p9.Dirents, error) {
	return f.inner.Readdir(offset, count)
}

// Readlink implements p9.File.Readlink.
func (f *cachingFile) Readlink() (string, error) { return f.inner.Readlink() }

// StatFS implements p9.File.StatFS.
func (f *cachingFile) StatFS() (p9.FSStat, error) { return f.inner.StatFS() }

// ListXattrs implements p9.File.ListXattrs.
func (f *cachingFile) ListXattrs() ([]string, error) { return f.inner.ListXattrs() }

// GetXattr implements p9.File.GetXattr.
func (f *cachingFile) GetXattr(attr string) ([]byte, error) { return f.inner.GetXattr(attr) }

// Close implements p9.File.Close. Every proxy fid owns exactly one remote fid;
// closing here clunks it, which is essential to avoid exhausting the client's fid
// pool as the kernel clone-walks path components.
func (f *cachingFile) Close() error { return f.inner.Close() }
