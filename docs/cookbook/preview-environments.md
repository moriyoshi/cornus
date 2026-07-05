# Ephemeral preview environments

## The scenario

For every pull request or feature branch you want a throwaway environment: build the branch's image, deploy it under a per-PR name on the cluster, and hand reviewers a public URL they can click — from a browser or a phone — without VPN, kubeconfig, or a permanent ingress. When the PR merges or closes, it all disappears. Cornus does the whole loop with one binary: [`cornus build`](/cli/build) produces the image, [`cornus deploy`](/cli/deploy) stands up a uniquely-named workload, and [`cornus tunnel`](/cli/tunnel) gives it a public https URL. Tear-down is a single `--delete`.

## What you'll use

- [`cornus build`](/cli/build) — build the branch image with the in-process BuildKit engine and push it to the bundled registry.
- [`cornus deploy`](/cli/deploy) — apply a [deploy spec](/reference/deploy-spec) under a per-PR `name`.
- [`cornus tunnel`](/cli/tunnel) — expose the workload's port to the public internet through a hosted relay.
- [Tunnels](/guides/tunnels) — the model behind the shareable URL.

## Walkthrough

The steps below assume a connection profile already selects the cluster server (see [remote clusters](/guides/remote-clusters)); otherwise add `--server https://cornus.example.com` to each command.

**1. Name everything after the PR.** A single variable scopes the image tag and the deployment name so previews never collide:

```sh
PR=123
IMAGE="registry.example:5000/app:pr-${PR}"
NAME="app-pr-${PR}"
```

**2. Build and push the branch image.** The build runs on the server (with a `--builder` or a profile that names one); the caller needs no Docker and no build privileges:

```sh
cornus build -t "$IMAGE" .
```

**3. Deploy it under the per-PR name.** Generate the spec with the PR-scoped name and image, then apply it detached so the environment outlives the command:

```sh
cat > preview.yaml <<YAML
name: ${NAME}
image: ${IMAGE}
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
YAML

cornus deploy -f preview.yaml --detach
```

**4. Publish a public URL.** `cornus tunnel` asks the server to host a public tunnel to the workload's port and prints the URL; it reaches the port even if the workload never published it:

```sh
cornus tunnel --authtoken "$NGROK_AUTHTOKEN" "$NAME" 80
# prints e.g. https://abcd-1234.ngrok-free.app  -- paste into the PR
```

Post that URL as a PR comment and reviewers click through. The command stays up until `Ctrl-C`; for an always-on preview an operator can set a default credential (`CORNUS_TUNNEL_AUTHTOKEN`) on the server so callers omit `--authtoken`.

**5. Tear it all down** when the PR closes — delete the deployment by name (the tunnel drops with its command):

```sh
cornus deploy -f preview.yaml --delete
```

## How it works

Each stage is one server-side subsystem. `cornus build` runs the BuildKit engine on the server and pushes the result into the bundled registry, tagged for the registry the profile/server advertises — so the deploy's `image` ref resolves without a separate registry to operate. `cornus deploy --detach` POSTs the spec once and exits, leaving the workload running with no client session; because the `name` is PR-scoped and managed resources are labeled with it, apply and delete are idempotent and previews for different PRs never clash. `cornus tunnel` is independent of how the workload was deployed: the cornus **server** hosts the tunnel in-process and bridges each inbound connection to the workload through the same byte-bridge that [`cornus port-forward`](/cli/port-forward) uses, so it reaches a port on any backend (Docker host, containerd, or Kubernetes) and needs no ingress. The tunnel credential is injected by the client on the already-authenticated request, so the server never knows it beforehand. The backend (`ngrok` by default, or `ssh` / `cloudflare` / `tailscale`) is chosen server-side; see [tunnels](/guides/tunnels).

Because the deploy is detached, published ports bind on the server host rather than being auto-forwarded to your machine — which is exactly right here: the public entry point is the tunnel, not a local listener. Detached deploys also reject client-local mounts and client-sourced credentials, so a preview built this way is fully self-contained.

## Variations

**Use a Compose project instead of a raw spec.** If the branch ships a Compose file, scope the project name per PR and bring it up detached, then tear down with `down`:

```sh
cornus compose -p "pr-${PR}" up --build -d
cornus tunnel "pr-${PR}-web" 80
# later:
cornus compose -p "pr-${PR}" down --volumes
```

**Expose raw TCP** (a database, a gRPC endpoint) instead of HTTP:

```sh
cornus tunnel --proto tcp "$NAME" 5432
```

**Use a cluster ingress instead of a tunnel.** On a Kubernetes cluster that already runs an ingress controller, Cornus can hand each preview a public URL straight from a `networking.k8s.io/v1` Ingress — no relay process to keep alive, and the URL survives the detached deploy (a tunnel lives only as long as its command). An operator configures the cluster once through the Helm chart's `ingress` values (which set the server defaults `CORNUS_INGRESS_DOMAIN`, `CORNUS_INGRESS_CLASS`, and `CORNUS_INGRESS_TLS_ISSUER`): a wildcard preview domain such as `preview.example.com`, the ingress class, and a cert-manager cluster-issuer for HTTPS. Then step 3's spec just enables ingress and step 4 disappears — the host is auto-derived from the deployment name, so there is no per-PR URL to compute:

```sh
cat > preview.yaml <<YAML
name: ${NAME}
image: ${IMAGE}
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
ingress:
  enabled: true          # host auto-derived as <name>.<CORNUS_INGRESS_DOMAIN>
  tls: { }               # HTTPS via the server's default cluster-issuer
YAML

cornus deploy -f preview.yaml --detach
# reviewers browse https://app-pr-123.preview.example.com
```

With a Compose project the same thing is a bare `x-cornus-ingress: {}` on the web service; the host is namespaced per project as `<service>.<project>.<domain>` (e.g. `web.pr-123.preview.example.com` for `-p pr-123`), so many previews coexist on one base domain without colliding. Put shared overrides like `domain:` or `class_name:` in a project-level `x-cornus-ingress:` block at the top of the file — each service still opts in individually. Any server default is client-overridable per workload unless the operator pinned the domain with `CORNUS_INGRESS_ENFORCE_DOMAIN`. A workload can also front several names via `hosts:` (the token `@` maps to the apex domain). Ingress is Kubernetes-only; on the Docker-host or containerd backends the field is ignored with a warning, so the file stays portable. Tear-down is unchanged — `--delete` removes the deployment and Kubernetes garbage-collects the Ingress with it. Choose per cluster: the tunnel needs no ingress controller and works on any backend; the ingress is cluster-native and outlives the command but needs a controller plus wildcard DNS (and a cert-issuer for HTTPS).

**Wire it into CI.** The whole sequence is scriptable and daemon-free — build, deploy, tunnel — so a pipeline step can create the preview on `pull_request: opened` and run `cornus deploy -f preview.yaml --delete` on `closed`. The tunnel URL is printed to stdout; capture it and post it back to the PR.

**See also:** [building images](/guides/building-images) · [deploying workloads](/guides/deploying-workloads) · [networking and conduits](/guides/networking) · [tunnels](/guides/tunnels) · [Cookbook](/cookbook/)
