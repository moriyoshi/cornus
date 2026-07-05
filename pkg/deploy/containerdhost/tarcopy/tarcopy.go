// Package tarcopy implements docker-cp-compatible tar pack and unpack against
// a confinement root. In production the root is /proc/<pid>/root of a running
// container; in tests it is a plain temporary directory.
//
// All path resolution is confined to the root: the parent directory of any
// operand is resolved through continuity's fs.RootPath, which follows
// symlinks safely within the root, and the final path component is handled
// with Lstat so symlinks are described or created rather than followed.
package tarcopy

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/continuity/fs"

	"cornus/pkg/api"
)

// preservedModeBits are the file mode bits restored on unpack.
const preservedModeBits = iofs.ModePerm | iofs.ModeSetuid | iofs.ModeSetgid | iofs.ModeSticky

// resolveParent resolves the parent directory of the container-absolute path
// p through fs.RootPath (following symlinks confined to root) and returns the
// resolved host directory plus the final path component. The final component
// is deliberately not resolved so callers can Lstat or create it.
func resolveParent(root, p string) (hostDir string, base string, err error) {
	cp := pathpkg.Clean("/" + filepath.ToSlash(p))
	if cp == "/" {
		hostDir, err = fs.RootPath(root, "/")
		return hostDir, ".", err
	}
	hostDir, err = fs.RootPath(root, pathpkg.Dir(cp))
	if err != nil {
		return "", "", err
	}
	return hostDir, pathpkg.Base(cp), nil
}

// Stat returns docker-compatible metadata about the container-absolute path
// inside root. Symlinks are described, not followed; LinkTarget carries the
// raw readlink value for symlinks. A missing path yields an error wrapping
// os.ErrNotExist.
func Stat(root, path string) (api.PathStat, error) {
	hostDir, base, err := resolveParent(root, path)
	if err != nil {
		return api.PathStat{}, err
	}
	host := filepath.Join(hostDir, base)
	fi, err := os.Lstat(host)
	if err != nil {
		return api.PathStat{}, err
	}
	st := api.PathStat{
		Name:  pathpkg.Base(pathpkg.Clean("/" + filepath.ToSlash(path))),
		Size:  fi.Size(),
		Mode:  uint32(fi.Mode()),
		Mtime: fi.ModTime().UTC().Format(time.RFC3339Nano),
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		st.LinkTarget, err = os.Readlink(host)
		if err != nil {
			return api.PathStat{}, err
		}
	}
	return st, nil
}

// writeHeader writes a tar header for fi under the archive name, carrying
// mode, mtime, uid/gid (via tar.FileInfoHeader) and the symlink target.
func writeHeader(tw *tar.Writer, fi os.FileInfo, name, link string) (*tar.Header, error) {
	hdr, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return nil, err
	}
	hdr.Name = name
	if fi.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
		hdr.Name += "/"
	}
	return hdr, tw.WriteHeader(hdr)
}

// zeroReader is an unbounded source of zero bytes, used to pad a tar body when
// a file shrinks between the Lstat that sized its header and the read below.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// packFile writes one regular file's header and contents.
func packFile(tw *tar.Writer, fi os.FileInfo, host, name string) error {
	if _, err := writeHeader(tw, fi, name, ""); err != nil {
		return err
	}
	f, err := os.Open(host)
	if err != nil {
		return err
	}
	defer f.Close()
	// The header's Size is fi.Size(); the body must be exactly that many bytes
	// or tar.Writer fails the next WriteHeader/Close with "missed writing N
	// bytes". The confinement root is a live container, so the file may shrink
	// between the Lstat that produced fi and this read, leaving io.CopyN short.
	// Zero-pad the shortfall so the entry stays well-formed (docker parity).
	n, err := io.CopyN(tw, f, fi.Size())
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n < fi.Size() {
		if _, err := io.CopyN(tw, zeroReader{}, fi.Size()-n); err != nil {
			return err
		}
	}
	return nil
}

