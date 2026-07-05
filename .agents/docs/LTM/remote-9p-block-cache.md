# Remote 9P Block Cache And Writable Mount Performance

## Summary

Remote writable mounts use a block protocol between `ServeBlockProxy` and `ServeBlockServer`, layered over 9P and yamux. The cache keeps a 1 MiB addressing unit while optional sub-block coherence and demand fill dramatically improve database-shaped random I/O; bounded prefetch and concurrent caller operations remove high-latency and slow-storage bottlenecks.

## Key Facts

- Default behavior remains classic and unchanged. Enable the production database starting point on both endpoints: `CORNUS_BLOCK_COHERENCE=subhash,subfill` and `CORNUS_BLOCK_READAHEAD=64k` or a larger latency-oriented cap.
- `FeatSubBlockHash` preserves exact coherence cheaply; `FeatDeferHash` is optional and relaxes checking to fsync boundaries; `FeatSubBlockFill` implies sub-block hashing and fetches only a touched sub-range.
- `wire.BlockEnvOpts()` must be used at every proxy and caller `ServeBlock*` call site because HELLO negotiation intersects endpoint features.
- Reads and writes at the caller are bounded-concurrent (16 each); fsync and setattr drain writes as ordering barriers.

## Details

The original 1 MiB cache block gave random SQLite reads roughly 36x read amplification. Demand fill tracks sub-block presence in MemStore and DiskStore and introduces `opReadRange`, cutting cold random fetches to about 7 KiB/query (about 130x less data) while retaining the existing block and coherence model. `WriteThrough` and `HashRange` avoid copy-heavy RMW paths; full-unit writes hash the supplied buffer rather than reading it back.

Speculative prefetch keeps demand reads pure and asynchronously fills the next adaptive sequential range. `CORNUS_BLOCK_READAHEAD` is a cap for that prefetch distance: sequential reads grow it and jumps reset it, so random reads do not over-fetch. The mount has an eight-slot prefetch semaphore and single-flight dedup for identical demand/prefetch requests. A 2 ms-link SQLite scan improved from about 16.9 s to 0.62 s; classic mode benefits little because its whole-block fetches already need few round trips.

`blockServer.loop` concurrently dispatches read/range/stat and write requests while retaining a serial metadata path. Shared handle and sequence state uses `mu`; reply frames use `writeMu`; pooled scratch buffers avoid allocation amplification. Proxy-side per-block sequence admission and hash self-verification remain the coherence authority, so out-of-order same-block writes may drop and refetch but cannot become incorrect. Fsync/setattr wait for in-flight writes.

The in-process `pkg/wire/sqliteab` harness is the durable instrument: SQLite -> psanford VFS -> 9P -> block proxy -> yamux -> block server. It revealed allocation amplification (fixed), verified all feature combinations on memory and disk stores under `-race`, and showed the remaining ceiling under loss is TCP head-of-line blocking below yamux.

## Files

- `pkg/wire/blockserver.go` and block proxy code - protocol, coherence, caller concurrency, and prefetch.
- `pkg/blockcache/` - memory/disk stores, range presence, write-through, and hashing.
- `pkg/wire/sqliteab/` - real SQLite performance/correctness instrument.
- `pkg/deploy/.../ninep_backing.go` and other `ServeBlock*` sites - production feature wiring.

## Test Coverage

Run `go test -race ./pkg/wire ./pkg/blockcache`. Important tests include `TestBlockProxyFeatureModes`, `TestSQLiteCoherenceModes`, `TestSpeculativePrefetch`, `TestBlockProxyConcurrentWrites`, and `TestConcurrentCallerWriteHeavy`. The SQLite matrix covers insert, in-place update, reads, memory and disk stores, and all coherence/fill modes.

## Pitfalls

- Do not enable a feature at only one endpoint: negotiation will silently intersect it away.
- `FeatDeferHash` trades immediate coherence validation for fsync-boundary validation; prefer `subhash` for the normal production setting.
- Same-block concurrent writes are correct but can lose cache warmth. Per-sub-block sequence admission is a future optimization, not a correctness requirement.
- TCP loss causes head-of-line blocking that the block protocol cannot fix; it requires a lower transport change such as QUIC.
