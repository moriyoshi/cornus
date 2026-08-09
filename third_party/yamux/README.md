# yamux (vendored, modified)

A fork of [yamux](https://github.com/hashicorp/yamux), HashiCorp's stream
multiplexer, carrying cornus's per-stream QoS scheduling and send-path changes.
Reached through `replace github.com/hashicorp/yamux => ./third_party/yamux` in the
repository's `go.mod`.

## Provenance

| | |
|---|---|
| Upstream | https://github.com/hashicorp/yamux |
| Version | `v0.1.2` |
| License | Mozilla Public License 2.0 (see `LICENSE`) |
| Modified | yes |

## Changes from upstream

- `priority.go` (new) — per-stream send classes with strict priority for the
  control channel and weighted round-robin among data streams, plus a
  single-DATA-frame payload cap so one bulk frame cannot monopolise the send loop.
- `batched.go` (new) — a batched, pipelined send path: one `conn.Write` per frame
  and no synchronous per-frame wire round trip, bounded per stream so the QoS
  scheduler stays where frames queue.
- `session.go`, `stream.go`, `mux.go`, `const.go` — the `Config` fields the above
  are selected by (`SchedulerMode`, `SendMode`, `PipelineDepth`, `MaxDataFrame`),
  per-stream `SetPriority`/`SetMaxWindow`, and the wiring into the send path.

Rationale and measurements are in `.agents/docs/JOURNAL.md` and
`.agents/docs/LTM/yamux-qos-performance.md`; the A/B harnesses are
`pkg/wire/qosab` (in-process link) and `pkg/wire/qosab/netemab` (real TCP with
loss).

MPL-2.0 section 3.1 requires the source of modified files to be made available
under the same license; it is provided in-tree, and every file carries the
Exhibit A header.

## Updating

Re-copy upstream at the new tag, re-apply the changes above, then run the full
upstream suite plus the fork's own:

```
cd third_party/yamux && go test -race -short ./... && go test ./...
```
