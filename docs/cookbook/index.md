# Cookbook

Where the [Guides](/guides/) show how to use one feature at a time, these pages are
end-to-end walkthroughs that combine several features to solve a real problem — the
exact commands, a complete deploy spec, and an explanation of how the pieces fit
together. Each one is written to be adapted directly to your own project.

## Scenarios

### [Running an AI agent in a container with client egress routing](/cookbook/ai-agent-egress)

Run an autonomous AI agent as a workload on the cluster, route its outbound LLM
API calls through your own network (corporate proxy / VPN / SASE), and broker the
API key in at run time so it never lands in the image. Combines client-side
[egress](/guides/egress) and credential brokering with the
[deploy spec](/reference/deploy-spec).

### [A remote development environment on a cluster](/cookbook/remote-dev-environment)

Develop from a light laptop against a powerful remote cluster: edit files locally,
run them remotely over 9P, reach ports at `localhost`, and drive the stock docker /
devcontainer tooling — including opening the Dev Container in VS Code or Zed via the
[Docker API proxy](/cli/daemon). Combines [connection profiles](/guides/remote-clusters),
[Compose / devcontainers](/guides/compose-devcontainers-docker), and client-local
bind mounts.

### [Ephemeral preview environments](/cookbook/preview-environments)

Build an image and spin up a short-lived environment per pull request, then expose
it publicly through a hosted tunnel so reviewers can click a URL — and tear it down
just as fast. Combines [building](/guides/building-images),
[deploying](/guides/deploying-workloads), and [tunnels](/guides/tunnels).

### [Docker-free build and deploy from CI](/cookbook/dockerless-ci)

Build and ship to a cluster with no Docker daemon anywhere: the in-cluster build
engine builds, the bundled registry stores, and containerd / Kubernetes pulls.
Combines the [build engine](/guides/building-images), the
[registry](/guides/registry), and the [deploy backends](/reference/deploy-backends).

### [Shipping a local Compose project to Kubernetes unchanged](/cookbook/compose-to-kubernetes)

Take a working `compose.yaml` and run the same file, with the same command, on a
real Kubernetes cluster — no manifest rewrite. Combines the
[Compose client](/guides/compose-devcontainers-docker) and
[connection profiles](/guides/remote-clusters).

### [Wiring microservices together over the hub overlay](/cookbook/microservices-hub)

Let independently-deployed workloads reach each other by stable name across the
cluster or across backends, without hard-coding addresses. Combines the
[hub overlay](/guides/hub) and the [deploy spec](/reference/deploy-spec).
