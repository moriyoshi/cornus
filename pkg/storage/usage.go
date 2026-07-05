package storage

import (
	"context"
	"errors"
	"strings"
)

// Usage is a point-in-time snapshot of the registry CAS on-disk footprint.
type Usage struct {
	// Blobs is the number of content blobs under blobs/sha256/.
	Blobs int64
	// Bytes is their total size.
	Bytes int64
}

// Usage reports the current on-disk footprint of the registry CAS: the number of
// content blobs and their total byte size. It lists blobs/sha256/ and Stats each
// blob, so it costs O(blob count) store round-trips — cheap on the filesystem
// backend, but expensive at scale on S3 (a HEAD per object). It is meant for an
// occasional operator query (a non-destructive disk-usage surface, unlike the
// destructive GC), not a hot path.
//
// A blob that disappears between the List and its Stat (a concurrent GC or blob
// DELETE) is simply skipped — a usage snapshot need not be transactional — so the
// count never fails just because reclamation is running alongside it.
func (b *Backend) Usage(ctx context.Context) (Usage, error) {
	keys, err := b.obj.List(ctx, "blobs/sha256/", "")
	if err != nil {
		return Usage{}, err
	}
	var u Usage
	for _, key := range keys {
		if strings.HasSuffix(key, "/") {
			continue // a rolled-up directory prefix, not an object
		}
		size, err := b.obj.Stat(ctx, key)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue // deleted between List and Stat; not an error for a snapshot
			}
			return Usage{}, err
		}
		u.Blobs++
		u.Bytes += size
	}
	return u, nil
}
