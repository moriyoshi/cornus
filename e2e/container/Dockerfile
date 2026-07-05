# syntax=docker/dockerfile:1
#
# All-in-one E2E runner image for cornus.
#
# Bundles everything the Starlark E2E harness needs to run the FULL suite —
# including the privileged in-process build engine and the kind/Kubernetes path —
# inside a single container:
#
#   - dockerd (Docker-in-Docker) so the docker + kube targets have a daemon
#   - the dind base image's standalone containerd + the staged CNI plugins for
#     the containerd target (started on demand by entrypoint.sh)
#   - incusd + incus-client + skopeo + umoci for the incus target (OCI app
#     containers; incusd started + minimally initialized on demand by entrypoint.sh)
#   - runc + overlayfs + user-namespace tooling for the in-process build engine
#   - kind + kubectl for the kube target
#   - ssh-keygen/ssh-agent/ssh-add for the ssh-mount build scenario
#   - the cornus / cornus-e2e binaries and the scenarios
#
# It MUST be run privileged (the build engine mounts overlayfs and creates
# namespaces; kind and the 9p mount sidecar need it too):
#
#   docker run --rm --privileged cornus-e2e:latest
#
# Select what runs with E2E_TARGETS (default "docker"); add "kube" to also spin
# up an in-container kind cluster, "containerd"/"bare" to run the backend-agnostic
# subset against an in-container containerd / the daemonless OCI runtime, and/or
# "incus" to run it against an in-container incusd. See entrypoint.sh.

# --- build the web UI so the binary embeds the real SPA ----------------------
# web.star drives `cornus web`; embedding the UI (pkg/webui //go:embed dist) lets
# it assert the real SPA is served, not the "not built" fallback. This stage runs
# before the Go build so the assets exist on disk at compile time. Pinned to the
# BUILD platform (arch-independent output) for parity with the release Dockerfile.
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS webui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- build the two static binaries -------------------------------------------
FROM golang:1.26-bookworm AS build
WORKDIR /src
# BuildKit cache mounts (see the root Dockerfile for the rationale) persist the
# downloaded module cache and the compiler build cache so `go mod download` /
# `go build` reuse artifacts across builds. TARGETARCH keys the build-cache mount
# per target so it never mixes objects across architectures; the module cache is
# architecture-independent and shares one `gomod` mount with the release image.
ARG TARGETARCH
COPY go.mod go.sum ./
# third_party holds the in-repo yamux fork that go.mod's `replace` points at, so it
# must be present before `go mod download` reads the replaced module's go.mod
# (the cached module-download layer would otherwise fail: no such file or directory).
COPY third_party/ ./third_party/
RUN --mount=type=cache,target=/go/pkg/mod,id=gomod \
    go mod download
COPY . .
COPY --from=webui /src/pkg/webui/dist/ pkg/webui/dist/
# Fully static (netgo/osusergo) so the binaries run on the Alpine-based dind image.
# BUILD_TAGS lets a caller add optional features (e.g. `otelcol` for the embedded
# OpenTelemetry Collector the otel-collector scenario needs in the sidecar image);
# the default keeps the image lean, matching the released image's default posture.
ARG BUILD_TAGS="netgo osusergo"
RUN --mount=type=cache,target=/go/pkg/mod,id=gomod \
    --mount=type=cache,target=/root/.cache/go-build,id=gobuild-e2e-${TARGETARCH} \
    CGO_ENABLED=0 go build -tags "${BUILD_TAGS}" -ldflags "-s -w" -o /out/cornus     ./cmd/cornus \
 && CGO_ENABLED=0 go build -tags "netgo osusergo" -ldflags "-s -w" -o /out/cornus-e2e ./cmd/cornus-e2e
# Fail the build loudly if a stale Docker layer-cache served a cornus that predates
# a deploy backend: without this, an image missing (e.g.) the bare backend still runs
# — defaultBackendFactory's default arm silently falls back to dockerhost, so the
# bare E2E target masquerades as green while never exercising bare. Each backend has
# a unique compiled-in marker string; assert all four are present in the linked binary.
RUN for m in \
        'bare: OCI runtime' \
        'dockerhost: remote mode requires' \
        'containerd: remote mode requires' \
        'incus: connecting to daemon at' \
        'kubernetes: exec requires a real cluster connection'; do \
      strings /out/cornus | grep -qF "$m" || { echo "FATAL: cornus binary is missing a deploy backend (marker: $m); likely a stale go-build layer — rebuild with --no-cache" >&2; exit 1; }; \
    done

