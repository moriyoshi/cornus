# Workload Lineage and Project Attribution

## Summary

Cornus records structured workload origin metadata so the web BFF can attribute
live workloads to Compose projects without treating names as trusted structure.
The BFF joins backend status with locally loaded project definitions; origin can
name an unloaded project, but it cannot fabricate that project's graph or mount
model.

## Key Facts

- `api.Origin` is a structured trust boundary, not a display string.
- Backend persistence differs: Docker labels, Kubernetes annotations, and
  host-backend records/config each have their own established shape.
- Project sections are built only from projects loaded by the local BFF.
- An origin-attributed workload from an unloaded project keeps its project name
  but belongs in the Overview's `Other` group.
- Mounts cannot be reconstructed uniformly: the server does not persist
  `DeploySpec`, backends retain different realized data, and client-local sources
  have been rewritten to 9P mountpoints.

## Details

The initial lineage path carried origin through deploy and backend status. The
BFF then joined status with the local Compose project inventory. A follow-up
exposed origin attribution for non-project workloads without claiming that an
unloaded project had been loaded.

Adding mounts for arbitrary workloads is a separate backend-observation feature:
it requires extending `DeployStatus`, implementing per-backend observation, and
accepting uneven fidelity. It is not a free use of stored deployment specs
because Cornus is imperative and intentionally has no revision/spec store.

## Files

- `pkg/api` - `Origin` and status types.
- `pkg/deploy/*` - backend-specific metadata persistence.
- `cmd/cornus/internal/webbff` - local-project/status join.
- `web/src/views/Workloads.tsx` - lineage presentation.

## Test Coverage

Backend tests cover metadata round trips. BFF and SPA fixtures cover loaded
projects, origin-attributed unloaded projects, and workloads without lineage.

## Pitfalls

- Do not infer project identity from deployment-name conventions.
- Attribution is not proof that the BFF knows the project's current Compose
  definition.
- Backend mount inspection would expose realized paths, not necessarily the
  caller's original paths.
