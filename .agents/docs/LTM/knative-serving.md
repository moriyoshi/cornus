# Knative Serving descriptor

## Summary

Cornus accepts a Knative Serving Service manifest (`serving.knative.dev/v1`, Kind `Service` — a "ksvc") as a first-class deploy descriptor alongside the native `DeploySpec`, docker-compose, and devcontainers. `cornus deploy -f` auto-detects one and translates it into a `DeploySpec` carrying a `Knative *KnativeSpec` block. On a cluster that serves Knative, the kubernetes backend round-trips that block into a native ksvc (autoscaling, scale-to-zero); every other backend runs it as an ordinary container and warns. Serving only — no Eventing; single always-latest revision — no traffic splitting.

## Key Facts

- `api.DeploySpec.Knative *KnativeSpec` (`pkg/api/deploy.go`) holds `Enabled`, `MinScale`/`MaxScale`/`Target`/`Concurrency` (`*int`), `Class` (`kpa`/`hpa`), `Metric` (`concurrency`/`rps`/`cpu`), `TimeoutSeconds`, `Port`, and `Annotations` passthrough. `KnativeSpec.Validate()` mirrors `IngressSpec.Validate` (class/metric sets, non-negative scales, `minScale <= maxScale`, `cpu` metric requires `hpa`).
- `pkg/knative.Detect([]byte)` sniffs `apiVersion` prefix `serving.knative.dev/` + `kind == Service`; `pkg/knative.Load([]byte) (api.DeploySpec, []string, error)` translates one document and returns warnings. Uses `sigs.k8s.io/yaml` and `k8s.io/apimachinery/.../resource` — no `knative.dev/*` dependency.
- Translation: `container.command` -> `Entrypoint`, `container.args` -> `Command` (so the round-trip through `deployment()` is byte-for-byte); env value-form only; single port; k8s resource quantities -> `api.Resources` (cores/bytes); exec probes -> `Healthcheck`; `autoscaling.knative.dev/*` annotations -> KnativeSpec fields (unknown ones pass through to `Annotations`). Dropped/approximated constructs warn (valueFrom env, non-exec probes, extra ports, volumes, traffic); multi-container / missing image / wrong kind error.
- CLI: `DeployCmd.Run` (`cmd/cornus/commands.go`) branches on `knative.Detect` before the native `yaml.Unmarshal`, printing loader warnings via `cli.out().Warn`.

## Backend Details

- `pkg/deploy/kubernetes/knative.go`. The `applyWorkload` funnel (`job.go`) is the single dispatch point: `knativeGuard` rejects unsupported combinations, then if `spec.Knative.Enabled` and `knativeServed()` -> `applyKnativeService`, else degrade to a Deployment with a warning (or hard-error under `CORNUS_KNATIVE_STRICT=true`).
- `knativeServed()` probes `serving.knative.dev/v1` via the discovery API, cached with `sync.Once`; needs the `Backend.dyn dynamic.Interface` field added to the struct and set in `NewWithClients`.
- `knativeService()` reuses the container `deployment()` built (`desired.Spec.Template.Spec.Containers[0]`), converts it with `runtime.DefaultUnstructuredConverter.ToUnstructured`, drops `stdin`/`tty`/`volumeMounts`, trims to one routed port (`chooseKnativePort`), sets `containerConcurrency`/`timeoutSeconds`, stamps autoscaling annotations + the `cornus.app`/`cornus.managed` labels on the revision template (so pod selection keeps working), and applies via the dynamic client (Get -> set resourceVersion -> Update / else Create, like the Ingress reconcile). The Knative path skips `applyDependents` — the ksvc owns its own Route.
- Lifecycle: `Status` gains a `knativeStatus` fall-through (like `jobStatus`) reading `status.url` + pods by `cornus.app`; `Delete` also deletes the ksvc (best-effort, Knative GC cascades Configuration/Revisions/Route); `Restart` stamps the revision template (new Revision); `Stop`/`Start` return an unsupported error (scale-to-zero is automatic). `api.DeployStatus.URL` carries the ksvc `status.url`.
- Guardrails (v1): Knative is rejected with a clear error when combined with one-shot (`IsOneShot`), `Ingress`, `Mounts`, `Networks`, `Volumes`, `Proxy`, `DNS`, `Hub`, `Docker`, `AgentForward`, or relay-mode `Egress`.
- Host backends (dockerhost/containerd/bare) warn-and-ignore `spec.Knative` at Apply, mirroring the ingress warn.

