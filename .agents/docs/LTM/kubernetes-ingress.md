# Kubernetes Ingress

## Summary

Cornus can create a declarative `networking.k8s.io/v1` Ingress next to a Kubernetes deployment's ClusterIP Service. Kubernetes realizes the feature, while dockerhost and containerd warn and ignore it, allowing portable specifications with Kubernetes-specific exposure.

## Key Facts

- `api.DeploySpec.Ingress *IngressSpec` has `Enabled`, `Hosts`, `Domain`, `Subdomain`, `Path`, `PathType`, `Port`, `ClassName`, `Annotations`, and TLS secret/issuer fields.
- `@` is the base-domain apex token, and Kubernetes emits one rule per resolved host over the shared Service.
- Services opt in with `x-cornus-ingress`; a project-level block field-merges domain, class, and issuer defaults but never exposes every service.
- `CORNUS_INGRESS_DOMAIN`, `_CLASS`, and `_TLS_ISSUER` are defaults. `CORNUS_INGRESS_ENFORCE_DOMAIN` is the optional multi-tenant policy boundary.

## Details

`IngressSpec.Validate` validates DNS names and path-type syntax while leaving controller-specific checks to the backend. `(*kubernetes.Backend).ingress` reconciles through Get -> set `ResourceVersion` -> Update or Create, and an owner reference lets Kubernetes GC remove the Ingress with its Deployment. TLS supports an explicit secret or a cluster issuer annotation; fake clientsets test owner-reference wiring but cannot execute GC.

### Compose translation and host derivation

Service ingress accepts an object, `{}`, or `true` through custom `UnmarshalJSON`; scalar `host` is unioned with `hosts`. `LoadWithOptions` must copy `p.Ingress` when merging projects or top-level defaults silently disappear. The backend joins the server-owned base domain, deriving from `Subdomain` or name. Compose supplies `<service>.<project>` so `web.pr-123.<domain>` is unambiguous; `sanitizeSubdomain` normalizes each label, while raw `deploy -f` falls back to `<name>.<domain>`.

### Defaults and policy

Helm supplies server defaults, but a workload may override domain, class, or issuer. Domain enforcement rejects resolved hosts outside the configured base domain. Per-project dotted hosts may need a per-host issuer or project wildcard because `*.<domain>` does not cover `web.project.<domain>`.

## Files

- `pkg/api/deploy.go`, `pkg/compose/`, and `pkg/deploy/kubernetes/` - API, Compose extension/merge, and object realization.
- `pkg/server/` and `deploy/helm/cornus/` - environment defaults, policy, and Helm wiring.
- `pkg/e2e/` and `e2e/scenarios/deploy-ingress.star` - `deploy(ingress=...)` support and E2E coverage.

## Test Coverage

- API, Compose, and Kubernetes tests cover validation, enablement, inheritance, host/domain policy, TLS, port selection, idempotency, and subdomain sanitization.
- `deploy-ingress.star` is registered for `make e2e-kube` and resolves under `cornus-e2e --check`; it covers derivation, explicit TLS/path, apex/multiple hosts, and owner-reference cleanup.

## Pitfalls

- The ingress-nginx CI leg performs a live controller fetch; object-only kube runs
  retain shape assertions without pretending to test routing.
- Dockerhost and containerd deliberately warn and ignore ingress rather than creating an equivalent resource.

## Later Routing and Certificate Findings

Project, profile, CLI, and server ingress settings merge field-by-field. Managed
certificate material becomes a native Kubernetes Secret, and its `secrets` RBAC
is values-gated rather than granted to every installation.

Emulated ingress groups routes sharing a host and selects the longest matching
path. Emulate mode must not also create a native cluster Ingress. Startup RBAC
preflight reports missing permissions, and a real ingress-nginx E2E leg validates
settings that object-shape tests cannot.

Compose `x-cornus-ingress.tls` uses `secret_name` and `cluster_issuer`, not the
direct deploy spec's camelCase spellings. Background-agent conduit configuration is reconciled in place for an existing
project; the server connection remains immutable project identity.

## Controller-Observed Coverage and Shared Transport

Object-shape assertions are necessary but insufficient. The controller-enabled
E2E leg deploys an auto-derived host, dials the ingress-nginx NodePort with the
matching Host header, and requires both HTTP 200 and the workload's echoed request
line. The fetch is gated on controller existence, not on a target name or ambient
flag.

Before ingress-nginx observes a new Ingress it answers 404 from the default
backend; after observation but before endpoints it may answer 503. Positive
controller requests use `http_get(retry_until=200)`. Negative 404 assertions do
not retry 404, because that would merge "not yet" with "never".

Both native/emulated bridge callers use `ingressroute.BridgeTransport`. The
helper's bounds and adoption at each caller are tested separately; a helper test
alone does not prevent a caller from restoring an unbounded inline transport.

Canonical ingress predicates suppress backend warnings for empty or
client-emulated ingress. Client-emulated ingress is already being served through
the conduit, so warning that the backend creates no cluster Ingress would be
actively misleading.
