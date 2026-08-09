# coder/websocket (vendored, modified)

A fork of [coder/websocket](https://github.com/coder/websocket) carrying one
addition: `DialOptions.WriteBufferSize`. Reached through
`replace github.com/coder/websocket => ./third_party/websocket` in the
repository's `go.mod`.

## Provenance

| | |
|---|---|
| Upstream | https://github.com/coder/websocket |
| Version | `v1.8.15` |
| Vendored on | 2026-08-09 |
| License | ISC (see `LICENSE.txt`) |
| Modified | yes |

Upstream's `README.md` is preserved as `README.upstream.md`.

## Why

RFC 6455 requires a WebSocket CLIENT to mask every byte it sends, and
`writeFramePayload` masks and flushes in chunks bounded by the connection's
`bufio.Writer` — hardcoded at bufio's 4096-byte default. So a client sending N
bytes issues `ceil(N/4096)` write syscalls **regardless of framing**: bigger
frames, fewer frames, and a larger yamux data-frame cap all change nothing.

In cornus the client is the deploy caller, which dials and then serves the mount
export, so the cost lands on read replies. A container reading 16 MiB from a
client-local mount cost the caller **4274 write syscalls**, about a third of all
syscall time on that path and 11-16% of its CPU. With the buffer sized to one
yamux frame that becomes **194**.

There is no way to reach the buffer through the exported API: no
`DialOptions`/`AcceptOptions` field, and `bufioWriterPool` is package-private and
only ever receives writers it created. Supplying a custom `http.Client`,
`Transport`, or a pre-buffered `net.Conn` does not help either — after a 101
upgrade `net/http` hands back `newReadWriteCloserBody(pc.br, pc.conn)`, so the
write half is the raw conn and anything supplied sits BELOW the 4 KiB buffer,
still seeing every flush.

Only the client side needs it: server writes are unmasked, take bufio's
large-write bypass, and already cost about two syscalls per frame.

## Changes from upstream

One file. `cornus.patch` is the full diff against pristine `v1.8.15`.

- `dial.go` — adds `DialOptions.WriteBufferSize`, threads it into
  `getBufioWriter`, and keeps custom-sized writers out of the shared pool (it is
  not size-segregated, so a custom writer entering it would be handed to the next
  connection that asked for a default).

`write.go` is untouched: masking is chunk-size independent because
`writeFramePayload` already threads the rotated key across chunks, so the bytes on
the wire are identical at any buffer size. That is what makes this fifteen lines
instead of a protocol change.

New tests: `writebuffersize_test.go` — that the size reaches the flush loop, that
the masked bytes are byte-identical across buffer sizes, and that a custom-sized
writer never enters the shared pool.

## Updating

Re-copy upstream at the new tag, re-apply `cornus.patch`, regenerate it, and run:

```
cd third_party/websocket && go test ./...
sh third_party/websocket/regen-patch.sh > third_party/websocket/cornus.patch
```

`go vet` reports two `mask_arm64.s` symbol warnings on arm64. They are upstream's
— pristine `v1.8.15` reports the same — so vet is not run over this module in CI.

**Watch for v1.9.0.** v1.8.15 ships SIMD masking assembly that is compiled but
disabled behind a `TODO: Will enable in v1.9.0` in `mask_asm.go`. Take it on a
routine bump, but it is not a substitute for this change and not worth waiting
for: masking is ~2.4% of the profile here, against ~16% for the flush loop.

## License

ISC, which imposes no obligation to mark modified files. They are marked anyway,
because the next reader needs to know which lines are not upstream's.