# --- runner image: Docker-in-Docker + kind/kubectl + build engine + incus ----
# Debian (glibc) base rather than the Alpine docker:dind image so the incus
# target can install a current, OCI-capable incus from the official Zabbly apt
# repo. Alpine only packages the incus 6.0 LTS line (predates OCI support, added
# in 6.3), and mixing an edge incus onto the Alpine base broke `nft` via a
# libnftables/libnftnl symbol skew; Debian ships a consistent nftables + skopeo +
# umoci in one repo, so there is no skew.
#
# Docker-in-Docker is reconstructed here: the Docker Engine (dockerd + the
# containerd.io the containerd target also uses + runc) comes from Docker's
# official Debian apt repo, and the battle-tested dind BOOTSTRAP scripts
# (dockerd-entrypoint.sh / docker-entrypoint.sh / dind / modprobe) are copied from
# the docker:dind image — they are architecture-independent shell. The docker:dind
# BINARIES are musl-linked (runc) so are not reused; only the scripts are.
FROM debian:bookworm-slim

# Versions of the out-of-tree tools, overridable at build time.
ARG KIND_VERSION=v0.24.0
ARG KUBECTL_VERSION=v1.31.0
# Multus support (staged into the runner image so E2E_MULTUS runs are hermetic —
# no runtime fetch of plugins/images). CNI_PLUGINS provides the `bridge` (and
# macvlan/ipvlan) binaries the kindest/node image does NOT ship — it bundles
# only the minimal set kindnet needs (ptp/host-local/portmap/loopback) — which
# Multus delegates to; without them the CNI ADD fails and annotated pods hang.
# `static` is the IPAM plugin the netdriver's pinned-addressing mode delegates
# to (compose user networks assign deterministic per-service IPs carried in the
# pod's network-selection annotation — matrix row A').
ARG CNI_PLUGINS_VERSION=v1.5.1
ARG CRANE_VERSION=v0.20.2
ARG MULTUS_IMAGE=ghcr.io/k8snetworkplumbingwg/multus-cni:v4.1.4-thick
# @devcontainers/cli: the official devcontainer CLI (the engine VS Code's Dev
# Containers extension shells out to), driven by devcontainer-vscode.star
# against the cornus docker proxy.
ARG DEVCONTAINERS_CLI_VERSION=0.80.0
# Docker Engine + CLI are PINNED, not "whatever is latest in the apt repo".
# Leaving them unpinned made the docker leg non-reproducible: when Docker's
# stable repo published 29.x the runner silently jumped 27.5.1 -> 29.6.2 and the
# docker-only scenarios regressed (compose flag parsing, the foreground
# `docker run` attach the devcontainer CLI drives, sshd bring-up). Pin to the
# last-known-good 27.5.1 — the same line the dind bootstrap scripts below are
# pinned to (docker:27-dind) — so CI is deterministic and upstream Docker
# releases cannot break it out from under us. Bump deliberately, with a CI run.
ARG DOCKER_VERSION=27.5.1
# The compose + buildx CLI plugins are PINNED and installed EXPLICITLY, not left to
# apt's Recommends. docker-ce only Recommends docker-compose-plugin /
# docker-buildx-plugin, and this image installs with --no-install-recommends, so a
# cold cache rebuild dropped them entirely: `docker compose` then had no plugin and
# the docker CLI parsed `compose -f ...` itself, failing with "unknown shorthand
# flag: 'f' in -f" (dockerd.star). A warm cache still carried an old layer that
# happened to include them, which is why the break was intermittent. Name the
# packages here so they are always present, and pin the versions so the compose
# convergence behavior the scenarios exercise (compose v5; see dockerd.star) is
# reproducible. Bump deliberately, with a CI run.
ARG DOCKER_COMPOSE_VERSION=5.3.1
ARG DOCKER_BUILDX_VERSION=0.35.0

ENV DEBIAN_FRONTEND=noninteractive

