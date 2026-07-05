package lazyctx

import (
	"context"
	"fmt"
	"io"
	gofs "io/fs"

	"github.com/moby/buildkit/cache/contenthash"
	"github.com/opencontainers/go-digest"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
)

// FileDigest is a file's stat header plus its contenthash digest, computed
// producer-side (a local content read, no network). The build server feeds these
// to BuildKit's contenthash cache so the RUN cache-key scan of the (lazy) mount
// is skipped — no content pulled over the wire. Both fields are serializable
// (Stat is a protobuf), so the remote caller can compute and transmit them.
type FileDigest struct {
	Path   string        // slash-separated, relative to the context root
	Stat   *fstypes.Stat // the stat header contenthash hashes
	Digest digest.Digest // contenthash digest: NewFromStat(header) + content
}

// ComputeDigests walks dir and computes each entry's contenthash digest exactly
// as a BuildKit scan would (contenthash.NewFromStat header + content for regular
// files). This is the producer-side hash: it reads local content once so the
// build server never reads content over the wire. Runs where the files are
// local — in-process for a local build, or on the caller for a remote build.
func ComputeDigests(ctx context.Context, dir string, ignore Ignore) ([]FileDigest, error) {
	fsys, err := fsutil.NewFS(dir)
	if err != nil {
		return nil, err
	}
	var out []FileDigest
	err = fsys.Walk(ctx, "", func(p string, d gofs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if ignore != nil && ignore(p) {
			if d.IsDir() {
				return gofs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*fstypes.Stat)
		if !ok {
			return fmt.Errorf("lazyctx: %s: no fstypes.Stat", p)
		}
		h, err := contenthash.NewFromStat(stat)
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && info.Size() > 0 {
			rc, err := fsys.Open(p)
			if err != nil {
				return err
			}
			_, cerr := io.Copy(h, rc)
			rc.Close()
			if cerr != nil {
				return cerr
			}
		}
		out = append(out, FileDigest{Path: p, Stat: stat, Digest: digest.NewDigest(digest.SHA256, h)})
		return nil
	})
	return out, err
}
