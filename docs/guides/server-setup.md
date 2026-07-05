# Setting up a server

Every `cornus` command talks to a server. This page has one short runbook per
arrangement the server can be in — what it needs, the command that starts it,
and how to confirm it worked. [`cornus setup`](/cli/setup) links straight into
the section for the arrangement you picked.

These are runbooks, not references. Each one ends with a link to the page that
covers its subject exhaustively; follow it when you need the full flag,
value, or capability list.

## Which arrangement? {#which}

The server is one process. What changes between arrangements is **where it runs**
and **which runtime it drives** ([deploy backends](/reference/deploy-backends)).

| You want to | Arrangement | `cornus setup` scenario |
| --- | --- | --- |
| Try Cornus with the least setup | [Local, Docker](#local-docker) | `local` |
| Run without a Docker daemon | [Local, containerd](#local-containerd) or [bare](#local-bare) | `local` |
| Run without any daemon at all | [Local, bare](#local-bare) | `local` |
| Use Incus instances | [Local, Incus](#local-incus) | `local` |
| Deploy into a cluster from your machine | [Local, Kubernetes](#local-kubernetes) | `local` |
| Use a beefier build/deploy host | [Remote host over SSH](#ssh) | `ssh-*` |
| Run Cornus in the cluster it deploys to | [In the cluster](#in-cluster) | `kube-port-forward`, `kube-url` |
| Keep the host clean | [As a container](#in-a-container) | `docker-container` |
| Use a server someone else runs | [Nothing to set up](#existing) | `url` |

Two rules apply everywhere:

- **The build engine needs privilege.** It uses runc, overlayfs, and user
  namespaces, so run the server as root/privileged, or use `cornus serve --rootless`.
  See [privilege posture](/reference/deploy-backends#privilege-posture).
- **Check before you commit.** `cornus daemon preflight` runs the same host checks
  `cornus serve` runs at startup and exits non-zero on a configuration it would
  refuse. Every runbook below uses it.

## Local server {#local}

Cornus runs on your machine. Its data directory holds the registry CAS and the
build cache — pass `--data-dir` (or `CORNUS_DATA`) to keep them across restarts.

Get the binary first: [installation](/introduction/installation).

### Docker {#local-docker}

The default, and the least to arrange.

**Needs:** the Docker socket, `/var/run/docker.sock`.

```sh
cornus daemon preflight                     # verify the host first
cornus serve --data-dir ~/.local/share/cornus
```

**Check:** `cornus health` prints nothing and exits 0 when the server is up.

**More:** [`dockerhost` backend](/reference/deploy-backends#dockerhost-default).

### containerd {#local-containerd}

No dockerd, but still a daemon.

**Needs:** root, a containerd socket, and the CNI plugins (`bridge`, `portmap`,
`host-local`, `loopback`) in `/opt/cni/bin`.

```sh
sudo CORNUS_DEPLOY_BACKEND=containerd cornus daemon preflight
sudo CORNUS_DEPLOY_BACKEND=containerd cornus serve --data-dir /var/lib/cornus
```

**Check:** `cornus health`.

**More:** [`containerd` backend](/reference/deploy-backends#containerd).

### Bare, with no daemon {#local-bare}

Cornus drives an OCI runtime itself and is its own supervisor — no dockerd, no
containerd.

**Needs:** root, an OCI runtime on `PATH`, and the same CNI plugins. `runc` is
the default; `CORNUS_BARE_RUNTIME` selects `crun`, `youki`, or `runsc` (gVisor).

```sh
sudo CORNUS_DEPLOY_BACKEND=bare cornus daemon preflight
sudo CORNUS_DEPLOY_BACKEND=bare cornus serve --data-dir /var/lib/cornus
```

A missing runtime fails fast at startup with an actionable error, rather than at
first deploy.

::: warning Run this one under systemd
On `bare`, **cornus is the workload supervisor** — it waits on each container's
PID 1 and applies the restart policy itself (`CORNUS_BARE_SHIM` would detach that
into per-container shims, but it is off by default). A server you start in a
terminal therefore takes workload supervision with it when it exits, and the
startup reconcile that reattaches survivors and rebuilds after a host reboot only
runs when cornus runs. `Restart=on-failure` plus `WantedBy=multi-user.target` are
what make workloads outlive a crash and a reboot.

The other backends delegate supervision to their daemon or the cluster, so losing
cornus loses the API and not the workloads — a foreground `cornus serve` stays a
reasonable dev loop there.

`cornus setup` offers a matching `cornus.service` for this arrangement; take it
rather than composing one by hand.
:::

**Check:** `cornus health`.

**More:** [`bare` backend](/reference/deploy-backends#bare).

### Incus {#local-incus}

Workloads become Incus application containers.

**Needs:** incusd **6.3+** (earlier releases have no OCI support), access to its
socket, and `skopeo` + `umoci` **on the daemon host** — incusd shells out to them
to flatten the image, so they are needed where incusd runs.

```sh
CORNUS_DEPLOY_BACKEND=incus cornus daemon preflight
CORNUS_DEPLOY_BACKEND=incus cornus serve --data-dir ~/.local/share/cornus
```

`CORNUS_INCUS_SOCKET` (default `/var/lib/incus/unix.socket`) and
`CORNUS_INCUS_PROJECT` (default `default`) override the target.

**Check:** `cornus health`.

**More:** [`incus` backend](/reference/deploy-backends#incus).

### Kubernetes, from your machine {#local-kubernetes}

The server runs locally and deploys into a cluster your kubeconfig reaches —
[k3s](https://k3s.io/), kind, minikube, or a remote one. No local container
runtime is involved.

**Needs:** a reachable cluster (`KUBECONFIG`, else `~/.kube/config`) and RBAC to
manage Deployments and Services in `CORNUS_K8S_NAMESPACE` (default `default`).

```sh
CORNUS_DEPLOY_BACKEND=kubernetes cornus daemon preflight
CORNUS_ADVERTISE_REGISTRY=192.0.2.10:5000 \
  CORNUS_DEPLOY_BACKEND=kubernetes cornus serve --data-dir ~/.local/share/cornus
```

::: warning The nodes pull the image, not you
`CORNUS_ADVERTISE_REGISTRY` is not optional here. The cluster's nodes pull built
images from this server's registry themselves, so an address like `127.0.0.1:5000`
resolves *on the node* to the node — and every deploy fails pulling an image that
is sitting on your machine. Set it to an address the nodes can reach.
:::

Cornus is primarily meant to run **inside** the cluster, where the registry is a
service endpoint the nodes reach by construction and this problem does not arise.
Prefer [in the cluster](#in-cluster) unless you specifically want the server local.

**More:** [`kubernetes` backend](/reference/deploy-backends#kubernetes-k8s).

## Remote host over SSH {#ssh}

The server runs on another machine; your CLI reaches it through an SSH tunnel
that binds no local port. Which runtime it drives is decided **there**, with
`CORNUS_DEPLOY_BACKEND` — the tunnel itself is backend-agnostic.

The shape is the same for all four:

1. Install the cornus binary on the remote host
   ([installation](/introduction/installation)).
2. Satisfy that backend's prerequisites (below).
3. Verify: `ssh HOST '<env> cornus daemon preflight'`.
4. Run it bound to loopback — the tunnel exits on the host, so its own loopback
   reaches the server:
   `ssh HOST '<env> cornus serve --addr 127.0.0.1:5000'`.
5. Configure your side: `cornus setup --scenario ssh-<backend>`.

Step 4 deserves a systemd unit rather than a shell. `cornus setup` generates a
correct `cornus.service` for the backend you chose, including its prerequisites
as comments — take it instead of composing one by hand.

### Docker {#ssh-docker}

**Needs on the host:** the Docker socket. **Env:** none (the default).

```sh
ssh HOST 'cornus daemon preflight'
cornus setup --scenario ssh-docker
```

### containerd {#ssh-containerd}

**Needs on the host:** root, a containerd socket, CNI plugins in `/opt/cni/bin`.
**Env:** `CORNUS_DEPLOY_BACKEND=containerd`.

```sh
ssh HOST 'sudo CORNUS_DEPLOY_BACKEND=containerd cornus daemon preflight'
cornus setup --scenario ssh-containerd
```

### Bare {#ssh-bare}

**Needs on the host:** root, an OCI runtime on `PATH`, CNI plugins.
**Env:** `CORNUS_DEPLOY_BACKEND=bare`.

```sh
ssh HOST 'sudo CORNUS_DEPLOY_BACKEND=bare cornus daemon preflight'
cornus setup --scenario ssh-bare
```

### Incus {#ssh-incus}

**Needs on the host:** incusd 6.3+, socket access, `skopeo` and `umoci`.
**Env:** `CORNUS_DEPLOY_BACKEND=incus`.

```sh
ssh HOST 'CORNUS_DEPLOY_BACKEND=incus cornus daemon preflight'
cornus setup --scenario ssh-incus
```

### As a container on that host {#ssh-container}

You do not have to install a binary there. On a Docker host the server can run
from the published image, reached through the same tunnel — `cornus setup
--scenario ssh-docker` asks *"Will the server run as a container on the remote
host?"* and switches to this shape.

**Needs on the host:** Docker, and a host directory for the data dir. No cornus
binary, no systemd unit.

```sh
# Check the binds there first.
ssh HOST 'docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest daemon preflight'

ssh HOST 'docker run -d --name cornus --privileged --restart unless-stopped \
  -p 127.0.0.1:5000:5000 \
  -e CORNUS_DATA=/var/lib/cornus \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest serve --addr :5000'
```

It publishes to **loopback on the remote host**, which is exactly where the SSH
tunnel exits — nothing is exposed on that host's network. No systemd unit is
offered for this shape: there is no binary for one to start, and
`--restart unless-stopped` is what brings the server back after a reboot.

The binds carry the same weight as they do locally; see
[as a container](#in-a-container) and
[running the server in a container](/guides/server-in-a-container).

**Registry caveat, all four:** if the host's deploy targets cannot pull from the
derived registry address, set `--registry-host`.

**More:** [remote container hosts over SSH](/guides/remote-docker-hosts).

## In the cluster {#in-cluster}

The arrangement Cornus is built around: the server runs as a StatefulSet in the
cluster it deploys to, so its registry is a service endpoint the nodes reach by
construction and the build cache survives restarts.

**Needs:** a cluster and `kubectl`/`helm`. Nothing on your machine but the CLI.

```sh
helm install cornus oci://ghcr.io/moriyoshi/charts/cornus
kubectl rollout status statefulset/cornus --timeout=300s
```

Helm is the recommended path: the chart is versioned and its image tag tracks the
chart version, so one command gives you a server and a manifest that match. A raw
manifest works too, but must be pinned to a **release tag**, never a branch — it
installs a privileged StatefulSet with broad RBAC.

Then point the CLI at it:

```sh
cornus setup --scenario kube-port-forward   # auto port-forward, no exposure needed
cornus setup --scenario kube-url            # or reach it at an ingress URL
```

**Registry exposure:** a NodePort registry auto-advertises the node address; for
ClusterIP or ingress set `registry.advertiseHost` (or the client's
`--registry-host`). `cornus setup` writes a matching `cornus-values.yaml` for you.

**More:** [installation](/introduction/installation),
[Helm chart values](/reference/helm-values),
[working with remote clusters](/guides/remote-clusters), and the
[quick start](/introduction/quick-start), which walks this whole path on a
single-node k3s cluster.

## As a container on the docker host {#in-a-container}

The server itself runs as a container on the Docker host it manages. The
difficulty here is entirely in the bind mounts, and a wrong one does not fail at
startup — it fails silently at deploy time.

**Needs:** Docker, and a host directory for the data dir. Nothing else — no
Compose, no bundled file.

```sh
# Check the binds first, in the image you will serve from.
docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest daemon preflight

docker run -d --name cornus --privileged --restart unless-stopped \
  -p 127.0.0.1:5000:5000 \
  -e CORNUS_DATA=/var/lib/cornus \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest serve --addr :5000
```

Run the preflight **first**: it is far cheaper to fix a bind while nothing is
running. Each of the three flags earns its place — the socket bind is what makes
this the host's docker rather than none at all, `:rshared` is what lets a mount
cornus makes inside the container reach the host, and `--privileged` is needed to
build in-process and for the kernel 9P mount.

`cornus setup --scenario docker-container` asks for the host data directory and
port and prints this command with your answers filled in.

**Check:** `cornus health`.

**More:** [running the server in a container](/guides/server-in-a-container).

## A server someone else operates {#existing}

Nothing to set up. Ask whoever runs it for the URL and, if it needs one, a
credential, then:

```sh
cornus setup --scenario url
```

If it requires authentication, the wizard walks you through enrolling an SSH key
or storing a token. See [security and authentication](/guides/security).

## After the server is up {#after}

```sh
cornus health                # is it listening?
cornus version               # does the CLI reach it through the configured profile?
cornus compose up            # deploy something
```

If `cornus version` fails where `cornus health` succeeds, the server is fine and
the connection profile is not — re-run [`cornus setup`](/cli/setup), or see
[connection config](/reference/connection-config).

**See also:** [cornus setup](/cli/setup),
[deploy backends](/reference/deploy-backends),
[cornus serve](/cli/serve),
[security and authentication](/guides/security).
