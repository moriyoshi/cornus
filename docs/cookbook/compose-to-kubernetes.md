# Shipping a local Compose project to Kubernetes unchanged

## The scenario

A team has a working `compose.yaml` they run locally every day. They want the
**same file** on a real Kubernetes cluster — for a shared staging environment or
an integration run — without rewriting it into Deployments, Services, and PVCs.
With Cornus the Compose file is the live control surface on every backend, so the
move is a change of connection profile, not a change of source.

## What you'll use

- The Compose-compatible client that drives builds and deploys against a server — see [Compose, devcontainers, and the docker CLI](/guides/compose-devcontainers-docker) and [`cornus compose`](/cli/compose).
- Connection profiles to switch from a local server to an in-cluster one — see [Working with remote clusters](/guides/remote-clusters).
- The deploy engine's translation of Compose concepts into the native spec — see [Deploy spec](/reference/deploy-spec) and [Deploy backends](/reference/deploy-backends).

## Walkthrough

1. **Start from the Compose file you already run.** A plain multi-service
   project — a web front end that talks to an API, which talks to a database over
   a user network:

   ```yaml
   # compose.yaml
   name: shop
   services:
     web:
       build: ./web
       ports:
         - "8080:80"
       depends_on:
         - api
       networks:
         - frontend
     api:
       build: ./api
       environment:
         DATABASE_URL: postgres://db:5432/shop
       networks:
         - frontend
         - backend
     db:
       image: postgres:16
       volumes:
         - db-data:/var/lib/postgresql/data
       networks:
         - backend
   networks:
     frontend:
     backend:
   volumes:
     db-data:
   ```

2. **Run it locally, exactly as today.** Against a local Cornus server (the
   default `dockerhost` backend), `cornus compose up` builds the `build:`
   services, deploys the stack, and holds the published port open at
   `127.0.0.1:8080`.

   ```sh
   cornus compose up --build
   # -> forwarding 127.0.0.1:8080 -> :80 ; curl http://127.0.0.1:8080 answers
   ```

3. **Point a profile at the cluster.** Store the in-cluster server once. For a
   cluster with no ingress, name its Service and let the CLI open the
   port-forward around each command.

   ```sh
   cornus config set-context staging \
     --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
   cornus config use-context staging
   ```

4. **Run the identical command against the cluster.** Same file, same command —
   the only difference is the selected profile, which resolves to the
   in-cluster server whose `CORNUS_DEPLOY_BACKEND=kubernetes`.

   ```sh
   cornus compose up --build
   ```

   The `build:` services build in the cluster and push to the bundled registry;
   each service becomes a Deployment named `shop-web` / `shop-api` / `shop-db`
   plus a Service for its published ports; the `frontend` / `backend` user
   networks are realised on the cluster; and `8080` is auto-forwarded back to
   `127.0.0.1:8080` on your machine for the session's lifetime — so `curl
   http://127.0.0.1:8080` answers even though the workload runs in the cluster.

5. **Inspect and tear down the same way.**

   ```sh
   cornus compose ps
   cornus compose logs --follow web
   cornus compose down --volumes     # --volumes also removes the db-data PVC
   ```

## How it works

A Compose file is translated into the native [deploy spec](/reference/deploy-spec)
internally, and the same spec is applied to whichever backend the server runs, so
every core concept carries across unchanged:

- **Services** become one deployment each, named `<project>-<service>`.
- **`ports:`** become published ports. During a session they auto-forward to
  `127.0.0.1:<host>` on every backend — including Kubernetes — so the workload
  answers on localhost. Pick per-port listeners (the default) or a single SOCKS5
  proxy that reaches services by name with `--conduit`.
- **`networks:`** become user-defined networks: members of the same network
  resolve each other by service name (and aliases). On Kubernetes the default
  driver is `services` (DNS only, any cluster); `bridge` / `ipvlan` / `macvlan`
  (Multus) or `cilium` are opt-in via `CORNUS_K8S_NET_DRIVER`.
- **`volumes:`** become managed volumes — a named volume is a project-scoped
  store that survives a single deployment's deletion (a PVC on Kubernetes, a
  Docker named volume on `dockerhost`); an anonymous one is ephemeral.
- **`depends_on`**, **`healthcheck`**, **`deploy.replicas`**, and
  **`deploy.update_config`** all map through as well.

Because the backend is selected on the server, the CLI-side workflow is identical
across `dockerhost`, `containerd`, `bare`, and `kubernetes` — see
[Deploy backends](/reference/deploy-backends).

### What differs on Kubernetes

A few Compose knobs have no Kubernetes equivalent and are handled per field
(the [deploy spec reference](/reference/deploy-spec) calls out each one):

- A port's `hostIP` (Compose `127.0.0.1:8080:80`) is honored by the host
  backends but Kubernetes Services have no equivalent.
- UDP published ports work on `dockerhost` / `containerd` / `bare`, but Kubernetes
  port-forward is TCP-only, so a `/udp` mapping is skipped there.
- A healthcheck becomes a Docker healthcheck on `dockerhost` and an exec
  liveness / readiness probe on Kubernetes.
- `deploy.update_config` maps only onto the Kubernetes Deployment
  `strategy.rollingUpdate`; host backends recreate a single instance.
- Compose `labels:` become pod-template **annotations** on Kubernetes, not
  labels. Many host-only knobs (`init`, `stop_signal`, `ulimits`, `devices`, and
  similar) are ignored on Kubernetes with a warning.

## Variations

- **Detached staging.** `cornus compose up --build -d` hands the mounts and
  forwarded ports to a background helper and returns; `cornus compose down` stops
  it later.
- **Reach services by name.** `cornus compose up --conduit socks5` swaps the
  per-port listeners for one proxy, so `web.cornus.internal` and
  `db.cornus.internal` resolve through it.
- **Layered overrides.** Keep the base `compose.yaml` and add
  `-f compose.staging.yaml` for cluster-only tweaks, still the same command.

**See also:** [Compose, devcontainers, and the docker CLI](/guides/compose-devcontainers-docker) · [Deploying workloads](/guides/deploying-workloads) · [Working with remote clusters](/guides/remote-clusters) · [Deploy spec](/reference/deploy-spec) · [Deploy backends](/reference/deploy-backends)
