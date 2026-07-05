# Comparison with similar tools

Cornus overlaps with a crowded ecosystem, but its combination is unusual: a
**single self-contained binary** that is *at once* the registry, the image
builder, and the deploy engine, and that drives all three from your **existing**
`compose.yaml`, `docker` commands, or `devcontainer.json` — with **no new config
DSL** and **no pre-existing registry, `buildkitd`, or GitOps controller** to
stand up first. Most tools in this space orchestrate components you already run;
Cornus *is* those components. The tools it is most often compared to fall into
three groups.

## Inner-loop "dev on Kubernetes" orchestrators

[Skaffold](https://skaffold.dev/), [Tilt](https://tilt.dev/),
[DevSpace](https://www.devspace.sh/), [Garden](https://garden.io/), and
[Okteto](https://www.okteto.com/) automate the build -> push -> deploy loop
against a cluster. They are **orchestrators**: they shell out to your builder
(`docker` / BuildKit / kaniko), push to a registry you provide, and apply
manifests / Helm / kustomize you write, all driven by a tool-specific config file
(Skaffold YAML, a `Tiltfile`, `devspace.yaml`, Garden's project graph). Cornus
differs on two axes: it **bundles** the builder and registry rather than calling
out to them, and it consumes the **Docker artifacts you already have** — a
Compose file or a devcontainer — instead of a new DSL. Where Okteto and DevSpace
sync your source into a dev container running in the cluster, Cornus keeps your
files on your machine and streams only the bytes a build or bind mount actually
reads over 9P.

## Local and remote-cluster bridges

[Telepresence](https://www.telepresence.io/), [mirrord](https://mirrord.dev/),
and [Gefyra](https://gefyra.dev/) run a process **locally** while making it
behave as if it were **in** the cluster — intercepting a running pod's traffic,
environment, and file reads down to your laptop. Cornus solves the adjacent
problem from the other direction: it **deploys the workload into the cluster**
and brings the cluster back to you — published ports auto-forward to
`127.0.0.1`, `cornus exec` / `cornus port-forward` reach any container port, a
SOCKS5 conduit resolves `*.cornus.internal` to services by name, and the
workload-to-workload hub connects services across NAT and cluster boundaries. If
your goal is "run my code locally against cluster dependencies," reach for
mirrord or Telepresence; if it is "get my Compose project *running* in the
cluster with the inner-loop conveniences of local Docker," that is Cornus.

## Remote file-sync tools

A whole category of remote-dev tooling exists just to keep a local directory and a
remote one in step. Almost all of it reduces to **two sync engines**:
[Mutagen](https://mutagen.io/) (with its
[mutagen-compose](https://mutagen.io/documentation/orchestration/compose/)
integration; acquired by Docker in 2024 and now the basis of Docker Desktop's
synchronized bind mounts) and [Syncthing](https://syncthing.net/), descendants of
the classic [Unison](https://github.com/bcpierce00/unison) and `rsync`
(+ `lsyncd`). The Kubernetes dev tools mostly wrap one of the two —
[ksync](https://ksync.github.io/ksync/) and [Okteto](https://www.okteto.com/) drive
Syncthing, [Garden](https://garden.io/)'s code-sync drives Mutagen — while
[DevSpace](https://www.devspace.sh/) ships its own and
[Skaffold](https://skaffold.dev/docs/filesync/) / [Tilt](https://tilt.dev/) copy
changed files into the running container on change. All of them share one model:
**copy** your tree to the far side, then continuously reconcile the two copies —
buying local-speed remote reads and offline tolerance, at the cost of a second
materialized copy, an initial full transfer, and bidirectional conflict resolution.

Cornus is not in that camp at all. It does not sync, it **serves**, so it is really
the network-filesystem family — **sshfs**, **NFS**, **virtiofs** (Docker Desktop's
VM bind path), 9P — that it belongs to. During a remote build or a client-local
bind mount, the caller runs a read-through 9P server and the workload reads the
caller's files **in place** — a single source of truth, so no divergence, no
conflict resolution, and no upfront copy. What distinguishes it from a plain
network mount is the transport and the scoping: 9P tunneled over one WebSocket
(so it works through NAT with no mount daemon on either side), confined to the
context / named-context / mount directories and filtered through `.dockerignore`,
and — with `--lazy` — served on demand, so only the bytes a build or mount actually
touches ever cross the wire (a 20 MB context whose build reads 11 bytes transfers
11 bytes). The trade-off is the mirror image of sync's: an uncached read depends on
the link rather than on a resident local copy, so Cornus aims at the inner-loop /
dev case, not long-lived offline work. If your workflow is "edit here, run there,
keep both sides converged," a dedicated syncer like Mutagen is purpose-built for it;
Cornus folds the equivalent capability into its own transport, with nothing extra
to run. (Mutagen also forwards network ports, which Cornus covers with its own
per-connection tunnels — see [networking](/guides/networking).)

## The components Cornus subsumes

| You would otherwise run | Cornus's take |
| --- | --- |
| [BuildKit](https://github.com/moby/buildkit) / `buildkitd` as a daemon | embeds the **same** BuildKit solver in-process — full `buildx` feature set, no daemon |
| [Docker Registry](https://github.com/distribution/distribution) (`distribution`), [Zot](https://zotregistry.dev/), [Harbor](https://goharbor.io/) | a built-in tiny OCI Distribution v1.1 registry with a pluggable content store |
| [Kompose](https://kompose.io/) / [Docker Compose Bridge](https://docs.docker.com/compose/bridge/) | those convert Compose to manifests **once**; Cornus keeps Compose as the live control surface |
| [nerdctl](https://github.com/containerd/nerdctl) (Docker CLI over containerd) | the containerd deploy backend runs Compose projects natively on a bare containerd host, and also targets Docker and Kubernetes |
| stock `docker` / `docker compose` against a local daemon | the same commands, redirected to a remote Cornus server (`cornus daemon docker`, `cornus compose`), with files streamed from your machine |

The closest single-binary analogue is [Werf](https://werf.io/), which also builds
and deploys to Kubernetes from one binary — but Werf is Git-driven and still
relies on an external registry and a Helm-based apply, whereas Cornus is
Compose / devcontainer-driven, ships its own registry, and reconciles a
`DeploySpec` imperatively across Docker, containerd, and Kubernetes alike.

## See also

- [What is Cornus?](/introduction/what-is-cornus) — the three subsystems and the end-to-end flow.
- [Quick start](/introduction/quick-start) — go from a Compose file to a running workload.
- [Architecture](/architecture/) — how the pieces fit and why.
