# Kubernetes E2E Target Caveats

## Summary

The Kubernetes E2E target intentionally differs from host backends where it relies on cluster infrastructure. Scenario assertions must distinguish a client-side emulator from backend side effects, and must target-gate features that Kubernetes cannot implement without optional components.

## Key Facts

- `(*kubernetes.Backend).Stats` returns 501 without metrics-server. `deploy-stats.star` is correctly skipped on the kube target.
- An ingress emulator still submits the workload to the backend. Kubernetes realizes `x-cornus-ingress` as a real Ingress, so its TLS policy applies to a client-only TLS assertion.
- For an emulated `tls: {}` ingress on kube, supply `CORNUS_INGRESS_TLS_ISSUER` or a `secretName`; the issuer need not issue a certificate when the conduit terminates generated TLS itself.

## Operational Guidance

Use `TARGET` gates where a scenario validates a host-only implementation such as cgroup stats. When the scenario needs a backend-realized object, seed the minimal kube configuration that permits it, following existing ingress scenarios. Do not treat dockerhost/containerd warning-and-ignore behavior as evidence that Kubernetes will accept the descriptor.

## Files

- `e2e/scenarios/deploy-stats.star` - kube target gate.
- `e2e/scenarios/socks5-ingress-tls.star` - issuer environment fixture.
- `pkg/deploy/kubernetes/kubernetes.go` - Stats and Ingress validation behavior.

## Test Coverage

Run `cornus-e2e --check` for every scenario change. A full kube leg needs kind and privilege; the target-specific edits mirror already-green ingress target patterns.
