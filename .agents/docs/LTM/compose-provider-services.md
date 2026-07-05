# Compose Provider Services

## Summary

Cornus supports Compose `provider:` services by delegating their lifecycle to an
external plugin while preserving dependency ordering and environment injection.
Provider services have no image or deployment and appear as
`provider:<type>` in status output.

## Key Facts

- A provider is mutually exclusive with `image`, `build`, and `deploy`.
- The runtime resolves `docker-<type>` or `<type>` on `PATH` and invokes its
  Compose protocol with `up`, `down`, or `stop`.
- Newline-delimited JSON events include `info`, `debug`, `error`, `setenv`, and
  `rawsetenv`; non-JSON lines remain diagnostic output.
- Dependents wait on provider readiness. `setenv` keys are service-prefixed;
  `rawsetenv` is not; explicit dependent environment wins.
- Foreground reload and detached `--watch` re-run provider `up`, relying on
  provider idempotence.

## Details

Parsing lives in `pkg/compose`, while process execution and stream decoding live
in `cmd/cornus/internal/composecli/provider.go`. Provider state is held behind a
pointer so shallow runtime copies used during reload remain lock-free and
vet-clean.

Lifecycle parity was added after the first implementation: `stop` routes to the
plugin, `start` maps to `up`, and `restart` is `stop` followed by `up`.

## Files

- `pkg/compose/types.go` and `pkg/compose/provider_test.go` - schema and parsing.
- `cmd/cornus/internal/composecli/provider.go` - execution and protocol.
- `cmd/cornus/internal/composecli/provider_test.go` - helper-process tests.
- `docs/cli/compose.md` - user-facing provider section.

## Test Coverage

Tests cover parsing, option sorting, mutual exclusion, binary resolution, stream
events including `=` in values, environment precedence, error paths, and
lifecycle/reload behavior. A registered E2E scenario covers a real plugin.

## Pitfalls

- Provider output is a streaming protocol; do not assume every line is JSON.
- Reconciliation depends on plugin idempotence.
- Japanese and Simplified Chinese provider documentation still require sync.
