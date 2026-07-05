# Deploying workloads

Recipes for applying a [deploy spec](/reference/deploy-spec) with
[cornus deploy](/cli/deploy), reaching workloads, and driving them across the
three [deploy backends](/reference/deploy-backends). The local backend is chosen
by the `CORNUS_DEPLOY_BACKEND` environment variable; there is no CLI flag.

## Deploy a Compose project locally on a Docker host (default dockerhost backend)

Apply a spec to the local Docker daemon, the default `dockerhost` backend.

```sh
cornus deploy -f app.yaml
```

```yaml
name: web
image: localhost:5000/app:v1
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
```

- `dockerhost` needs the Docker socket (`/var/run/docker.sock`). It is the richest backend and maps the widest set of spec fields.

**See also:** [cornus deploy](/cli/deploy), [deploy spec](/reference/deploy-spec), [deploy backends](/reference/deploy-backends)

## Deploy to a bare containerd host (CORNUS_DEPLOY_BACKEND=containerd)

Run workloads natively on a containerd host with no dockerd in the loop.

```sh
sudo CORNUS_DEPLOY_BACKEND=containerd cornus deploy -f app.yaml
```

- Linux-only; needs root (it creates netns and runs CNI), the containerd socket (`CORNUS_CONTAINERD_ADDRESS`, default `/run/containerd/containerd.sock`), and the standard CNI plugins under `/opt/cni/bin`.
- Known gaps vs dockerhost: attach is output-only and healthchecks are ignored.

**See also:** [deploy backends](/reference/deploy-backends), [cornus deploy](/cli/deploy)

## Deploy to a Kubernetes cluster (through a server / connection profile)

The `kubernetes` backend is server / in-cluster only, so deploy against a cornus server running in the cluster.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
```

- A local `cornus deploy` with `CORNUS_DEPLOY_BACKEND=kubernetes` falls back to `dockerhost` with a warning; the cluster backend runs on the server (`cornus serve`).
- Store the server once as a connection profile so later commands need no `--server`.

**See also:** [remote clusters](/guides/remote-clusters), [deploy backends](/reference/deploy-backends), [remote workflows](/topics/remote-workflows)

## Apply a raw deploy spec file (cornus deploy -f spec.yaml)

Deploy the native schema directly, the same shape Compose and devcontainers translate into.

```sh
cornus deploy -f spec.yaml
```

- The spec is applied imperatively: one spec goes in, and the backend converges the workload to it. See the full field reference for ports, mounts, volumes, resources, and healthchecks.

**See also:** [deploy spec](/reference/deploy-spec), [cornus deploy](/cli/deploy)

## Delete a deployment (cornus deploy --delete / cornus compose down)

Tear down a deployment by name, locally or against a server.

```sh
cornus deploy -f app.yaml --delete
cornus deploy -f app.yaml --server https://cornus.example.com --delete
```

- For a Compose project, use `cornus compose down` instead (add `--volumes` to also remove project-scoped named volumes).

**See also:** [cornus deploy](/cli/deploy), [cornus compose](/cli/compose)

## Run a deploy in the background (-d/--detach)

POST the spec to a server once and exit, leaving the workload running with no client session.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --detach
# later, tear it down:
cornus deploy -f app.yaml --server https://cornus.example.com --delete
```

- Detached deploys reject client-local bind mounts and client-sourced credentials, and published ports bind on the server host rather than being auto-forwarded.
- `--detach` is a no-op for local deploys.

**See also:** [cornus deploy](/cli/deploy), [remote workflows](/topics/remote-workflows)

## Scale replicas and configure rolling updates (deploy spec replicas + updateConfig)

Set the desired instance count and how a Kubernetes rolling update proceeds.

```yaml
name: web
image: localhost:5000/app:v1
replicas: 3
updateConfig:
  parallelism: 1
  order: start-first
```

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
```

- `replicas` is honored by every backend; on host backends published host ports go to replica 0 only.
- `updateConfig` maps onto the Kubernetes Deployment `strategy.rollingUpdate` only; host backends recreate a single instance and ignore it.

**See also:** [deploy spec](/reference/deploy-spec), [deploy backends](/reference/deploy-backends)

## Run a command inside a running workload (cornus exec)

Exec into a deployment's first instance through a server, like `docker exec`.

```sh
cornus exec --server https://cornus.example.com -it web -- sh
```

- Everything after the deployment name is passed to the command verbatim. `-i` forwards stdin; `-t` requests a PTY (downgraded to a plain stream when stdin is not a terminal).
- The remote command's exit code is propagated as cornus's own.

**See also:** [cornus exec](/cli/exec), [cornus config](/cli/config)

## Mount a client-local directory into a remote workload (--local-mount, streamed over 9P)

Bind-mount a directory that lives on your machine into a workload running on a remote server.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --local-mount ./config:/etc/app:ro \
  --local-mount ./data:/data
```

- `--local-mount SRC:DST[:ro]` is repeatable and serves the path over 9P for the session lifetime. The workload reads your files in place; no upfront copy.
- Add `,cache` to declare an immutable read-only source. It uses the server per-file cache and implies `:ro`.
- Add `,async` for a writable, cache-coherent mount backed by the block protocol. It is intended for write-intensive single-writer workloads such as a development database, requires `replicas: 1`, and cannot be combined with `ro` or `cache`.
- Set `CORNUS_BLOCK_COHERENCE=subhash,subfill` in both the server and deploy-caller environments, plus `CORNUS_BLOCK_READAHEAD=64k` or a larger cap, as a starting point for database-shaped async mounts. See [server environment variables](/reference/server-env-vars#remote-9p-file-cache-and-writable-mounts).
- Requires a foreground session; `--detach` rejects client-local mounts.

**See also:** [cornus deploy](/cli/deploy), [networking](/guides/networking), [remote workflows](/topics/remote-workflows)

## Reach published and unpublished ports (auto client-side forward + cornus port-forward)

During a `--server` session, published ports (spec `ports:`) auto-forward to `127.0.0.1:<host>`; reach any other container port on demand with `cornus port-forward`.

```sh
# Published ports auto-forward for the session's lifetime:
cornus deploy -f app.yaml --server https://cornus.example.com
# (prints forwarding 127.0.0.1:8080 -> :80)

# Reach an unpublished container port separately:
cornus port-forward web 5432:5432
```

- Disable the auto-forward with `--no-forward-ports` on the deploy. `cornus port-forward` binds one local listener per `LOCAL:REMOTE` (or bare `PORT`) mapping and runs in the foreground until Ctrl-C.
- For a cluster profile both paths go straight to the workload pod with your kubeconfig, falling back to a tunnel through the server; `/udp` mappings work on the dockerhost, containerd, and bare backends but are skipped on Kubernetes.

**See also:** [cornus port-forward](/cli/port-forward), [networking](/guides/networking), [remote workflows](/topics/remote-workflows)
