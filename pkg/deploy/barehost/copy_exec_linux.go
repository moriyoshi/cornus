//go:build linux

package barehost

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	pathpkg "path"
	"strings"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
)

// Copy on sandboxed runtimes (gVisor/runsc). The cgroupfs-runtime copy path
// resolves the container rootfs through the init's /proc/<pid>/root and reads it
// directly from the host (copy_linux.go). Under gVisor the guest filesystem is
// served by the sentry/gofer and is NOT visible at /proc/<pid>/root, so that
// path would read the sandbox process's own root, not the container's. Instead
// these functions run `tar` INSIDE the container over an exec and stream the
// archive across the runtime boundary — the same mechanism `docker cp` falls
// back to for userns/remote cases.
//
// Limitations (documented, sandboxed-runtime only — the default runc/crun/youki
// path is unaffected):
//   - The image must provide a `tar` binary. Scratch/distroless images without
//     one cannot be copied to/from under gVisor.
//   - CopyToOptions.NoOverwriteDirNonDir is best-effort: it is not translated to
//     a portable tar flag, so extraction follows the guest tar's default
//     overwrite behavior. CopyUIDGID is honored via `--no-same-owner` when unset.
//   - Directory Size in a StatPath result comes from the tar header (0), not the
//     host dir size; docker clients do not rely on it.

// tarPackDirBase splits a container-absolute path into the parent directory to
// run `tar -C` from and the base entry to archive, so the archive is rooted at
// the base name (docker semantics). The container root archives ".".
func tarPackDirBase(p string) (dir, base string) {
	cp := pathpkg.Clean("/" + p)
	if cp == "/" {
		return "/", "."
	}
	return pathpkg.Dir(cp), pathpkg.Base(cp)
}

// tarPackArgs builds the in-container `tar` command that streams path as an
// archive on stdout.
func tarPackArgs(path string) []string {
	dir, base := tarPackDirBase(path)
	return []string{"tar", "-C", dir, "-cf", "-", "--", base}
}

// tarExtractArgs builds the in-container `tar` command that extracts the archive
// on stdin into path. --no-same-owner maps CopyUIDGID=false (do not restore the
// archive's uid/gid); NoOverwriteDirNonDir has no portable tar equivalent and is
// left to the guest tar's default behavior.
func tarExtractArgs(path string, opts api.CopyToOptions) []string {
	args := []string{"tar"}
	if !opts.CopyUIDGID {
		args = append(args, "--no-same-owner")
	}
	args = append(args, "-C", path, "-xf", "-")
	return args
}

// statFromTarHeader synthesizes a docker PathStat from the first tar header of a
// pack stream. Mode comes from tar's FileInfo (Go os.FileMode bits, matching the
// cgroupfs path's tarcopy.Stat); Name is the base of the requested path.
func statFromTarHeader(reqPath string, hdr *tar.Header) api.PathStat {
	st := api.PathStat{
		Name:  pathpkg.Base(pathpkg.Clean("/" + reqPath)),
		Size:  hdr.Size,
		Mode:  uint32(hdr.FileInfo().Mode()),
		Mtime: hdr.ModTime.UTC().Format(time.RFC3339Nano),
	}
	if hdr.Typeflag == tar.TypeSymlink {
		st.LinkTarget = hdr.Linkname
	}
	return st
}

// execTarProcess builds the exec process spec for an in-container tar run,
// inheriting the container's env (so tar is found on PATH) but forcing uid 0 so
// copy can read/write anywhere, mirroring docker cp's root-in-container behavior.
func (b *Backend) execTarProcess(rec *instanceRecord, args []string) (*specs.Process, error) {
	base, err := readBundleConfig(rec.BundleDir)
	if err != nil {
		return nil, err
	}
	return execProcessSpec(base, api.ExecConfig{Cmd: args, WorkingDir: "/", User: "0"})
}

