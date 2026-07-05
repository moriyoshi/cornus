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

- The E2E scenario still needs a live kind cluster with an ingress controller.
- Dockerhost and containerd deliberately warn and ignore ingress rather than creating an equivalent resource.