// packDir recursively archives the directory at container path cpath under
// the archive name arcname. Each descent re-confines through fs.RootPath.
// Symlinked subdirectories are archived as symlink entries and never
// followed (docker parity); sockets and devices are skipped silently.
func packDir(tw *tar.Writer, root, cpath, arcname string) error {
	hostDir, err := fs.RootPath(root, cpath)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(hostDir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		fi, err := ent.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // raced with a concurrent delete
			}
			return err
		}
		childHost := filepath.Join(hostDir, ent.Name())
		childArc := pathpkg.Join(arcname, ent.Name())
		mode := fi.Mode()
		switch {
		case mode.IsDir():
			if _, err := writeHeader(tw, fi, childArc, ""); err != nil {
				return err
			}
			if err := packDir(tw, root, pathpkg.Join(cpath, ent.Name()), childArc); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			link, err := os.Readlink(childHost)
			if err != nil {
				return err
			}
			if _, err := writeHeader(tw, fi, childArc, link); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := packFile(tw, fi, childHost, childArc); err != nil {
				return err
			}
		default:
			// Sockets, devices and other special files are skipped silently.
		}
	}
	return nil
}

// Pack writes a tar stream of the container-absolute path inside root to w,
// with docker cp FROM-container semantics: a directory becomes one top-level
// directory entry named filepath.Base(path) with its tree underneath; a
// regular file or symlink becomes a single entry named filepath.Base(path).
// Mode, mtime, uid/gid and symlink targets are preserved. The Stat of path
// is returned alongside.
func Pack(root, path string, w io.Writer) (api.PathStat, error) {
	st, err := Stat(root, path)
	if err != nil {
		return api.PathStat{}, err
	}
	hostDir, base, err := resolveParent(root, path)
	if err != nil {
		return api.PathStat{}, err
	}
	host := filepath.Join(hostDir, base)
	fi, err := os.Lstat(host)
	if err != nil {
		return api.PathStat{}, err
	}
	name := pathpkg.Base(pathpkg.Clean("/" + filepath.ToSlash(path)))

	tw := tar.NewWriter(w)
	mode := fi.Mode()
	switch {
	case mode.IsDir():
		if _, err := writeHeader(tw, fi, name, ""); err != nil {
			return api.PathStat{}, err
		}
		cpath := pathpkg.Clean("/" + filepath.ToSlash(path))
		if err := packDir(tw, root, cpath, name); err != nil {
			return api.PathStat{}, err
		}
	case mode&os.ModeSymlink != 0:
		link, err := os.Readlink(host)
		if err != nil {
			return api.PathStat{}, err
		}
		if _, err := writeHeader(tw, fi, name, link); err != nil {
			return api.PathStat{}, err
		}
	case mode.IsRegular():
		if err := packFile(tw, fi, host, name); err != nil {
			return api.PathStat{}, err
		}
	default:
		// Sockets, devices and other special files produce an empty archive.
	}
	if err := tw.Close(); err != nil {
		return api.PathStat{}, err
	}
	return st, nil
}

// UnpackOptions mirrors Docker's archive PUT query parameters.
type UnpackOptions struct {
	// NoOverwriteDirNonDir refuses to replace an existing directory with a
	// non-directory entry or vice versa.
	NoOverwriteDirNonDir bool
	// CopyUIDGID restores tar uid/gid on extracted entries. Chown failures
	// with EPERM (running unprivileged) are ignored.
	CopyUIDGID bool
}

// cleanEntryName validates and cleans a tar entry name relative to the
// extraction destination. Absolute names and names escaping the destination
// via ".." are rejected.
func cleanEntryName(name string) (string, error) {
	n := filepath.ToSlash(name)
	if pathpkg.IsAbs(n) {
		return "", fmt.Errorf("tar entry %q points outside of the destination", name)
	}
	cleaned := pathpkg.Clean(n)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("tar entry %q points outside of the destination", name)
	}
	return cleaned, nil
}

