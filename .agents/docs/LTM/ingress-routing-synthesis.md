# Ingress Routing Synthesis

## Summary

Cornus derives one declarative host/path route vocabulary and realizes it in
three ways: native Kubernetes Ingress objects, a server-owned emulated front
door, or a client-held public tunnel. `pkg/ingressroute` keeps derivation
consistent, while `pkg/ingressmux` supplies persisted longest-match routing and
backend-neutral gateway dialing for realizations that do not rely directly on a
cluster ingress controller.

## Included Documents

| Document | Focus |
|----------|-------|
| [kubernetes-ingress.md](./kubernetes-ingress.md) | Deploy and Compose schema, defaults, policy, Kubernetes objects, TLS, and controller validation |
| [ingress-tunnels.md](./ingress-tunnels.md) | Shared routes, persisted front door, backend gateway, tunnel lifetime, and teardown |

## Stable Knowledge

- `api.IngressSpec` is the declarative source. Compose
  `x-cornus-ingress` adds project/service merging, including field-wise defaults
  and explicit enablement rather than exposing every service.
- `pkg/ingressroute` owns host/path derivation for native, emulated, and tunneled
  modes. Project-scoped subdomains avoid collisions; `@` represents the base
  domain apex.
- Server defaults come from `CORNUS_INGRESS_DOMAIN`, `_CLASS`, and
  `_TLS_ISSUER`. `CORNUS_INGRESS_ENFORCE_DOMAIN` is the optional tenant boundary
  that rejects resolved hosts outside the configured base.
- Native mode reconciles a Kubernetes `networking.k8s.io/v1` Ingress over the
  deployment's shared Service. Owner references drive cleanup. Managed
  certificate data uses a Kubernetes Secret, and its RBAC is values-gated.
- Emulated and tunneled modes use `pkg/ingressmux.Table`, persisted and recovered
  by the server. Routes sharing a host choose the longest matching path.
  `deploy.PortForwardDialer` and `deploy.IngressGateway` hide backend-specific
  workload dialing.
- Compose tunnel enablement is a client-lifetime opt-in. The caller owns the
  provider session; teardown withdraws aliases, bounds and drains bridges, and
  removes the corresponding front-door route.
- HTTP and raw TCP fronts are distinct. A successful response from one front is
  not evidence that the requested protocol is wired correctly.
- Object creation is not end-to-end proof. Ingress class, annotations, TLS
  material, and path behavior require a real controller-backed test.

## Operational Guidance

- Change route derivation in `pkg/ingressroute`, not independently in each
  realization.
- Select exactly one realization for a route. Emulate mode must not also create a
  native Kubernetes Ingress.
- Keep configuration precedence explicit across project, profile, CLI, and
  server defaults. Background-agent conduit settings are first-writer state and
  require deliberate reconciliation when changed.
- When adding a provider, test startup failure, idle sessions, alias withdrawal,
  route persistence, bridge shutdown, and response-body disclosure as well as
  the happy path.

## Files

- `pkg/api/deploy.go` and `pkg/compose/` - ingress schema and Compose translation.
- `pkg/ingressroute/` - shared route derivation.
- `pkg/ingressmux/` and `pkg/server/ingress.go` - persisted route table, proxy,
  and front door.
- `pkg/deploy/kubernetes/` - native Ingress, Service, Secret, and ownership
  realization.
- `cmd/cornus/ingress_tunnel.go` - client-held tunnel lifetime.
- `deploy/helm/cornus/` - defaults, RBAC, and certificate wiring.

## Tests

- API, Compose, route, mux, persistence, gateway, teardown, and Kubernetes object
  unit tests cover deterministic behavior.
- Docker and Kubernetes E2E scenarios cover emulated and tunneled fronts.
- A real ingress-nginx leg validates class, annotations, TLS, routing, and
  controller acceptance beyond fake-client object shape.

## Pitfalls

- A fake Kubernetes client does not execute admission, controller behavior, or
  owner-reference garbage collection.
- Wildcard certificates for `*.<domain>` do not cover
  `<service>.<project>.<domain>`.
- Scenario skips must include the target because controller flags can also be
  present during combined Docker and Kubernetes runs.
- VitePress links into Japanese headings may legitimately require decomposed
  kana inside URL fragments; normalize all other prose.
