# Kubernetes E2E Target Caveats

## Summary

The Kubernetes E2E target intentionally differs from host backends where it relies on cluster infrastructure. Scenario assertions must distinguish a client-side emulator from backend side effects, and must target-gate features that Kubernetes cannot implement without optional components.

## Key Facts

- `(*kubernetes.Backend).Stats` returns 501 without metrics-server. `deploy-stats.star` is correctly skipped on the kube target.
- An ingress emulator still submits the workload to the backend. Kubernetes realizes `x-cornus-ingress` as a real Ingress, so its TLS policy applies to a client-only TLS assertion.
- For an emulated `tls: {}` ingress on kube, supply `CORNUS_INGRESS_TLS_ISSUER` or a `secretName`; the issuer need not issue a certificate when the conduit terminates generated TLS itself.
- `KubeTarget.Setup` creates the kind cluster but does NOT build or load the `cornus:e2e` sidecar image. Only the containerized runner's `prepare_kube` does. A direct `make e2e-kube` against a pre-existing cluster therefore runs whatever sidecar binary was loaded into it last, so a caretaker-side change can appear to fail in the server. Rebuild and `kind load docker-image cornus:e2e` before trusting a kept-cluster run.
- The kubernetes archive trio (`StatPath`/`CopyFrom`/`CopyTo`) is unsupported outright. Anything that moves bytes into or out of a pod on this target goes through `deploy.FSOperator` (the caretaker's `S` stream), and only for paths mounted into the caretaker — its own mount namespace cannot see image layers. Exec, by contrast, works, so a path can be listable and untransferable at the same time.

## Operational Guidance

Use `TARGET` gates where a scenario validates a host-only implementation such as cgroup stats. When the scenario needs a backend-realized object, seed the minimal kube configuration that permits it, following existing ingress scenarios. Do not treat dockerhost/containerd warning-and-ignore behavior as evidence that Kubernetes will accept the descriptor.

## Files

- `e2e/scenarios/deploy-stats.star` - kube target gate.
- `e2e/scenarios/socks5-ingress-tls.star` - issuer environment fixture.
- `pkg/deploy/kubernetes/kubernetes.go` - Stats and Ingress validation behavior; `addFSOpRole` and `addServerInitiatedRoles`.
- `pkg/deploy/kubernetes/fsop.go` - the FSOperator realization (caretaker registry lookup).
- `e2e/scenarios/web-fs.star` - the kube arm, including the volume/operator section.

## Test Coverage

Run `cornus-e2e --check` for every scenario change. A full kube leg needs kind and privilege; the target-specific edits mirror already-green ingress target patterns.
