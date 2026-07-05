package blockcache

// NullStore is a Store that retains nothing: every read reports absent and every
// write is discarded.
//
// It backs the block protocol's NO-CACHE mode. The block proxy's correctness does
// not depend on the cache — the CALLER is authoritative, reads miss-fill from it
// and writes are written through to it — so a store that never retains is a
// complete and correct implementation, just one that accelerates nothing.
//
// It exists so the block protocol can be used without provisioning cache storage.
// MemStore is unbounded, so it cannot serve that role for arbitrary-size mounts,
// and requiring an on-disk cache would make the protocol's availability depend on
// disk provisioning rather than on what the mount needs.
//
// Pairing: the proxy also skips the caching read/write paths entirely in this mode
// (blockProxyFile.cacheable stays false), so these methods are mostly not reached
// on the hot path. They must still be correct, because the paths that do NOT
// consult cacheable — Rename and unlink invalidation — call into the store
// unconditionally.
type NullStore struct{}

var _ Store = (*NullStore)(nil)

// NewNullStore returns a Store that caches nothing.
func NewNullStore() *NullStore { return &NullStore{} }

// Reads: never present, so every read falls through to the caller.
func (*NullStore) Get(FileID, int, []byte) (int, bool, error)           { return 0, false, nil }
func (*NullStore) GetSub(FileID, int, int64, []byte) (int, bool, error) { return 0, false, nil }
func (*NullStore) ChunkHash(FileID, int) (uint64, bool, error)          { return 0, false, nil }
func (*NullStore) HashRange(FileID, int, int64, int64) (uint64, bool, error) {
	return 0, false, nil
}

// Hint reports nothing known, so the proxy never concludes a cached entry is
// stale — there is no cached entry to be stale.
func (*NullStore) Hint(FileID) (int64, int64, bool, error) { return 0, 0, false, nil }

// Writes and bookkeeping: discarded. None can fail, so none returns an error;
// reporting one would fail a deploy over a cache that is not there by design.
func (*NullStore) Put(FileID, int, []byte) error                              { return nil }
func (*NullStore) PutSub(FileID, int, int64, []byte) error                    { return nil }
func (*NullStore) PutHashed(FileID, int, []byte, uint64) error                { return nil }
func (*NullStore) WriteChunk(FileID, int, int64, []byte, int64, uint64) error { return nil }
func (*NullStore) WriteThrough(FileID, int, int64, []byte, int64) error       { return nil }
func (*NullStore) SetHint(FileID, int64, int64) error                         { return nil }
func (*NullStore) Drop(FileID, int) error                                     { return nil }
func (*NullStore) Resize(FileID, int64) error                                 { return nil }
func (*NullStore) Invalidate(FileID) error                                    { return nil }
func (*NullStore) Rename(FileID, FileID) error                                { return nil }
func (*NullStore) Close() error                                               { return nil }
