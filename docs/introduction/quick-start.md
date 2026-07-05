# Quick start

Cornus is primarily meant to run inside a local Kubernetes cluster. This walkthrough goes from nothing installed to a workload running in a single-node [k3s](https://k3s.io/) cluster, using the prebuilt `cornus` binary and the multi-arch image published to `ghcr.io/moriyoshi/cornus`.

No clone, no Go toolchain, and no Docker: k3s runs containerd natively, Cornus's own in-cluster build engine builds the demo image, and `cornus compose` talks to the server directly — so there is no Docker daemon anywhere in the loop. The whole thing is an ordinary `compose.yaml` and one command.

## 1. Install the Cornus CLI

Download the prebuilt static binary for your platform and put it on `PATH`:

```sh
curl -fsSL https://github.com/moriyoshi/cornus/releases/latest/download/cornus-linux-amd64 -o cornus
chmod +x cornus && sudo mv cornus /usr/local/bin/cornus
cornus version
```

(For arm64, swap `amd64` for `arm64`.) See [installation](/introduction/installation) for the container image and building from source.

## 2. Install k3s and Cornus, then point the CLI at it

Cornus is exposed on a fixed NodePort (`30500`) so both your CLI and the node's containerd reach it at a real service endpoint — nothing here depends on a `kubectl port-forward`. First tell k3s's containerd that `localhost:30500` is a plain-HTTP registry (the demo image you build in step 3 is served from there), then install k3s:

```sh
sudo mkdir -p /etc/rancher/k3s
sudo tee /etc/rancher/k3s/registries.yaml >/dev/null <<'EOF'
mirrors:
  "localhost:30500":
    endpoint:
      - "http://localhost:30500"
EOF
curl -sfL https://get.k3s.io | sh -s - --write-kubeconfig-mode 644
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
```

Now install Cornus. The shipped manifest — a privileged StatefulSet (the build engine needs it) with a PVC, RBAC for deploying into the cluster, and a NodePort `Service` (`30500` -> container `5000`) — already points at the published GHCR image, so there is nothing to build: the node's containerd pulls `ghcr.io/moriyoshi/cornus` straight from GHCR.

```sh
kubectl apply -f https://raw.githubusercontent.com/moriyoshi/cornus/main/deploy/k8s/cornus.yaml
# or, the recommended Helm path — install the chart straight from the OCI registry:
#   helm install cornus oci://ghcr.io/moriyoshi/charts/cornus --version 0.1.0
kubectl rollout status statefulset/cornus --timeout=300s
```

Check that the server is up and ready to serve at the NodePort:

```sh
curl http://localhost:30500/healthz        # -> {"status":"ok"}
```

Store it as your default connection profile so no later commands need `--server` or `CORNUS_HOST`:

```sh
cornus config set-context demo --server http://localhost:30500
cornus config use-context demo
```

Prefer a guided path? [`cornus setup`](/cli/setup) is an interactive wizard that
picks your deployment scenario, creates and verifies the profile, and prints the
remaining setup steps.

See [connection config](/reference/connection-config) and [working with remote clusters](/guides/remote-clusters) for connecting to a remote or ingress-less cluster.

## 3. Build and deploy with a Compose file

The Cornus CLI speaks Compose. Write an ordinary `compose.yaml` — the same file `docker compose` would read — whose `build:` section exercises a cache mount and a secret mount, with one published port:

```sh
mkdir -p demo
tee demo/Dockerfile >/dev/null <<'EOF'
FROM alpine:3.20
RUN --mount=type=cache,target=/var/cache/apk apk add --no-cache curl busybox-extras
RUN --mount=type=secret,id=token \
    test -f /run/secrets/token && echo "secret present (not stored in image)"
RUN mkdir -p /www && echo 'cornus demo' > /www/index.html
CMD ["sh", "-c", "echo cornus demo && exec httpd -f -v -p 80 -h /www"]
EOF
echo -n s3cret > /tmp/token

tee demo/compose.yaml >/dev/null <<'EOF'
name: demo
services:
  web:
    build:
      context: .
      secrets:
        - token
    ports:
      - "8080:80"
secrets:
  token:
    file: /tmp/token
EOF
```

Now bring it up. One command builds the image in the cluster (the context and the secret stream over 9P-on-WebSocket to the Cornus pod, so your host never needs build privileges or Docker), pushes it into Cornus's in-cluster registry, deploys it, and forwards the published port back to your machine:

```sh
cd demo
cornus compose up
```

The service is built and deployed as `localhost:30500/demo-web:latest` (the ref is `<project>-<service>`, so a `build:` service sets no `image:` of its own). The command holds the session in the foreground — streaming any client-local mounts and tunneling the workload's published port — and prints `forwarding 127.0.0.1:8080 -> :80`. The demo container serves a page on `:80`, so `curl http://127.0.0.1:8080` returns `cornus demo` even though the workload runs in the cluster. Leave it running.

## 4. Inspect and clean up

From another terminal (the workload is named `<project>-<service>`):

```sh
kubectl get deployment,service demo-web
kubectl logs deployment/demo-web           # -> cornus demo
cornus compose logs demo-web               # same logs, no kubectl needed
```

`cornus compose logs` streams each service's logs — add `--follow` to follow, `--tail`, `--since`, or `-t` for timestamps, and name services to filter (default: all).

Then tear it down — Ctrl-C the foreground `cornus compose up` to release the published-port tunnel, remove the services, and remove the cluster:

```sh
cornus compose down
/usr/local/bin/k3s-uninstall.sh
rm -rf demo /tmp/token
```

::: tip Variants
The same flow works on k0s (single-binary containerd), kind (map the node port or load the image between build and deploy), a plain Docker host (`dockerhost` backend with the Docker socket mounted), and a bare containerd host (`CORNUS_DEPLOY_BACKEND=containerd`). See [deploy backends](/reference/deploy-backends).
:::

## Driving the engine directly

`cornus compose up` is sugar over two primitives — the build engine and the deploy engine — that you can drive directly when you want explicit control, have no Compose file, or need to interleave a step:

```sh
# Build in the cluster and push to the registry. --builder streams the context and
# the secret over 9P-on-WebSocket to the Cornus pod, so the host needs no Docker
# and no build privileges:
cornus build --builder ws://localhost:30500/.cornus/v1/build/attach \
  -t localhost:30500/demo:v1 \
  --secret id=token,src=/tmp/token demo

curl http://localhost:30500/v2/demo/tags/list    # -> {"name":"demo","tags":["v1"]}

# Deploy from a native spec — the schema every higher-level surface translates
# into. It uses the current connection profile (an explicit --server overrides):
tee demo.yaml >/dev/null <<'EOF'
name: demo
image: localhost:30500/demo:v1
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
EOF
cornus deploy -f demo.yaml
```

See [`cornus build`](/cli/build), [`cornus push`](/cli/push), [`cornus deploy`](/cli/deploy), and the [deploy spec reference](/reference/deploy-spec) for the full field set.

## Next steps

* [Output modes](/guides/output-modes) — pick `plain` for CI or `json` for agents.
* [Working with remote clusters](/guides/remote-clusters) — point the CLI at a remote cluster.
* [Tunnels](/guides/tunnels) — expose a workload publicly.
* [The workload hub](/guides/hub) — reach other workloads by name.
