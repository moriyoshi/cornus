# A remote development environment on a cluster

## The scenario

You develop on a light laptop but the code needs a powerful machine — a big build, a GPU, a database that would flatten your fan. You want the best of both: keep editing files *locally* in your own editor, but have the code *run* remotely on the cluster, with the workload's ports reachable at `localhost` and your stock Docker / Dev Container tooling working unchanged. Cornus makes the remote server feel local: a [connection profile](/reference/connection-config) removes the endpoint plumbing, [client-local bind mounts](/guides/deploying-workloads#mount-a-client-local-directory-into-a-remote-workload-local-mount-streamed-over-9p) stream your working tree over 9P so edits sync with no copy step, and published ports auto-forward back to your machine.

## What you'll use

- [Connection profiles](/reference/connection-config) — store the server once with [`cornus config`](/cli/config).
- [`cornus compose`](/cli/compose) — bring a Compose project (or a [Dev Container](/guides/compose-devcontainers-docker)) up against the server.
- [Client-local bind mounts over 9P](/guides/deploying-workloads#mount-a-client-local-directory-into-a-remote-workload-local-mount-streamed-over-9p) — your source lives on the laptop, streamed on demand into the remote workload.
- [Automatic port forwarding](/guides/deploying-workloads) — published ports answer at `127.0.0.1:<host>` for the session's lifetime.
- [`cornus daemon docker`](/cli/daemon) — optionally expose a `DOCKER_HOST` so the official `devcontainers` CLI (or stock `docker`) drives the remote server.

## Walkthrough

**1. Store the cluster as a profile** so no command needs `--server` or a token. For an in-cluster cornus with no ingress, name the Service and let the CLI port-forward to it around each command:

```sh
cornus config set-context devbox \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus
cornus config use-context devbox
```

(For a server that has a URL, use `--server https://cornus.example.com --token "$(cat token.jwt)"` instead. The `--kube-auth-*` flags mint a short-lived token from your own kube access, so there is no static secret to manage — see [remote clusters](/guides/remote-clusters).)

**2. Describe the environment as a Compose project.** The bind mount under `volumes:` is a path on *your* laptop; `ports:` are what you want to reach at `localhost`:

```yaml
name: devbox

services:
  app:
    build: .                      # built by the cornus engine, pushed to its registry
    command: ["npm", "run", "dev"]
    working_dir: /workspace
    volumes:
      - ./:/workspace             # client-local: streamed over 9P, edits sync live
    ports:
      - "3000:3000"               # dev server, reachable at 127.0.0.1:3000
    environment:
      NODE_ENV: development
    depends_on:
      - db

  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: dev
    volumes:
      - pgdata:/var/lib/postgresql/data
    ports:
      - "5432:5432"

volumes:
  pgdata:                         # named: shared/persistent across up/down
```

**3. Bring it up in the foreground.** It builds what needs building on the server, deploys in dependency order, holds your bind mount over 9P, auto-forwards `3000` and `5432` to `127.0.0.1`, and streams the logs:

```sh
cornus compose up --build
```

**4. Edit locally, run remotely.** Your editor writes `./src/...` on the laptop; the `app` container sees the change through the 9P mount and the dev server reloads. Open `http://localhost:3000` — the request is tunneled to the workload pod (straight to the pod with your kubeconfig for a cluster profile, else through the server). `psql -h 127.0.0.1 -p 5432` reaches the remote database the same way. `Ctrl-C` tears down what `up` brought up.

**5. Reach ports that were never published**, on demand, without editing the spec:

```sh
cornus port-forward app 9229:9229     # e.g. a debugger port
```

## How it works

The moving parts fit together so that nothing about your inner loop changes. The **connection profile** is the CLI-side, kubeconfig-style file that carries the endpoint, auth, and (here) the in-cluster port-forward target, so every `cornus compose` invocation resolves the server with nothing on the command line. **Client-local bind mounts** are the key to editing locally: a Compose `volumes:` entry that names a host path is streamed over 9P from your machine and served from the server's own mount area for the session's lifetime, so the workload reads your files in place — no upfront copy, no rsync, and the mount is always permitted without relaxing the host-privilege policy. **Published ports** are auto-forwarded to `127.0.0.1:<host>`, even when the backend is Kubernetes, so a remote workload answers locally exactly as `docker compose` would. All three are tied to the live foreground `up`; a detached `up -d` hands the mounts and forwards to a background client agent instead (inspect it with `cornus daemon status`). The details are in the [remote clusters](/guides/remote-clusters) and [deploying workloads](/guides/deploying-workloads) recipes.

## Variations

**Use a Dev Container instead of a Compose file.** If your repo has a `.devcontainer/devcontainer.json`, `cornus compose` reads it natively — no hand-written Compose file — running its lifecycle hooks (`initializeCommand` on the host, then `postCreate` / `postStart` / `postAttach` in the container) and bind-mounting the project at `workspaceFolder` over 9P:

```sh
cornus compose --devcontainer . up
```

**Open the devcontainer in VS Code or Zed against the remote server.** Run the client-side Docker Engine API proxy and point `DOCKER_HOST` at it, and stock `docker`, `docker compose`, the official `devcontainers` CLI, and editor Dev Container support all run their containers on the remote cornus, with your local bind-mount directories streamed over 9P:

```sh
cornus daemon docker -d
export DOCKER_HOST="unix://$XDG_RUNTIME_DIR/cornus-docker.sock"
devcontainer up --workspace-folder .      # official CLI, remote execution
```

The proxy speaks Docker's exact protocol (create/start, attach, wait, the lifecycle event stream), which is what lets the official `@devcontainers/cli` — the engine behind VS Code's Dev Containers extension — drive it unmodified. So launch your editor from the same shell (so it inherits `DOCKER_HOST`) and use its normal Dev Container flow, and the container runs remotely:

- **VS Code** — install the Dev Containers extension, run `code .`, then **Dev Containers: Reopen in Container**.
- **Zed** — run `zed .` and open the project's Dev Container; Zed launches it through the same Docker endpoint.

Because the proxy does not emulate Docker's `/build` endpoint (builds belong to [`cornus build`](/cli/build)), reference a prebuilt `image:` in `devcontainer.json` rather than a `build:` / `dockerFile:` — build it first with `cornus build -t <registry>/devcontainer:latest .` if it comes from a Dockerfile.

**Reach every service by name over one proxy.** Set the profile's conduit to SOCKS5 so a single split-tunnel proxy reaches `app`, `db`, and any other service by name (and everything else egresses directly):

```sh
cornus config set-context devbox --merge --conduit-mode socks5
```

**See also:** [remote clusters](/guides/remote-clusters) · [Compose, devcontainers, and the docker CLI](/guides/compose-devcontainers-docker) · [connection config](/reference/connection-config) · [Cookbook](/cookbook/)