// statPathViaExec runs tar in the container and reads only the first header to
// build the PathStat, cancelling the exec once it has what it needs so a large
// directory is not fully archived.
func (b *Backend) statPathViaExec(ctx context.Context, name, path string) (api.PathStat, error) {
	rec, _, err := b.runningInstance(ctx, name)
	if err != nil {
		return api.PathStat{}, err
	}
	pspec, err := b.execTarProcess(rec, tarPackArgs(path))
	if err != nil {
		return api.PathStat{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	go func() {
		eio := &copyIO{stdout: pw, stderr: io.Discard}
		pw.CloseWithError(b.rt.Exec(ctx, rec.ID, *pspec, runtimeExecOpts{IO: eio}))
	}()
	hdr, err := tar.NewReader(pr).Next()
	if err != nil {
		return api.PathStat{}, fmt.Errorf("bare: stat %q via in-container tar: %w (is `tar` present in the image?)", path, err)
	}
	st := statFromTarHeader(path, hdr)
	cancel()                       // stop tar; we only needed the first header
	_, _ = io.Copy(io.Discard, pr) // let the exec goroutine drain and exit
	return st, nil
}

// copyFromViaExec runs tar in the container and streams the full archive to w,
// tee-ing the bytes through a tar.Reader so the first header yields the PathStat
// without consuming the stream (w still receives the complete archive).
func (b *Backend) copyFromViaExec(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error) {
	rec, _, err := b.runningInstance(ctx, name)
	if err != nil {
		return api.PathStat{}, err
	}
	pspec, err := b.execTarProcess(rec, tarPackArgs(path))
	if err != nil {
		return api.PathStat{}, err
	}
	pr, pw := io.Pipe()
	go func() {
		eio := &copyIO{stdout: pw, stderr: io.Discard}
		pw.CloseWithError(b.rt.Exec(ctx, rec.ID, *pspec, runtimeExecOpts{IO: eio}))
	}()
	tee := io.TeeReader(pr, w)
	hdr, err := tar.NewReader(tee).Next()
	if err != nil {
		return api.PathStat{}, fmt.Errorf("bare: copy from %q via in-container tar: %w (is `tar` present in the image?)", path, err)
	}
	st := statFromTarHeader(path, hdr)
	// Drain the rest through the tee so w receives the whole archive (the header
	// bytes were already mirrored while parsing hdr above).
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return st, fmt.Errorf("bare: copy from %q via in-container tar: %w", path, err)
	}
	return st, nil
}

// copyToViaExec runs tar in the container with the incoming archive on stdin,
// extracting it into path. tar's stderr is captured for a useful error on
// failure (e.g. a missing tar binary or a non-existent destination directory).
func (b *Backend) copyToViaExec(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error {
	rec, _, err := b.runningInstance(ctx, name)
	if err != nil {
		return err
	}
	pspec, err := b.execTarProcess(rec, tarExtractArgs(path, opts))
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	eio := &copyIO{stdin: r, stderr: &stderr}
	execErr := b.rt.Exec(ctx, rec.ID, *pspec, runtimeExecOpts{IO: eio})
	if code := execExitCode(execErr); code != 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "is `tar` present in the image and the destination a directory?"
		}
		return fmt.Errorf("bare: copy into %q via in-container tar failed (exit %d): %s", path, code, msg)
	}
	return nil
}

// copyIO wires an exec's stdio for the copy paths: stdin from an io.Reader (the
// incoming archive), stdout/stderr to io.Writers os/exec drains before Wait
// returns. Any nil stream is left at the runtime's default. It implements
// go-runc's IO the same way execPipeIO does.
type copyIO struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (c *copyIO) Stdin() io.WriteCloser { return nil }
func (c *copyIO) Stdout() io.ReadCloser { return nil }
func (c *copyIO) Stderr() io.ReadCloser { return nil }
func (c *copyIO) Set(cmd *exec.Cmd) {
	if c.stdin != nil {
		cmd.Stdin = c.stdin
	}
	if c.stdout != nil {
		cmd.Stdout = c.stdout
	}
	if c.stderr != nil {
		cmd.Stderr = c.stderr
	}
}
func (c *copyIO) Close() error { return nil }
