package fsop

import (
	"archive/tar"
	"io"
	"os"
	pathpkg "path"
	"strings"
	"time"

	"github.com/pkg/sftp"

	"cornus/pkg/api"
)

// SFTPFS serves an FSOp over an SFTP channel, for a backend with no local path
// to open.
//
// incus is the case it exists for: its instances are reachable through the
// daemon's own file channel (GetInstanceFileSFTP), which needs nothing in the
// image — an OCI application container ships no sshd, and a distroless one has no
// shell either. The alternative, reading the instance's rootfs from the host,
// works only when cornus is co-located on a `dir` storage pool AND reports
// id-shifted ownership (a uid-1000 file appears as 1001000), which is the wrong
// answer to show a file browser.
//
// CONFINEMENT differs from LocalFS by construction and deliberately. LocalFS must
// resolve symlinks against its root because the surrounding filesystem is the
// HOST, and a container symlink that escaped would hand out the machine. Here the
// channel can only ever address one instance, so the instance is the boundary and
// the daemon enforces it; a symlink inside the instance resolving elsewhere inside
// the instance is not an escape. What is still required is that a request path
// cannot climb ABOVE the declared root, which path cleaning gives.
type SFTPFS struct{ Client *sftp.Client }

// abs joins a root and a request-relative path, refusing to climb above the root.
// Clean resolves any ".." lexically before the join, so the result is always
// within rootPath.
func (s SFTPFS) abs(rootPath, rel string) string {
	return pathpkg.Join(rootPath, pathpkg.Clean("/"+strings.TrimPrefix(rel, "/")))
}

func (s SFTPFS) Stat(rootPath, rel string) (api.PathStat, error) {
	p := s.abs(rootPath, rel)
	fi, err := s.Client.Lstat(p)
	if err != nil {
		return api.PathStat{}, err
	}
	st := api.PathStat{
		Name:  pathpkg.Base(pathpkg.Clean("/" + strings.TrimPrefix(rel, "/"))),
		Size:  fi.Size(),
		Mode:  uint32(fi.Mode()),
		Mtime: fi.ModTime().UTC().Format(time.RFC3339Nano),
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		if st.LinkTarget, err = s.Client.ReadLink(p); err != nil {
			return api.PathStat{}, err
		}
	}
	return st, nil
}

func (s SFTPFS) List(rootPath, rel string) ([]api.PathStat, error) {
	p := s.abs(rootPath, rel)
	fi, err := s.Client.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, &StatusError{Code: api.FSErrNotDir, Msg: rel + ": not a directory"}
	}
	// ReadDir does not follow symlinks, which is the behaviour the contract
	// requires: a listing that followed them would report the TARGET's kind and
	// size for something that is a link.
	infos, err := s.Client.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]api.PathStat, 0, len(infos))
	for _, info := range infos {
		e := api.PathStat{
			Name:  info.Name(),
			Size:  info.Size(),
			Mode:  uint32(info.Mode()),
			Mtime: info.ModTime().UTC().Format(time.RFC3339Nano),
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// A link whose target cannot be read is still a link; dropping the
			// entry would hide it from the listing entirely.
			e.LinkTarget, _ = s.Client.ReadLink(pathpkg.Join(p, info.Name()))
		}
		out = append(out, e)
	}
	return out, nil
}

// Pack writes the same archive shape LocalFS does: one top-level entry named
// after the request path's basename, so a copy produces docker-cp-compatible
// naming whichever route served it.
func (s SFTPFS) Pack(rootPath, rel string, w io.Writer) error {
	p := s.abs(rootPath, rel)
	fi, err := s.Client.Lstat(p)
	if err != nil {
		return err
	}
	name := pathpkg.Base(pathpkg.Clean("/" + strings.TrimPrefix(rel, "/")))
	tw := tar.NewWriter(w)
	if err := s.packOne(tw, p, name, fi); err != nil {
		return err
	}
	if fi.IsDir() {
		if err := s.packDir(tw, p, name); err != nil {
			return err
		}
	}
	return tw.Close()
}

func (s SFTPFS) packDir(tw *tar.Writer, dir, prefix string) error {
	infos, err := s.Client.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, info := range infos {
		child := pathpkg.Join(dir, info.Name())
		childName := prefix + "/" + info.Name()
		if err := s.packOne(tw, child, childName, info); err != nil {
			return err
		}
		if info.IsDir() {
			if err := s.packDir(tw, child, childName); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s SFTPFS) packOne(tw *tar.Writer, path, name string, fi os.FileInfo) error {
	link := ""
	if fi.Mode()&os.ModeSymlink != 0 {
		var err error
		if link, err = s.Client.ReadLink(path); err != nil {
			return err
		}
	}
	hdr, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return err
	}
	hdr.Name = name
	if fi.IsDir() {
		hdr.Name += "/"
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	f, err := s.Client.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func (s SFTPFS) Unpack(rootPath, rel string, r io.Reader, opts UnpackOptions) error {
	dest := s.abs(rootPath, rel)
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := s.abs(dest, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := s.Client.MkdirAll(target); err != nil {
				return err
			}
			if err := s.Client.Chmod(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Replace rather than fail: an extraction over an existing tree is
			// ordinary, and Symlink refuses an existing name.
			_ = s.Client.Remove(target)
			if err := s.Client.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeReg:
			if opts.NoOverwriteDirNonDir {
				if fi, err := s.Client.Stat(target); err == nil && fi.IsDir() {
					return &StatusError{Code: api.FSErrIsDir, Msg: hdr.Name + ": cannot overwrite a directory with a file"}
				}
			}
			if err := s.Client.MkdirAll(pathpkg.Dir(target)); err != nil {
				return err
			}
			f, err := s.Client.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			if err := s.Client.Chmod(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		default:
			// Devices, fifos and sockets have no SFTP primitive. Skipping one is
			// right where failing the whole extraction is not: the archive's
			// ordinary files still land, which is what a file browser copied.
			continue
		}
		if opts.CopyUIDGID {
			// Best effort: the channel may be serving as a user who cannot chown,
			// and a copy that lands with the caller's ownership beats one that
			// fails outright.
			_ = s.Client.Chown(target, hdr.Uid, hdr.Gid)
		}
		if !hdr.ModTime.IsZero() {
			_ = s.Client.Chtimes(target, hdr.ModTime, hdr.ModTime)
		}
	}
}

func (s SFTPFS) MkdirAll(rootPath, rel string) error {
	return s.Client.MkdirAll(s.abs(rootPath, rel))
}

func (s SFTPFS) Remove(rootPath, rel string, recursive bool) error {
	p := s.abs(rootPath, rel)
	if recursive {
		if err := s.Client.RemoveAll(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	// Delete-if-exists, matching LocalFS: a retried delete must not report a
	// failure for work already done.
	if err := s.Client.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s SFTPFS) Rename(srcRootPath, srcRel, dstRootPath, dstRel string) error {
	return s.Client.Rename(s.abs(srcRootPath, srcRel), s.abs(dstRootPath, dstRel))
}
