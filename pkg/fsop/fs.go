package fsop

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/containerd/continuity/fs"

	"cornus/pkg/api"
	"cornus/pkg/deploy/containerdhost/tarcopy"
)

// FS is the I/O an FSOp is served through. It exists so that WHERE the bytes
// live and WHAT an operation means stop being the same decision.
//
// Everything that decides an outcome stays in this package and is shared by every
// implementation: which root serves a path, the read-only refusal, the
// docker-cp-compatible naming a copy produces, the refusal to remove or overwrite
// a mount root, the listing truncation, and the FSErr* classification a caller
// picks its next move from. Two implementations of "copy a directory" is how
// routes start disagreeing, and that was already the stated reason this package
// exists at all.
//
// What varies is only the I/O. LocalFS reaches a tree this process can open;
// incus reaches an instance through the daemon's SFTP channel, where there is no
// local path to open at all and the ownership a local path WOULD report is
// id-shifted and wrong to show a user.
//
// rootPath is the implementation's own handle for a root (a directory for
// LocalFS, the instance root for a remote one) and rel is the absolute path
// within it. Confinement is the implementation's responsibility and differs by
// construction: LocalFS resolves symlinks against the root so a container link
// cannot escape to the HOST, while a channel that can only ever see one instance
// is confined by the channel itself.
type FS interface {
	// Stat returns the docker-cp-compatible stat of rel.
	Stat(rootPath, rel string) (api.PathStat, error)
	// List returns rel's entries, describing symlinks rather than following them:
	// a listing that followed them would report a directory's size and kind for
	// something that is a link, and the caller would then walk into it. It must
	// fail with a FSErrNotDir StatusError when rel is not a directory.
	List(rootPath, rel string) ([]api.PathStat, error)
	// Pack writes a tar of rel, in the same framing `docker cp` uses.
	Pack(rootPath, rel string, w io.Writer) error
	// Unpack extracts a tar into rel.
	Unpack(rootPath, rel string, r io.Reader, opts UnpackOptions) error
	// MkdirAll creates rel and any missing parents.
	MkdirAll(rootPath, rel string) error
	// Remove deletes rel. A path that is not there is a SUCCESS, matching
	// Backend.Delete and VolumeRemover, so a retried delete does not report a
	// failure for work already done.
	Remove(rootPath, rel string, recursive bool) error
	// Rename moves src to dst. Both are already known writable.
	Rename(srcRootPath, srcRel, dstRootPath, dstRel string) error
}

// UnpackOptions carries the two tar-extraction flags an FSOp request can set.
type UnpackOptions struct {
	NoOverwriteDirNonDir bool
	CopyUIDGID           bool
}

// LocalFS serves a tree this process can open directly: the caretaker's mounted
// volumes, or a container rootfs reached through its init process's
// /proc/<pid>/root.
//
// Every method goes through continuity's fs.RootPath, which is what confines a
// symlink inside the container so it cannot resolve out to the host. That
// confinement is the reason this is not simply os.
type LocalFS struct{}

func (LocalFS) Stat(rootPath, rel string) (api.PathStat, error) {
	return tarcopy.Stat(rootPath, rel)
}

func (LocalFS) List(rootPath, rel string) ([]api.PathStat, error) {
	host, err := fs.RootPath(rootPath, rel)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(host)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, &StatusError{Code: api.FSErrNotDir, Msg: rel + ": not a directory"}
	}
	des, err := os.ReadDir(host)
	if err != nil {
		return nil, err
	}
	out := make([]api.PathStat, 0, len(des))
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			// The entry vanished between the readdir and the stat. A live directory
			// does this; dropping it is right, failing the whole listing is not.
			continue
		}
		e := api.PathStat{
			Name:  de.Name(),
			Size:  info.Size(),
			Mode:  uint32(info.Mode()),
			Mtime: info.ModTime().UTC().Format(time.RFC3339Nano),
		}
		if info.Mode()&os.ModeSymlink != 0 {
			e.LinkTarget, _ = os.Readlink(filepath.Join(host, de.Name()))
		}
		out = append(out, e)
	}
	return out, nil
}

func (LocalFS) Pack(rootPath, rel string, w io.Writer) error {
	_, err := tarcopy.Pack(rootPath, rel, w)
	return err
}

func (LocalFS) Unpack(rootPath, rel string, r io.Reader, opts UnpackOptions) error {
	return tarcopy.Unpack(rootPath, rel, r, tarcopy.UnpackOptions{
		NoOverwriteDirNonDir: opts.NoOverwriteDirNonDir,
		CopyUIDGID:           opts.CopyUIDGID,
	})
}

func (LocalFS) MkdirAll(rootPath, rel string) error {
	host, err := fs.RootPath(rootPath, rel)
	if err != nil {
		return err
	}
	return os.MkdirAll(host, 0o755)
}

func (LocalFS) Remove(rootPath, rel string, recursive bool) error {
	host, err := fs.RootPath(rootPath, rel)
	if err != nil {
		return err
	}
	if recursive {
		return os.RemoveAll(host)
	}
	if err := os.Remove(host); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (LocalFS) Rename(srcRootPath, srcRel, dstRootPath, dstRel string) error {
	from, err := fs.RootPath(srcRootPath, srcRel)
	if err != nil {
		return err
	}
	to, err := fs.RootPath(dstRootPath, dstRel)
	if err != nil {
		return err
	}
	return os.Rename(from, to)
}
