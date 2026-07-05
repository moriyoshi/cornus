# Activity Flight Recorder

## Summary

`pkg/activity` writes durable NDJSON begin/end records for server, caretaker, and
supervised-child work. Its primary diagnostic is absence: a begin record without
a matching end identifies work interrupted by a crash, SIGKILL, or lost process.

## Key Facts

- Process lifetime is itself an activity, so clean and unclean shutdown are
  distinguishable.
- Records are append-only NDJSON with shared ids for begin/end pairs.
- `cornus activity`, `GET /.cornus/v1/activity`, SSE `?follow=1`, MCP
  `activity_read`, and `cornus://activity/unfinished` read the same data.
- `activity.Tailer.Next` performs the initial history read and subsequent tail in
  one cursor, avoiding a history-to-follow race.
- `--follow --unfinished` is invalid: unfinished activity is a whole-stream
  snapshot, not an event feed.
- Host-backend caretaker scratch directories bind into the server data dir;
  Kubernetes caretaker records still need shipping over the caretaker connection.

## Details

The recorder was motivated by stranded 9P mounts, but it records general process
and supervised-child lifecycle rather than mount-specific state. The E2E kills a
containerized privileged server while a real 9P mount exists, proves the mount is
stranded, then proves the next server recovers it from the record.

That scenario exposed a harness hazard: `deploy_attach` waits by workload name,
so a workload left by a previous failed run can satisfy readiness immediately.
Scenarios that intentionally kill resources must perform poison cleanup at the
start because Starlark has no `defer`.

## Files

- `pkg/activity` - writer, reader, tailer, and unfinished resolution.
- `pkg/server/activity_http.go` - HTTP/SSE surface.
- `cmd/cornus/activity.go` - CLI.
- `cmd/cornus/internal/webbff` - MCP tool/resource.
- `e2e/scenarios/activity-flight-record.star` - crash recovery.

## Test Coverage

Unit tests cover append/read, pairing, truncation tolerance, tailing, and
supervised-child lifecycle. The privileged Docker E2E mutation-checks real mount
recovery.

## Pitfalls

- Cleanup at the end of a destructive scenario is insufficient after failure.
- A mutation that does not compile is not a valid mutation check.
- Root-run containerized servers can leave root-owned E2E artifacts.
