//go:build linux

package incushost

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	pathpkg "path"

	incus "github.com/lxc/incus/v6/client"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// cp for the incus backend rides the Incus instance file API (GetInstanceFile /
// CreateInstanceFile, exposed through the incusConn seam) and translates to and
// from Docker's archive tar so the shape matches the other backends: a directory
// packs as a top entry named path's basename with its tree underneath; a regular
// file packs as a single entry named the basename (see the tarcopy contract).
//
// Integration against a live incusd is E2E-gated (make e2e-incus); the tar
// translation itself is unit-tested against a filesystem-modelling fake conn.

// StatPath returns metadata for path inside the deployment's first instance
// (docker cp / archive HEAD).
func (b *Backend) StatPath(ctx context.Context, name, path string) (api.PathStat, error) {
	id, err := b.firstInstance(name)
	if err != nil {
		return api.PathStat{}, err
	}
	st, _, err := b.statPath(id, path)
	return st, err
}

// statPath fetches path's metadata (and, for a regular file, its size — the file
// API carries no length, so the content is measured). It returns the PathStat
// plus the file response so CopyFrom can reuse the type/entries without a second
// round trip.
func (b *Backend) statPath(id, path string) (api.PathStat, *incus.InstanceFileResponse, error) {
	rc, resp, err := b.conn.GetFile(id, path)
	if err != nil {
		if isIncusNotFound(err) {
			return api.PathStat{}, nil, fmt.Errorf("incus: %q not found in %s: %w", path, id, deploy.ErrNotFound)
		}
		return api.PathStat{}, nil, fmt.Errorf("incus: stat %q: %w", path, err)
	}
	defer rc.Close()
	var size int64
	if resp.Type == "file" {
		n, err := io.Copy(io.Discard, rc)
		if err != nil {
			return api.PathStat{}, nil, fmt.Errorf("incus: reading %q: %w", path, err)
		}
		size = n
	}
	return api.PathStat{
		Name: pathpkg.Base(pathpkg.Clean("/" + path)),
		Size: size,
		Mode: incusFileMode(resp),
	}, resp, nil
}

// incusFileMode renders an InstanceFileResponse's permission bits + type as the
// uint32 os.FileMode encoding the PathStat/tar headers use.
func incusFileMode(resp *incus.InstanceFileResponse) uint32 {
	m := os.FileMode(resp.Mode) & os.ModePerm
	switch resp.Type {
	case "directory":
		m |= os.ModeDir
	case "symlink":
		m |= os.ModeSymlink
	}
	return uint32(m)
}

// CopyFrom writes a Docker-format tar of path from the deployment's first
// instance to w and returns the path's stat (docker cp from container / archive
// GET).
func (b *Backend) CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error) {
	id, err := b.firstInstance(name)
	if err != nil {
		return api.PathStat{}, err
	}
	top, resp, err := b.statPath(id, path)
	if err != nil {
		return api.PathStat{}, err
	}
	tw := tar.NewWriter(w)
	if err := b.packEntry(id, path, top.Name, resp, tw); err != nil {
		return api.PathStat{}, err
	}
	if err := tw.Close(); err != nil {
		return api.PathStat{}, err
	}
	return top, nil
}

// packEntry writes srcPath (already stat'd as resp) into tw under the tar name
// tarName, recursing into directory entries. The top-level call passes the stat
// from statPath; recursive calls fetch each child.
func (b *Backend) packEntry(id, srcPath, tarName string, resp *incus.InstanceFileResponse, tw *tar.Writer) error {
	switch resp.Type {
	case "directory":
		hdr := &tar.Header{Name: tarName + "/", Typeflag: tar.TypeDir, Mode: int64(resp.Mode & 0o777)}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		for _, entry := range resp.Entries {
			childPath := pathpkg.Join(srcPath, entry)
			crc, cresp, err := b.conn.GetFile(id, childPath)
			if err != nil {
				return fmt.Errorf("incus: reading %q: %w", childPath, err)
			}
			content, rerr := io.ReadAll(crc)
			crc.Close()
			if rerr != nil {
				return fmt.Errorf("incus: reading %q: %w", childPath, rerr)
			}
			if err := b.packOne(id, childPath, pathpkg.Join(tarName, entry), cresp, content, tw); err != nil {
				return err
			}
		}
		return nil
	default:
		crc, cresp, err := b.conn.GetFile(id, srcPath)
		if err != nil {
			return fmt.Errorf("incus: reading %q: %w", srcPath, err)
		}
		content, rerr := io.ReadAll(crc)
		crc.Close()
		if rerr != nil {
			return fmt.Errorf("incus: reading %q: %w", srcPath, rerr)
		}
		return b.packOne(id, srcPath, tarName, cresp, content, tw)
	}
}

// packOne writes a single already-fetched entry (recursing for directories).
func (b *Backend) packOne(id, srcPath, tarName string, resp *incus.InstanceFileResponse, content []byte, tw *tar.Writer) error {
	if resp.Type == "directory" {
		return b.packEntry(id, srcPath, tarName, resp, tw)
	}
	hdr := &tar.Header{
		Name:     tarName,
		Typeflag: tar.TypeReg,
		Mode:     int64(resp.Mode & 0o777),
		Size:     int64(len(content)),
	}
	if resp.Type == "symlink" {
		hdr.Typeflag = tar.TypeSymlink
		hdr.Linkname = string(content)
		hdr.Size = 0
		content = nil
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(content)
	return err
}

// ownerFor returns the uid/gid to stamp on an extracted entry: the archive's
// own owner when opts.CopyUIDGID is set (docker cp --archive), else 0/0 (root),
// matching the docker cp default of chowning to the container's effective user.
func ownerFor(hdr *tar.Header, copyUIDGID bool) (int64, int64) {
	if copyUIDGID {
		return int64(hdr.Uid), int64(hdr.Gid)
	}
	return 0, 0
}

// CopyTo extracts the tar read from r into path inside the deployment's first
// instance (docker cp into container / archive PUT). path is the destination
// directory; each tar entry is created relative to it via CreateInstanceFile.
func (b *Backend) CopyTo(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error {
	id, err := b.firstInstance(name)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("incus: reading archive: %w", err)
		}
		dest := pathpkg.Join(path, pathpkg.Clean("/"+hdr.Name))
		uid, gid := ownerFor(hdr, opts.CopyUIDGID)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := b.conn.CreateFile(id, dest, incus.InstanceFileArgs{
				Type: "directory",
				Mode: int(hdr.Mode),
				UID:  uid,
				GID:  gid,
			}); err != nil {
				return fmt.Errorf("incus: creating dir %q: %w", dest, err)
			}
		case tar.TypeReg:
			content, err := io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("incus: reading archive entry %q: %w", hdr.Name, err)
			}
			if err := b.conn.CreateFile(id, dest, incus.InstanceFileArgs{
				Content: bytes.NewReader(content),
				Type:    "file",
				Mode:    int(hdr.Mode),
				UID:     uid,
				GID:     gid,
			}); err != nil {
				return fmt.Errorf("incus: writing file %q: %w", dest, err)
			}
		case tar.TypeSymlink:
			if err := b.conn.CreateFile(id, dest, incus.InstanceFileArgs{
				Content: bytes.NewReader([]byte(hdr.Linkname)),
				Type:    "symlink",
				Mode:    int(hdr.Mode),
			}); err != nil {
				return fmt.Errorf("incus: writing symlink %q: %w", dest, err)
			}
		}
	}
}
