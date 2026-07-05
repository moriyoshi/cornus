# Control-Plane API Namespace

## Summary

Cornus-specific HTTP endpoints live under the versioned `/.cornus/v1/*` namespace. This keeps the
control plane distinct from the OCI Distribution API at `/v2/*` and from conventional operational
endpoints, while leaving room for future incompatible API versions.

## Key Facts

- The pre-release `/api/*` surface was replaced with `/.cornus/v1/*` in one hard cutover. There is
  no compatibility alias; an old client receives 404 from a new server.
- Resource paths are `/.cornus/v1/{build,deploy,caretaker,hub,mount,gc,volume,info}`.
- `/healthz`, `/readyz`, and `/metrics` remain at their conventional paths. OCI `/v2/*` is unchanged.
- `/.cornus` is an ordinary path segment. `path.Clean` only treats exact `.` and `..` segments
  specially, so `net/http` does not redirect or rewrite it.
- The client-side web BFF uses the sibling namespace `/.cornus/web/*`, not the server API version
  namespace.

## Details

### Safe mechanical replacement

The repository contains both Cornus API paths and Kubernetes API paths. A blind replacement of
`/api/` would corrupt core Kubernetes paths such as `/api/v1/...`; `/apis/...` is a separate named
group prefix. The safe migration matched only Cornus resource tokens:

```text
/api/(build|deploy|caretaker|hub|mount|gc|volume|info)
```

Documentation sweeps additionally anchored the leading boundary so package paths such as
`pkg/api/deploy.go`, `k8s.io/api/...`, metadata endpoints, and user-configured bare `/api` ingress
examples stayed untouched. Cornus endpoint literals inside Kubernetes packages still had to move;
directory exclusion was not safe.

### Consumers that must move together

The paths are distributed literals rather than centralized constants. A namespace change must
update route registration, `TrimPrefix` parsing, auth exemptions and caretaker-scope checks,
telemetry route normalization, REST and WebSocket client construction, inter-replica forwarding,
caretaker dials, Kubernetes helpers, CLI defaults, E2E harness calls, scenarios, Helm comments, and
user/agent documentation.

Go 1.22 ServeMux specificity continues to select `/.cornus/v1/deploy/exec/` and attach routes ahead
of the broader `/.cornus/v1/deploy/` subtree. Runtime handler tests, rather than compilation alone,
are the evidence for route stability.

## Files

- `pkg/server/server.go` - control-plane route registration.
- `pkg/server/auth.go`, `pkg/server/observability.go` - path-sensitive authorization and telemetry.
- `pkg/client/client.go` - REST and WebSocket URL construction.
- `pkg/caretaker/`, `pkg/kubehub/`, `pkg/kubelogs/` - non-CLI consumers of server endpoints.
- `pkg/e2e/`, `e2e/scenarios/` - real protocol paths used by the harness.
- `ARCHITECTURE.md`, `README.md`, `docs/`, `.agents/docs/` - canonical and historical references.

## Test Coverage

`go test ./...` drives real `/.cornus/v1/*` handlers, including the leading-dot path and route
precedence. Repository-wide scoped searches verify that stale Cornus `/api/*` forms are absent while
Kubernetes `/api/v1` and `/apis/` paths remain intact. `npm run docs:build` catches invalid links,
but generated `docs/.vitepress/dist/` should be rebuilt instead of hand-edited.

## Pitfalls

- Never replace `/api/` globally; it will damage Kubernetes API paths.
- Do not move health, readiness, or metrics endpoints into the versioned namespace without an
  explicit compatibility plan for orchestrators and Prometheus.
- Do not add a silent legacy alias: the chosen pre-release contract is a hard versioned cutover.
- Comments, help text, fixtures, and agent memory can be protocol consumers or operator guidance;
  include them in scoped audits while preserving chronological journal text.