# Base packages:
#   uidmap: newuidmap/newgidmap for the rootless build path.
#   iproute2/kmod/iptables/nftables: the container + firewall plumbing dockerd,
#     containerd and incus need (ip, modprobe, both firewall backends — a single
#     consistent Debian nftables, so `nft` works and incus's bridge inits).
#   util-linux/mount: a full mount(8) for the 9p mount sidecar path.
#   fuse-overlayfs/e2fsprogs/xfsprogs: storage drivers for dockerd + incus.
#   openssh-client/server: ssh-keygen/ssh-agent/ssh-add + sshd for the ssh scenarios.
#   skopeo + umoci: Incus's OCI-image copy + unpack tools (consistent Debian builds).
#   nodejs/npm: runtime for @devcontainers/cli.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        bash curl git ca-certificates gnupg \
        uidmap iproute2 kmod iptables nftables \
        util-linux mount \
        fuse-overlayfs e2fsprogs xfsprogs \
        openssh-client openssh-server \
        skopeo umoci \
        nodejs npm \
 && rm -rf /var/lib/apt/lists/*

# Docker Engine (dockerd + containerd.io + runc + CLI) from Docker's official
# Debian apt repo, plus the dind bootstrap scripts from the docker:dind image
# (arch-independent shell that prepares cgroups/storage and execs dockerd).
RUN set -eux; \
    install -m0755 -d /etc/apt/keyrings; \
    curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc; \
    chmod a+r /etc/apt/keyrings/docker.asc; \
    . /etc/os-release; \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian ${VERSION_CODENAME} stable" > /etc/apt/sources.list.d/docker.list; \
    apt-get update; \
    ver="$(apt-cache madison docker-ce     | awk -v v="5:${DOCKER_VERSION}-" 'index($3, v)==1 {print $3; exit}')"; \
    cliver="$(apt-cache madison docker-ce-cli | awk -v v="5:${DOCKER_VERSION}-" 'index($3, v)==1 {print $3; exit}')"; \
    { test -n "$ver" && test -n "$cliver"; } || { echo "docker-ce ${DOCKER_VERSION} not found in the apt repo (available: $(apt-cache madison docker-ce | awk '{print $3}' | tr '\n' ' '))" >&2; exit 1; }; \
    composever="$(apt-cache madison docker-compose-plugin | awk -v v="${DOCKER_COMPOSE_VERSION}-" 'index($3, v)==1 {print $3; exit}')"; \
    buildxver="$(apt-cache madison docker-buildx-plugin  | awk -v v="${DOCKER_BUILDX_VERSION}-"  'index($3, v)==1 {print $3; exit}')"; \
    { test -n "$composever"; } || { echo "docker-compose-plugin ${DOCKER_COMPOSE_VERSION} not found in the apt repo (available: $(apt-cache madison docker-compose-plugin | awk '{print $3}' | tr '\n' ' '))" >&2; exit 1; }; \
    { test -n "$buildxver"; } || { echo "docker-buildx-plugin ${DOCKER_BUILDX_VERSION} not found in the apt repo (available: $(apt-cache madison docker-buildx-plugin | awk '{print $3}' | tr '\n' ' '))" >&2; exit 1; }; \
    apt-get install -y --no-install-recommends "docker-ce=$ver" "docker-ce-cli=$cliver" containerd.io \
        "docker-compose-plugin=$composever" "docker-buildx-plugin=$buildxver"; \
    rm -rf /var/lib/apt/lists/*; \
    ln -sf /usr/libexec/docker/docker-init /usr/local/bin/docker-init; \
    dockerd --version; containerd --version; runc --version; docker-init --version; \
    docker compose version; docker buildx version
COPY --from=docker:27-dind \
    /usr/local/bin/dockerd-entrypoint.sh /usr/local/bin/docker-entrypoint.sh \
    /usr/local/bin/dind /usr/local/bin/modprobe \
    /usr/local/bin/

# Declare /var/lib/docker a volume, exactly as the docker:dind image does: an
# unnamed volume is a real host filesystem, so the nested dockerd's overlayfs
# snapshotter works — without it /var/lib/docker sits on the outer container's
# overlayfs and overlay-on-overlay mounts fail ("invalid argument") on the first
# image build. (The standalone containerd for the containerd target dodges this
# separately via start_incus/start_containerd's native-snapshotter fallback.)
VOLUME /var/lib/docker

# incus (+client) from the official Zabbly stable apt repo — a current,
# OCI-capable incus (>= 6.3), unlike Alpine's 6.0 LTS. Started on demand by
# entrypoint.sh's start_incus (which still version-gates as a safety net).
RUN set -eux; \
    install -m0755 -d /etc/apt/keyrings; \
    curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc; \
    chmod a+r /etc/apt/keyrings/zabbly.asc; \
    . /etc/os-release; \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/zabbly.asc] https://pkgs.zabbly.com/incus/stable ${VERSION_CODENAME} main" > /etc/apt/sources.list.d/zabbly-incus.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends incus incus-client; \
    rm -rf /var/lib/apt/lists/*; \
    printf '#!/bin/sh\nexec /opt/incus/lib/systemd/incusd "$@"\n' > /usr/local/bin/incusd; \
    chmod 0755 /usr/local/bin/incusd; \
    incus --version; \
    incusd --version

# Let Incus's skopeo pull cornus's OWN registry, which the E2E harness serves over
# plain HTTP on a loopback address with a dynamic port (127.0.0.1:<port>). A
# host-only insecure entry matches that host on ANY port (verified: skopeo then
# dials http://), so incus pulls the built image instead of failing with "server
# gave HTTP response to HTTPS client". Public registries (docker.io, ...) keep
# their default TLS. incus invokes skopeo, which reads registries.conf.d.
RUN mkdir -p /etc/containers/registries.conf.d \
 && printf '[[registry]]\nlocation = "127.0.0.1"\ninsecure = true\n\n[[registry]]\nlocation = "localhost"\ninsecure = true\n' \
      > /etc/containers/registries.conf.d/cornus-loopback-insecure.conf

# The official devcontainer CLI, pinned for reproducible scenario behavior.
RUN npm install -g @devcontainers/cli@${DEVCONTAINERS_CLI_VERSION} \
 && devcontainer --version

# kind + kubectl static binaries for the container's architecture.
RUN set -eux; \
    arch="$(uname -m)"; \
    case "$arch" in \
        x86_64)  a=amd64 ;; \
        aarch64) a=arm64 ;; \
        *) echo "unsupported arch $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /usr/local/bin/kind    "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-${a}"; \
    curl -fsSL -o /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${a}/kubectl"; \
    chmod 0755 /usr/local/bin/kind /usr/local/bin/kubectl; \
    kind --version; kubectl version --client=true

# Stage the Multus prerequisites into the image so an E2E_MULTUS run needs no
# network: (1) the CNI reference plugins the kind node lacks, `docker cp`-ed onto
# the node by entrypoint.sh; (2) the Multus image as a kind-loadable archive
# (pulled with crane at build time), `kind load`-ed on demand; (3) the pinned,
# vendored DaemonSet manifest.
# The extraction also includes portmap/host-local/loopback: together with bridge
# they are what the containerd deploy backend requires, so /opt/cornus/cni doubles
# as CORNUS_CNI_BIN_DIR for the containerd target (exported by entrypoint.sh).
RUN set -eux; \
    arch="$(uname -m)"; \
    case "$arch" in \
        x86_64)  a=amd64; crane_a=x86_64 ;; \
        aarch64) a=arm64; crane_a=arm64 ;; \
        *) echo "unsupported arch $arch" >&2; exit 1 ;; \
    esac; \
    mkdir -p /opt/cornus/cni; \
    curl -fsSL "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-${a}-${CNI_PLUGINS_VERSION}.tgz" \
        | tar xz -C /opt/cornus/cni ./bridge ./macvlan ./ipvlan ./portmap ./host-local ./static ./loopback; \
    curl -fsSL "https://github.com/google/go-containerregistry/releases/download/${CRANE_VERSION}/go-containerregistry_Linux_${crane_a}.tar.gz" \
        | tar xz -C /usr/local/bin crane; \
    crane pull --platform "linux/${a}" "${MULTUS_IMAGE}" /opt/cornus/multus.tar; \
    ls -la /opt/cornus /opt/cornus/cni

COPY --from=build /out/cornus     /usr/local/bin/cornus
COPY --from=build /out/cornus-e2e /usr/local/bin/cornus-e2e

# Scenarios + the app Dockerfiles they build. The harness references them by the
# repo-relative path "e2e/scenarios/...", so keep that layout under /work.
WORKDIR /work
COPY e2e/scenarios/ /work/e2e/scenarios/
COPY e2e/container/entrypoint.sh /usr/local/bin/e2e-entrypoint.sh
COPY e2e/container/appimage.Dockerfile /work/e2e/container/appimage.Dockerfile
COPY e2e/container/multus-daemonset-thick.yml /work/e2e/container/multus-daemonset-thick.yml
# The Knative install script the entrypoint's install_knative (E2E_KNATIVE=1)
# runs against the in-container kind cluster; shared with the direct
# `make e2e-kube` harness path.
COPY e2e/container/install-knative.sh /work/e2e/container/install-knative.sh
RUN chmod 0755 /usr/local/bin/e2e-entrypoint.sh

ENV E2E_TARGETS="docker"
ENTRYPOINT ["/usr/local/bin/e2e-entrypoint.sh"]
