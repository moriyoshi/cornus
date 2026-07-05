# Ingress Tunnels

## Summary

Ingress tunnels expose declarative ingress routes through Cornus's tunnel
providers without requiring a cluster ingress controller. A server-side front
door derives one route vocabulary, applies longest-match host/path routing, and
bridges through backend-neutral `deploy.IngressGateway` dialing.

## Key Facts

- `pkg/ingressroute` is the shared derivation used by native Kubernetes ingress,
  emulation, and tunnels.
- `pkg/ingressmux.Table` is persisted and recovered; `Proxy` routes by host and
  longest matching path.
- `deploy.PortForwardDialer` and `deploy.IngressGateway` keep routing independent
  of a concrete backend.
- Compose `x-cornus-ingress.tunnel` is an explicit client-lifetime opt-in.
- Host aliases are withdrawn during teardown; bridge lifetimes are bounded and
  drained.
- TCP and HTTP fronts are distinct. Tests must prove the intended front rather
  than merely finding that some route responds.

## Details

The feature landed in stages: common vocabulary and front door, backend gateway
seams, the client-held tunnel, domain/setup integration, declarative Compose
configuration, docs, and E2E. Self-review found startup hangs, idle bridge leaks,
recovery races, disclosure in 502 bodies, missing persistence reachability, and
incorrect `--proto tcp` behavior; the final implementation bounds startup,
reclaims bridges, withdraws aliases, and documents the remaining disclosure
surface.

A real ingress-nginx leg was added as a fourth E2E mode rather than replacing the
controller-less path. This matters because object-shape assertions can pass even
when annotations, TLS material, ingress class, or routing behavior are ignored by
a real controller.

## Files

- `pkg/ingressroute` - route derivation.
- `pkg/ingressmux` - table and proxy.
- `pkg/server/ingress.go` - front door and persistence.
- `cmd/cornus/ingress_tunnel.go` - client lifetime.
- `e2e/scenarios/ingress-tunnel-*.star` - provider and routing coverage.

## Test Coverage

Unit tests cover derivation, longest-match routing, persistence, bridge teardown,
and gateway behavior. Docker and Kubernetes scenarios cover emulated/tunnel
fronts; a controller-backed kube leg validates realized ingress settings.

## Pitfalls

- A generated Ingress object does not prove a controller accepts its settings.
- Scenario skip conditions must include the target; the controller flag is also
  present during combined docker+kube runs.
- Japanese anchors emitted by VitePress may require decomposed kana in URL
  fragments.
