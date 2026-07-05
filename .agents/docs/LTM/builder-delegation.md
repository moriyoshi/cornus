# Containerized Builder Delegation

## Summary

An unprivileged Cornus server delegates build HTTP and attach traffic to a
privileged builder server. When no explicit builder is configured and a local
container runtime is available, Cornus can build a builder image from its own
running binary and auto-spawn the helper on first build.

## Key Facts

- BuildKit cannot run merely because the process is uid 0; usable mount
  capability is probed with a real bind mount.
- `CORNUS_BUILDER_URL` selects an external builder; relay authorization happens
  before contacting it.
- Attach traffic is raw-spliced; POST bodies are streamed rather than buffered.
- The automatic builder is lazy, supervised, privileged, and uses host
  networking so build exports can reach the delegating server.
- The image is built from the current executable, not pulled, which preserves
  version and feature-tag agreement.
- The builder must mirror the delegating server's resolved registry mode; an
  independent default can make build export and registry acceptance disagree.

## Details

The first cut deliberately separated delegation from lifecycle management. The
follow-up added `builderctr`, capability probing, image construction from the
running executable, and supervised auto-spawn. This keeps privileged fallback
visible in server behavior while avoiding a second distributed build protocol.

The headline debugging lesson was to distinguish an unprivileged BuildKit
failure from later registry/data-directory failures. A poisoned or unwritable
data directory can surface differently at startup, registry write, and builder
export layers.

## Files

- `pkg/server/build_relay.go` - forwarding.
- `pkg/builderctr` - capability probe, image build, and lifecycle.
- `cmd/cornus/serve.go` - configuration and lazy startup.
- `pkg/server/build_relay_test.go` - transport and authorization tests.

## Test Coverage

Unit tests cover URL normalization, authorization order, streaming POST, raw
attach relay, capability probing, and registry-mode propagation. The complete
path was also exercised with a non-root server and privileged containerized
builder.

## Pitfalls

- Euid is not a capability test.
- Do not pull a nominally matching builder image when the running binary may be
  newer or differently tagged.
- Builder and delegating server registry defaults must be resolved once and
  propagated.
