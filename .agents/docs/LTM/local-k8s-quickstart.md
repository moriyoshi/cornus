# Local-Kubernetes Quick Start (k3s Flow)

## Summary

The README Quick start is built around single-node k3s and is fully Docker-free, because the
project is primarily intended to run in a local Kubernetes cluster and the earlier kind-based
draft still required Docker (`docker build`, `kind load`) — defeating the point. Cornus
bootstraps its own container image via a temporary host-side server, then runs in-cluster
exposed as a **NodePort (30500)** so there is no ad-hoc `kubectl port-forward` in the flow at
all. The marquee walkthrough now leads with the Docker-familiar `cornus compose up` (one command:
build in-cluster + push + deploy + publish-port forward), with the native `build`/`deploy`
primitives kept as a "Driving the engine directly" subsection. See
[[remote-cluster-connection-ergonomics]] for the connection-profile / auto-forward machinery this
flow deliberately does *not* use, and [[shipping-and-install-synthesis]] for the manifest/chart
packaging that ships the NodePort and RBAC.

## Key Facts

- **`cornus compose up` is the marquee path (2026-07-07).** Steps 2-5 are: set a connection
  profile (`config set-context demo --server http://localhost:30500`) -> write a standard
  `compose.yaml` with a `build:` section -> `cornus compose up`. That one command builds
  in-cluster, pushes, deploys, and publish-port-forwards, with no Docker daemon. The native
  `build --builder` + `deploy -f <spec>` demo moved to a `### Driving the engine directly`
  subsection framed as "the primitives compose translates into." This replaced a draft that led
  with the native verbs behind a hand-run port-forward and made newcomers learn a proprietary
  deploy-spec YAML.
- **The quick start uses NO port-forward (2026-07-07).** The in-cluster Service is **NodePort
  `30500`** (raw manifest `deploy/k8s/cornus.yaml` and chart default agree). The node pulls
  images through kube-proxy's node-port binding (reachable on the node's own loopback via
  `route_localnet`) — a real service endpoint, not a developer's `kubectl port-forward`. The same
  node port serves the CLI control plane, so `--server http://localhost:30500` reaches the server
  directly.
- **The pull-ref registry host comes from `/.cornus/v1/info`, not the client endpoint (2026-07-07).**
  Auth-exempt `GET /.cornus/v1/info` returns `api.ServerInfo{RegistryHost, RegistryScheme}`; the
  kubernetes backend auto-advertises `localhost:<nodePort>` (NodePort) or the LB host by
  introspecting its own Service. So the deploy `spec.Image` becomes
  `localhost:30500/<project>-<service>:latest`, which the node trusts, while the in-pod build push
  is server-side redirected to the co-located registry over loopback. See
  [[remote-cluster-connection-ergonomics]] for the full advertise/redirect design.
- **Profile uses a static `--server`, never the auto-forward feature.** Connection-profile
  auto-forward (`--pf-service`/`--namespace`) binds an *ephemeral* local port, so the image would
  be tagged `127.0.0.1:<random>/...` — unpullable by the node and gone when the command exits. A
  stable node-reachable registry host is load-bearing, so the demo profile is a static
  `http://localhost:30500`. Auto-forward lives in the remote-server section only.
- **The demo workload actually listens on `:80` (2026-07-08).** `cornus compose up` forwards
  `127.0.0.1:8080 -> :80`; the demo `Dockerfile` runs busybox httpd on `:80` so
  `curl http://127.0.0.1:8080` returns `cornus demo`. A forwarded port proves only the tunnel,
  not the workload.
- **Bootstrap self-build** (still needed to get the Cornus image into the cluster): a temporary
  root server (`sudo CORNUS_DATA=/tmp/cornus-bootstrap ./cornus serve --addr :5000`) builds the
  Cornus image from the repo Dockerfile via
  `cornus build --builder ws://localhost:5000/api/build/attach -t localhost:5000/cornus:dev .`;
  k3s pulls it from this bootstrap registry, which is then Ctrl-C'd. Pre-built GHCR images exist
  as a shortcut, but the walkthrough deliberately dogfoods the self-build.
