package blockcache

import "github.com/zeebo/xxh3"

// hashChunk is the per-chunk content hash used as the block cache's coherence
// token. xxh3-64 is a fast non-cryptographic change detector (multi-GB/s); the
// caller is authoritative and trusted, so collision resistance against an
// adversary is not required. A stored hash of 0 conventionally means "unknown"
// (never computed for this chunk yet), so a genuine chunk that happens to hash to
// 0 is indistinguishable from unknown — harmless, as "unknown" only forces a
// (correct) refetch/revalidation, never a stale serve.
func hashChunk(b []byte) uint64 { return xxh3.Hash(b) }

// HashChunk exposes the block-cache content-hash primitive so the wire block
// endpoints (pkg/wire) compute byte-identical per-block hashes. The coherence
// token MUST match on both sides, so this is the single source of truth for the
// algorithm.
func HashChunk(b []byte) uint64 { return hashChunk(b) }
