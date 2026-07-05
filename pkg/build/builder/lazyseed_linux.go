//go:build linux

package builder

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/snapshots"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/contenthash"
	"github.com/moby/buildkit/session"
	"github.com/opencontainers/go-digest"
	"github.com/tonistiigi/fsutil"

	"cornus/pkg/build/internal/lazyctx"
)

// hashedFileInfo is an os.FileInfo (via fsutil.StatInfo) that also carries a
// precomputed content digest — the `Hashed` interface contenthash.HandleChange
// reads instead of opening the file.
type hashedFileInfo struct {
	*fsutil.StatInfo
	dgst digest.Digest
}

func (h *hashedFileInfo) Digest() digest.Digest { return h.dgst }

// seedContentHash pre-populates BuildKit's contenthash cache for ref from
// producer-computed digests (local build: computed in-process; remote build:
// sent by the caller). BuildKit's later Checksum then finds every path cached and
// skips the mount scan, so the RUN cache key costs no content transfer over 9p.
func seedContentHash(ctx context.Context, ref cache.RefMetadata, digests []lazyctx.FileDigest) error {
	cc, err := contenthash.GetCacheContext(ctx, ref)
	if err != nil {
		return err
	}
	for _, fd := range digests {
		hfi := &hashedFileInfo{StatInfo: &fsutil.StatInfo{Stat: fd.Stat}, dgst: fd.Digest}
		if err := cc.HandleChange(fsutil.ChangeKindAdd, "/"+fd.Path, hfi, nil); err != nil {
			return err
		}
	}
	return contenthash.SetCacheContext(ctx, ref, cc)
}

// preseedLazyRefs force-creates each lazy layer's ref via GetByBlob and returns
// the held refs (caller must Release them AFTER Solve, so they are not GC'd).
// GetByBlob is idempotent (content-addressed), so the solve's own GetByBlob
// returns the same record. Mirrors source/containerimage/pull.go's call:
// same layer descriptor, a DescHandler with a Provider (never invoked for a
// remote snapshot) and the layer's inherited SnapshotLabels (carrying LazyLabel).
func (e *Engine) preseedLazyRefs(ctx context.Context, lazy []*lazyctx.LazyContext) ([]cache.ImmutableRef, error) {
	var held []cache.ImmutableRef
	release := func() {
		for _, r := range held {
			_ = r.Release(context.WithoutCancel(ctx))
		}
	}
	for _, lc := range lazy {
		dh := cache.DescHandlers{
			lc.LayerDesc.Digest: &cache.DescHandler{
				Provider:       func(session.Group) content.Provider { return lc.Store },
				SnapshotLabels: snapshots.FilterInheritedLabels(lc.LayerDesc.Annotations),
				Ref:            lc.Ref,
			},
		}
		ref, err := e.cacheMgr.GetByBlob(ctx, lc.LayerDesc, nil, dh, cache.WithImageRef(lc.Ref))
		if err != nil {
			release()
			return nil, fmt.Errorf("builder: preseed lazy ref %q: %w", lc.Name, err)
		}
		held = append(held, ref)
		// Producer-side digests: use the caller's if provided (remote build),
		// else compute them locally (local build).
		digests := lc.Digests
		if digests == nil && lc.Dir != "" {
			digests, err = lazyctx.ComputeDigests(ctx, lc.Dir, lc.Ignore)
			if err != nil {
				release()
				return nil, fmt.Errorf("builder: compute digests %q: %w", lc.Name, err)
			}
		}
		if len(digests) > 0 {
			if err := seedContentHash(ctx, ref, digests); err != nil {
				release()
				return nil, fmt.Errorf("builder: seed contenthash %q: %w", lc.Name, err)
			}
		}
	}
	return held, nil
}