- **k3s containerd does not trust localhost registries** the way Docker does:
  `/etc/rancher/k3s/registries.yaml` must mark the endpoints (bootstrap `localhost:5000` and the
  running server's `localhost:30500`) as plain-HTTP.
- **No image handoff is needed**: the single k3s node IS the host, so both the Cornus image and
  the built demo image are pulled by the node's containerd straight through the advertised
  registry. This is the key simplification over kind, whose nodes are Docker containers that
  cannot reach a host-local registry (`kind load` required).
- **In-cluster deploy backend must be set explicitly**: an in-cluster server defaults to the
  dockerhost backend and every deploy fails (no Docker socket in the pod) despite the shipped RBAC
  being for the kubernetes backend. Fixed: the raw manifest sets
  `CORNUS_DEPLOY_BACKEND=kubernetes` and the chart exposes `deployBackend: kubernetes`.
- **The local `cornus deploy` CLI hardcodes the dockerhost backend**
  (`cmd/cornus/commands.go` `DeployCmd.Run`) and does NOT honor `CORNUS_DEPLOY_BACKEND`. The
  correct cluster path is `cornus deploy --server http://localhost:30500 -f ...` (a deploy-attach
  session THROUGH the in-cluster server). A `--server` deploy is a foreground session: Ctrl-C
  tears the workload down.
- Install k3s with `--write-kubeconfig-mode 644`; the manifest's image line is overridden via a
  sed pipe into `kubectl apply` (or helm `--set image.repository/tag`). `k3s kubectl` works as a
  no-install kubectl fallback throughout.

## Details

### The `cornus compose up` flow (README Quick start)

1. Static CLI build (`CGO_ENABLED=0`, tags `netgo osusergo`).
2. Bootstrap serve as root with `CORNUS_DATA=/tmp/cornus-bootstrap` + self-build of
   `localhost:5000/cornus:dev` (the full self-build — golang base pull + module compile — took
   ~90s on the 2026-07-05 verification).
3. `registries.yaml` (plain-HTTP `localhost:30500`, plus the bootstrap `localhost:5000` during
   install) + k3s install + sed image override piped to `kubectl apply`; StatefulSet Ready with
   k3s containerd pulling the Cornus image. Then the bootstrap server is Ctrl-C'd. No port-forward
   is started — the in-cluster server is reached at `http://localhost:30500`.
4. `config set-context demo --server http://localhost:30500` sets the connection profile; write a
   standard `compose.yaml` with a `build:` section (and no `image:` — see the gotcha below).
5. `cornus compose up` builds in-cluster, pushes to the co-located registry, deploys
   `<project>-<service>` (Deployment `demo-web`) + its Service, and publish-port-forwards
   `127.0.0.1:8080 -> :80`. `curl http://127.0.0.1:8080` returns `cornus demo`.

Step 3's prose explains that the image ref host comes from `/.cornus/v1/info` (not the endpoint) and
that the in-pod build push is redirected to the co-located registry. The README also carries a
"Registry host for cluster pulls (multi-node)" section documenting the exposure matrix
(nodePort/clusterIP/hostPort/hostNetwork/ingress) with per-mode NetworkPolicy and node-trust
caveats.

### `cornus compose` gotchas

- A service with a `build:` section **ignores** any `image:` you also set (`BuildPlan.Image` is
  assigned at `pkg/compose/project.go:615` but never read) — the derived
  `<host>/<project>-<service>:latest` is always used for both push target and workload image. This
  diverges from Docker Compose (where `image:` names the built image). The quick-start build
  service therefore omits `image:`. `<host>` = resolved endpoint host:port advertised via
  `/.cornus/v1/info`; `<project>` = `-p` / `COMPOSE_PROJECT_NAME` / top-level `name:` / compose-file
  directory.

### The demo Dockerfile serving `:80` (2026-07-08)

The demo `Dockerfile` in the README:

- `apk add` installs `busybox-extras` alongside `curl` — Alpine split the `httpd` applet out of
  the default busybox into `busybox-extras`, so a bare `httpd` is `not found` on `alpine:3.20`.
- `RUN mkdir -p /www && echo 'cornus demo' > /www/index.html`.
- `CMD ["sh", "-c", "echo cornus demo && exec httpd -f -v -p 80 -h /www"]`: the startup `echo`
  preserves the `cornus demo` banner (keeps the `kubectl logs deployment/demo-web # -> cornus
  demo` expectation valid), then `exec`s busybox httpd in the foreground on `:80` serving `/www`.

Verified end to end: built with the BuildKit secret mount
(`docker build --secret id=token,src=token`), ran `-p 18080:80`, and
`curl http://127.0.0.1:18080` returned `cornus demo` with a `response:200` log line.

### Verified reference outputs (2026-07-05 native-verb run)

`/healthz` returns `{"status":"ok"}` (not `ok`); the in-cluster build yields
`{"name":"demo","tags":["v1"]}` from `/v2/demo/tags/list`. The demo workload's `$VERSION` in the
old testdata Dockerfile was a build ARG (absent at runtime).

### Variants

- **k0s**: same flow, native containerd (`localhost:30500`).
- **kind**: requires Docker and explicitly routes to `### Driving the engine directly`, because
  `compose up` builds and deploys in one shot with no gap to `kind load` the image into the node
  between the two steps. Load BOTH images with `kind load docker-image` since nodes cannot reach a
  host-local registry. The non-`latest` demo tag defaults to `imagePullPolicy: IfNotPresent`,
  which is what makes the side-loaded image usable — a concrete reason the primitive path stays
  documented.
- **Plain Docker single-host**: the shipped compose.yaml (privileged + docker.sock) keeps
  `-p 5000:5000` (direct publish, no forward) — build against
  `ws://localhost:5000/api/build/attach` so privilege needs stay inside the container, then
  `cornus deploy -f testdata/demo.yaml` + `docker logs` filtered on the `cornus.app=demo` label.

### Verifying k3s inside a privileged container (harness knowledge, not quickstart content)

The end-to-end verification ran inside a privileged debian:bookworm container playing "the
host" (the established `sg docker` pattern), because host sudo needs a password and
kubectl/helm were not installed. Container-only workarounds — real hosts with systemd and a
non-overlay /var/lib need none of these:

- The k3s installer hard-requires `systemctl`: provide a stub plus
  `INSTALL_K3S_SKIP_ENABLE`/`INSTALL_K3S_SKIP_START`, then run `k3s server` directly.
- `/var/lib/rancher` and `/var/lib/kubelet` must not sit on overlayfs — mount tmpfs over them
  (k3s's containerd refuses nested overlay).
- The container root cgroup must be evacuated kind/k3d-style before k3s can enable subtree
  controllers.
- Not verified literally: the installer's systemd service path and the `sudo` prefixes
  (container root stood in for both).

Side observation worth keeping: the Cornus build engine works fine on an overlayfs-backed data
dir (BuildKit falls back to the standard differ), while k3s's containerd refuses nested
overlay outright.

## Files

- `README.md` — the Quick start walkthrough (compose-up lead + `### Driving the engine directly`
  + "Registry host for cluster pulls (multi-node)" + variants footnote + demo Dockerfile).
- `deploy/k8s/cornus.yaml` — raw manifest; Service is **NodePort `30500`**, sets
  `CORNUS_DEPLOY_BACKEND=kubernetes`, pins the GHCR image.
- `deploy/helm/` — chart; `deployBackend: kubernetes` and `registry.exposure`
  (nodePort|clusterIP|hostPort|hostNetwork|ingress, default nodePort) + `registry.nodeCIDR`
  NetworkPolicy ipBlock rendered into the StatefulSet/Service.
- `testdata/demo.yaml`, `testdata/Dockerfile` — the demo workload used by the native-verb path.
- `cmd/cornus/commands.go` — `DeployCmd.Run` (hardcoded dockerhost backend for local deploys);
  ref-host resolution at `commands.go:59`.
- `pkg/compose/project.go` — `BuildPlan.Image` assigned at line 615, never read (the `image:`
  gotcha).

## Test Coverage

Manual end-to-end verification of the k3s flow (2026-07-05 native-verb path, privileged-container
run above); the demo-`:80` Dockerfile was verified with a throwaway `docker build`/`docker run`
(2026-07-08). The raw manifest is YAML-parse-checked and CI's helm job covers chart
lint/template. There is no automated quickstart E2E. The `cornus compose up` quick-start flow was
NOT run on a live k3s cluster — the one unconfirmed detail is the published-port Service name in
`kubectl get deployment,service demo-web` (the Deployment name `<project>-<service>` = `demo-web`
is solid). Advertise/redirect logic is unit-tested (`pkg/deploy/kubernetes/advertise_test.go`,
`pkg/server/server_info_test.go`, `cmd/cornus/internal/composecli/registry_test.go`) and E2E
scenario `registry-advertise.star`.

## Pitfalls

- **Do not add a `kubectl port-forward` to the quick start** — it is intentionally removed. The
  NodePort `30500` is the stable, node-reachable registry host and control plane. A port-forward
  binds an ephemeral `127.0.0.1:<random>` the node cannot pull from.
- Do not suggest `CORNUS_DEPLOY_BACKEND=kubernetes cornus deploy` for the local CLI — it is
  ignored (an early quickstart draft got this wrong). Use `--server`.
- A `build:` service in `compose.yaml` must omit `image:` — it is silently ignored, unlike Docker
  Compose.
- **`busybox httpd` is not in alpine's default busybox** — it lives in `busybox-extras`. A `CMD`
  using bare `httpd` on `alpine:*` fails with `httpd: not found`. Verify applet availability with
  a throwaway `docker run` before baking it into a documented Dockerfile.
- **A forwarded/published port only proves the tunnel, not the workload.** Docs that promise
  observable output (`curl` returns `cornus demo`) must ship a workload that actually produces it.
- Do not auto-advertise ClusterIP: it is the default Service type, carries no intent, and would
  silently rewrite the quick-start ref to `<clusterIP>:5000`, which the node does not trust.
- kind nodes cannot pull from a host-local registry; only the k3s/k0s single-node flows get the
  no-handoff property, and kind must use the native two-step (not `compose up`).
- The bootstrap server (`localhost:5000`) must be Ctrl-C'd once k3s has pulled the Cornus image;
  it is only needed to seed that first image.