// Unpack extracts the tar stream r into the container-absolute directory dest
// inside root, with docker cp TO-container semantics. dest must exist and be
// a directory. Existing files are overwritten; with NoOverwriteDirNonDir a
// directory is never replaced by a non-directory or vice versa. Mode and
// mtime are restored; uid/gid only when CopyUIDGID is set. Symlink entries
// are created verbatim without following; the location where each entry is
// created is confined to root via fs.RootPath.
func Unpack(root, dest string, r io.Reader, opts UnpackOptions) error {
	destHost, err := fs.RootPath(root, pathpkg.Clean("/"+filepath.ToSlash(dest)))
	if err != nil {
		return err
	}
	fi, err := os.Stat(destHost)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("destination %q: not a directory", dest)
	}
	cdest := pathpkg.Clean("/" + filepath.ToSlash(dest))

	type dirFixup struct {
		host  string
		mtime time.Time
	}
	var dirs []dirFixup

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		cleaned, err := cleanEntryName(hdr.Name)
		if err != nil {
			return err
		}
		if cleaned == "." {
			continue
		}
		hostDir, base, err := resolveParent(root, pathpkg.Join(cdest, cleaned))
		if err != nil {
			return err
		}
		hostPath := filepath.Join(hostDir, base)
		existing, lerr := os.Lstat(hostPath)
		if lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
			return lerr
		}
		entryIsDir := hdr.Typeflag == tar.TypeDir
		if existing != nil {
			if opts.NoOverwriteDirNonDir && existing.IsDir() != entryIsDir {
				return fmt.Errorf("cannot overwrite %q: directory/non-directory mismatch", cleaned)
			}
			switch {
			case existing.IsDir() && !entryIsDir:
				if err := os.RemoveAll(hostPath); err != nil {
					return err
				}
			case !existing.IsDir():
				// Remove instead of truncating so an existing symlink is
				// replaced rather than followed, and so a directory entry
				// can take the place of a file.
				if err := os.Remove(hostPath); err != nil {
					return err
				}
				existing = nil
			}
		}
		// Tolerate archives that omit intermediate directory entries.
		if err := os.MkdirAll(hostDir, 0o755); err != nil {
			return err
		}

		hmode := hdr.FileInfo().Mode() & preservedModeBits
		switch hdr.Typeflag {
		case tar.TypeDir:
			if existing == nil || !existing.IsDir() {
				if err := os.Mkdir(hostPath, hmode); err != nil {
					return err
				}
			}
			if err := os.Chmod(hostPath, hmode); err != nil {
				return err
			}
			dirs = append(dirs, dirFixup{host: hostPath, mtime: hdr.ModTime})
		case tar.TypeReg:
			f, err := os.OpenFile(hostPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, hmode)
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
			if err := os.Chmod(hostPath, hmode); err != nil {
				return err
			}
			if err := os.Chtimes(hostPath, hdr.ModTime, hdr.ModTime); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Created verbatim and never dereferenced during extraction; the
			// path where the link is created is already confined.
			if err := os.Symlink(hdr.Linkname, hostPath); err != nil {
				return err
			}
		case tar.TypeLink:
			// Hardlink targets resolve within the destination directory.
			lcleaned, err := cleanEntryName(hdr.Linkname)
			if err != nil {
				return err
			}
			tHostDir, tBase, err := resolveParent(root, pathpkg.Join(cdest, lcleaned))
			if err != nil {
				return err
			}
			if err := os.Link(filepath.Join(tHostDir, tBase), hostPath); err != nil {
				return err
			}
		default:
			// FIFOs, devices and other special entries are skipped silently.
			continue
		}
		if opts.CopyUIDGID {
			if err := os.Lchown(hostPath, hdr.Uid, hdr.Gid); err != nil && !errors.Is(err, os.ErrPermission) {
				return err
			}
		}
	}
	// Restore directory mtimes after all children have been created.
	for _, d := range dirs {
		if err := os.Chtimes(d.host, d.mtime, d.mtime); err != nil {
			return err
		}
	}
	return nil
}