## Files

- `pkg/api/deploy.go` - `KnativeSpec`, `Validate`, `DeployStatus.URL`.
- `pkg/knative/` - `Detect`/`Load` descriptor loader.
- `cmd/cornus/commands.go` - `DeployCmd.Run` auto-detection.
- `pkg/deploy/kubernetes/knative.go`, `job.go`, `kubernetes.go` - backend round-trip, funnel branch, Status/Delete/lifecycle.
- `pkg/deploy/{dockerhost,containerdhost,barehost}/` - warn-and-ignore.
- `pkg/e2e/harness.go` - `deploy(knative=...)` kwarg (`parseKnativeSpec`), `statusDict` `url`.
- `e2e/scenarios/deploy-knative.star`, Makefile `SCENARIOS` + `E2E_KNATIVE`, `e2e/container/entrypoint.sh` `install_knative`.

## Test Coverage

- `pkg/knative/knative_test.go` - loader mapping, `Detect`, warnings, error cases.
- `pkg/api/knative_test.go` - `KnativeSpec.Validate`.
- `pkg/deploy/kubernetes/knative_test.go` - `NewWithClients` + `dynamicfake.NewSimpleDynamicClientWithCustomListKinds` (register the ksvc list kind AND the netdriver NAD/CNP list kinds — `Delete` runs the netdriver GC which LISTs them) + `fakediscovery` served toggle. Asserts ksvc shape (image, argv, single port, autoscaling annotations, `containerConcurrency`, `cornus.app` label), delete, guardrails, degrade-to-Deployment, strict error, restart annotation, stop/start unsupported, status URL.
- `e2e/scenarios/deploy-knative.star` - kube-only, opt-in via `E2E_KNATIVE=1` (self-skips otherwise, like the `E2E_MULTUS` scenarios). Asserts a real ksvc, its shape, readiness, `status.url`, restart, and GC on remove. Runs on BOTH kube wrappers: the direct host-kind harness (`make e2e-kube E2E_KNATIVE=1` — `KubeTarget.Setup` installs Knative when the flag is set) and the containerized dind+kind runner (`make e2e-container E2E_TARGETS=kube E2E_KNATIVE=1`). Both share the one install implementation, `e2e/container/install-knative.sh` (Knative Serving + Kourier + sslip.io default-domain from upstream manifests — needs network, unlike the vendored Multus install); the Go harness invokes it with `KUBECONFIG` pointed at its cluster, the entrypoint's `install_knative` invokes it against the in-container cluster.
- CI: the `e2e.yml` kube leg sets `E2E_KNATIVE=1` alongside `E2E_MULTUS=1`, so `deploy-knative.star` executes on every push to `main` (not on PRs — those get the `e2e-check` resolve gate, which already covers the scenario since it is in `SCENARIOS`). The runner image bakes `install-knative.sh` via a `COPY` in `e2e/container/Dockerfile`. See [ci-github-actions.md](./ci-github-actions.md).

## Pitfalls

- The typed fake clientset does not know CRDs; the ksvc backend needs the dynamic fake + fake discovery (copy `internal/netdriver/netdriver_test.go`). Register the NAD/CNP list kinds too or `Delete`'s netdriver GC panics.
- Knative pod templates are restricted (queue-proxy injection, feature-flagged volumes, one port), so v1 rejects sidecar/volume/network interop rather than silently dropping it.
- The E2E needs a live Knative install; `install_knative` in the container runner fetches upstream release manifests from the internet (unlike the fully-vendored `install_multus`), so it needs network access. The sample image (`ghcr.io/knative/helloworld-go`) must be pullable by the kind nodes.
- Never add a `knative.dev/serving` typed-client dependency — it would introduce the first `knative.dev/*` dep and risk the pinned k8s version (`cmd/cornus/main.go`); use the dynamic client + `unstructured`, as with Multus NADs.
