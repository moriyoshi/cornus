# p9 (vendored, modified)

A fork of [p9](https://github.com/hugelgupf/p9), a pure-Go 9P2000.L client and
server, carrying one change: the server serves each Twrite's payload from the
receiving connection's buffer pool instead of allocating a fresh buffer per
message. Reached through `replace github.com/hugelgupf/p9 => ./third_party/p9` in
the repository's `go.mod`.

## Provenance

| | |
|---|---|
| Upstream | https://github.com/hugelgupf/p9 |
| Version | `v0.4.1` |
| Vendored on | 2026-08-09 |
| License | Apache License 2.0 (see `LICENSE`) |
| Modified | yes |

Upstream's `README.md` is preserved as `README.upstream.md`.

## Why

The server allocated one payload-sized buffer for every Twrite. On cornus's
client-local mount path an allocation profile attributed **69% of the write leg's
allocated bytes** to it — roughly one megabyte of garbage per megabyte written,
paid by both mount protocols, since each runs a p9 server somewhere.

The reuse machinery was already there and simply never fired: `recv` keeps a
payload buffer when the message already has one of the right size
(`transport.go`), but `registry.put` nils the payload before returning the message
struct to its cache (`messages.go`), so every message arrived with `Data == nil`.

Upstream had already solved the same problem in the other direction: `tread`
borrows an msize-shaped buffer from `connState.readBufPool` and returns it in
`rreadServerPayloader.PayloadCleanup`. This fork is that pattern applied to the
write direction.

## Changes from upstream

Four files, one of them documentation only. `cornus.patch` is the full diff
against pristine `v0.4.1`.

- `p9/messages.go` — a `payloadAllocator` interface; `twrite` gains
  `allocPayload`/`releasePayload` and the two fields they need; `registry.put`
  routes payload-owning messages through `releasePayload`.
- `p9/transport.go` — `recv` asks a payload-owning message for its buffer. The
  fallback arm is upstream verbatim.
- `p9/server.go` — `connState.lookup` binds a received Twrite to its connection.
- `p9/file.go` — documents that `File.WriteAt` must not retain `p`, which is the
  contract the reuse depends on (and what `io.WriterAt` already states).

New tests: `p9/twrite_payload_test.go`.

### Why it is safe

- **Per connection, never global.** Buffers come from and return to
  `connState.readBufPool`. The message cache in `registry` is process-global and
  shared across connections, so `releasePayload` clears the payload *and* the
  connection reference before a message can re-enter it.
- **Bounded to its own request.** `allocPayload` returns `buf[:n:n]`, so a `File`
  implementation cannot reslice or append into bytes an earlier request left
  behind, and `recv` fills `[0,n)` completely before decode or any handler runs
  (`vecnet.Buffers.ReadFrom` reads until every buffer is full).
- **No zero-fill needed.** The read path zeroes on release because a `File.ReadAt`
  may return `n` without having filled `n` bytes. p9 fills the write payload
  itself, so the analogous hole does not exist.
- **Only `*twrite`.** It is the one payloader both decoded on the receive path and
  drawn from the registry. Response payloaders decode into a *caller's* buffer on
  the client, which must never be resized or reused.

Scoped this way the change adds no exported API, which is also why it is worth
offering upstream; if it lands there, delete this directory and the `replace`.

## Updating

Re-copy upstream at the new tag, re-apply `cornus.patch`, regenerate it, and run:

```
cd third_party/p9 && go test ./p9/... ./linux/... ./vecnet/...
```

`fsimpl/localfs`'s tests do not build under Go 1.26 on `v0.4.1` — its test
dependency `golang.org/x/tools@v0.16.1` has a constant expression the current
compiler rejects. That is upstream's, not this fork's: pristine `v0.4.1` fails
identically. Hence the package list above rather than `./...`.

## License

Apache-2.0. Section 4(b) requires modified files to carry prominent notices
stating that they were changed; each of the four files above does, beside its
existing header. Upstream ships no `NOTICE` file, so section 4(d) does not apply.
