#!/usr/bin/env bash
#
# Entrypoint for the all-in-one cornus E2E runner image.
#
# Starts an in-container dockerd (Docker-in-Docker), then runs the Starlark E2E
# harness for each requested target. Every scenario runs through the harness's
# own preflight first, so a missing tool or capability fails fast and legibly.
#
# Configuration (env):
#   E2E_TARGETS   space-separated: any of "docker", "containerd", "bare", "kube", "local"
#                 (default "docker")
#   E2E_SCENARIOS explicit scenario glob/paths (default: all e2e/scenarios/*.star;
#                 the containerd target defaults to its backend-agnostic subset)
#   E2E_STORAGE   registry storage backend for serve() (default "mem://")
#   KEEP_CLUSTER  "1" to keep the kind cluster on exit (default: delete)
#
# Any extra CLI args are appended to every `cornus-e2e` invocation.
set -euo pipefail

E2E_TARGETS="${E2E_TARGETS:-docker}"
E2E_STORAGE="${E2E_STORAGE:-mem://}"
CLUSTER="${E2E_CLUSTER:-cornus-e2e}"
KEEP_CLUSTER="${KEEP_CLUSTER:-0}"
CORNUS_BIN=/usr/local/bin/cornus
E2E_BIN=/usr/local/bin/cornus-e2e

cd /work

# Default scenario set: every .star under e2e/scenarios (kube-only ones self-skip
# on the docker/local targets). Callers can override with E2E_SCENARIOS.
if [ -n "${E2E_SCENARIOS:-}" ]; then
    # shellcheck disable=SC2206
    SCENARIOS=(${E2E_SCENARIOS})
else
    SCENARIOS=(e2e/scenarios/*.star)
fi

# The containerd target runs the backend-agnostic subset (keep in sync with the
# Makefile's SCENARIOS_CONTAINERD) unless E2E_SCENARIOS overrides: the rest of
# the suite is docker-/kube-specific or self-skips.
CONTAINERD_SCENARIOS=(
    e2e/scenarios/deploy.star
    e2e/scenarios/deploy-stats.star
    e2e/scenarios/lifecycle.star
    e2e/scenarios/lifecycle-restart.star
    e2e/scenarios/exec.star
    e2e/scenarios/compose.star
    e2e/scenarios/compose-exec.star
)

# The bare (daemonless OCI-runtime) target runs the subset the backend currently
# implements (keep in sync with the Makefile's SCENARIOS_BARE) unless E2E_SCENARIOS
# overrides. Through M5 + M4: single-container deploy + lifecycle + restart monitor
# (in-process supervisor) + CNI networking / published ports / hosts-file + server
# DNS / volumes + exec / port-forward — the same backend-agnostic subset the
# containerd target runs.
BARE_SCENARIOS=(
    e2e/scenarios/deploy.star
    e2e/scenarios/deploy-stats.star
    e2e/scenarios/lifecycle.star
    e2e/scenarios/lifecycle-restart.star
    e2e/scenarios/deploy-server-restart.star
    e2e/scenarios/deploy-reboot-survival.star
    e2e/scenarios/deploy-portforward.star
    e2e/scenarios/deploy-egress-bare.star
    e2e/scenarios/deploy-mounts-sidecar-bare.star
    e2e/scenarios/compose.star
    e2e/scenarios/compose-logs.star
    e2e/scenarios/exec.star
    e2e/scenarios/compose-exec.star
)

log() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }

need_dockerd=0
for t in $E2E_TARGETS; do
    case "$t" in docker|kube) need_dockerd=1 ;; esac
done

start_dockerd() {
    if docker info >/dev/null 2>&1; then
        log "using existing docker daemon"
        return
    fi
    log "starting in-container dockerd"
    # The dind image ships dockerd-entrypoint.sh, which prepares cgroups/storage.
    dockerd-entrypoint.sh dockerd >/var/log/dockerd.log 2>&1 &
    for _ in $(seq 1 60); do
        if docker info >/dev/null 2>&1; then
            echo "dockerd is up"
            return
        fi
        sleep 1
    done
    echo "dockerd did not become ready; last log lines:" >&2
    tail -n 40 /var/log/dockerd.log >&2 || true
    exit 1
}

# start_containerd starts the dind base image's standalone containerd on the
# stock socket for the containerd target, and points the backend at the CNI
# reference plugins staged into /opt/cornus/cni at image build time. Robust to a
# base image without the binary: it reports and fails the target cleanly.
start_containerd() {
    if ! command -v containerd >/dev/null 2>&1; then
        echo "containerd binary not present in this image; cannot run the containerd target" >&2
        return 1
    fi
    export CORNUS_CNI_BIN_DIR=/opt/cornus/cni
    # Nested-overlay guard: in this dind container /var/lib sits on the outer
    # container's overlayfs, and the kernel rejects overlay-upon-overlay, which
    # surfaces as "failed to mount rootfs component: invalid argument" at task
    # start. Point the backend at the copy-based native snapshotter instead.
    # (busybox stat reports overlayfs as UNKNOWN, so read /proc/mounts: pick the
    # longest mount point covering containerd's root and inspect its fs type.)
    ctd_fstype=$(awk -v p=/var/lib/containerd '
        $2 == "/" || $2 == p || index(p, $2 "/") == 1 {
            if (length($2) >= len) { len = length($2); t = $3 }
        }
        END { print t }' /proc/mounts)
    if [ -z "${CORNUS_CONTAINERD_SNAPSHOTTER:-}" ] \
        && { [ "$ctd_fstype" = "overlay" ] || [ "$ctd_fstype" = "overlayfs" ]; }; then
        export CORNUS_CONTAINERD_SNAPSHOTTER=native
        log "overlay-backed containerd root detected; using the native snapshotter"
    fi
    if ctr --address /run/containerd/containerd.sock version >/dev/null 2>&1; then
        log "using existing containerd daemon"
        return 0
    fi
    log "starting in-container containerd"
    containerd >/var/log/containerd.log 2>&1 &
    for _ in $(seq 1 30); do
        if ctr --address /run/containerd/containerd.sock version >/dev/null 2>&1; then
            echo "containerd is up"
            return 0
        fi
        sleep 1
    done
    echo "containerd did not become ready; last log lines:" >&2
    tail -n 40 /var/log/containerd.log >&2 || true
    return 1
}

# setup_bare prepares the daemonless bare (OCI-runtime) target: it points the
# backend at the staged CNI reference plugins and, like start_containerd, guards
# against nested overlay by selecting the copy-based native snapshotter when the
# harness data dir sits on the outer container's overlayfs. There is no daemon to
# start — the backend drives runc directly.
# prepare_bare_agent_image builds a cornus-embedding agent image and pushes it to
# an in-memory registry the bare backend can pull from, exporting CORNUS_AGENT_IMAGE
# + CORNUS_BARE_INSECURE_REGISTRIES so the companion (egress / mount-sidecar)
# scenarios pick it up (they self-skip otherwise). Unlike the docker/kube targets
# (which build into the daemon store / kind), the bare backend pulls into its OWN
# content store, so the image must live in a registry. Uses crane — NO dockerd
# needed, matching the bare target. Best-effort: a failure just leaves the companion
# scenarios self-skipping.
prepare_bare_agent_image() {
    command -v crane >/dev/null 2>&1 || { log "crane absent; bare companion scenarios will self-skip"; return 0; }
    local reg=127.0.0.1:5544
    if ! pgrep -f "crane registry serve" >/dev/null 2>&1; then
        crane registry serve --address "$reg" >/tmp/bare-agent-registry.log 2>&1 &
        for _ in $(seq 1 50); do crane catalog "$reg" --insecure >/dev/null 2>&1 && break; sleep 0.2; done
    fi
    local tmp; tmp="$(mktemp -d)"
    mkdir -p "$tmp/rootfs/usr/local/bin"
    cp "$CORNUS_BIN" "$tmp/rootfs/usr/local/bin/cornus"
    tar cf "$tmp/layer.tar" -C "$tmp/rootfs" usr
    if crane append -b alpine:3.20 -f "$tmp/layer.tar" -t "$reg/cornus-agent:base" --insecure >/tmp/bare-agent-build.log 2>&1 \
        && crane mutate "$reg/cornus-agent:base" --entrypoint cornus -t "$reg/cornus-agent:e2e" --insecure >>/tmp/bare-agent-build.log 2>&1; then
        export CORNUS_AGENT_IMAGE="$reg/cornus-agent:e2e"
        export CORNUS_BARE_INSECURE_REGISTRIES="$reg"
        log "bare agent image ready: $CORNUS_AGENT_IMAGE"
    else
        log "bare agent image build failed (companion scenarios self-skip); see /tmp/bare-agent-build.log"
    fi
    rm -rf "$tmp"
}

setup_bare() {
    if [ -z "${CORNUS_BARE_RUNTIME:-}" ] && ! command -v runc >/dev/null 2>&1; then
        echo "no OCI runtime (runc) present in this image; cannot run the bare target" >&2
        return 1
    fi
    export CORNUS_CNI_BIN_DIR=/opt/cornus/cni
    prepare_bare_agent_image
    bare_root="${TMPDIR:-/tmp}"
    bare_fstype=$(awk -v p="$bare_root" '
        $2 == "/" || $2 == p || index(p, $2 "/") == 1 {
            if (length($2) >= len) { len = length($2); t = $3 }
        }
        END { print t }' /proc/mounts)
    if [ -z "${CORNUS_BARE_SNAPSHOTTER:-}" ] \
        && { [ "$bare_fstype" = "overlay" ] || [ "$bare_fstype" = "overlayfs" ]; }; then
        export CORNUS_BARE_SNAPSHOTTER=native
        log "overlay-backed bare data dir detected; using the native snapshotter"
    fi
    return 0
}

# prepare_kube pre-creates the kind cluster and loads the cornus:e2e image the
# mount scenarios reference, then hands the cluster to the harness via --keep so
# it reuses (not recreates) it. Cleaned up on exit unless KEEP_CLUSTER=1.
prepare_kube() {
    log "creating kind cluster '$CLUSTER'"
    if ! kind get clusters | grep -qx "$CLUSTER"; then
        kind create cluster --name "$CLUSTER"
    fi
    log "building cornus:e2e app/sidecar image and loading it into kind"
    local ctx
    ctx="$(mktemp -d)"
    cp "$CORNUS_BIN" "$ctx/cornus"
    cp e2e/container/appimage.Dockerfile "$ctx/Dockerfile"
    docker build -t cornus:e2e "$ctx"
    kind load docker-image cornus:e2e --name "$CLUSTER"
    rm -rf "$ctx"

    if [ "${E2E_MULTUS:-0}" = 1 ]; then
        install_multus
    fi
    if [ "${E2E_KNATIVE:-0}" = 1 ]; then
        install_knative
    fi
}

# install_knative installs Knative Serving plus the Kourier networking layer into
# the kind cluster so the deploy-knative scenario can round-trip a real
# serving.knative.dev Service. Gated by E2E_KNATIVE=1; the scenario self-skips
# without it. Delegates to the shared install-knative.sh (also used by the direct
# `make e2e-kube E2E_KNATIVE=1` harness path) so there is one implementation.
# Unlike install_multus (fully vendored, no runtime fetch), the script applies the
# upstream release manifests from the internet, so it needs network access.
install_knative() {
    log "installing Knative Serving into the kind cluster"
    bash /work/e2e/container/install-knative.sh
}

# prepare_docker_agent_image builds the same cornus-embedding image prepare_kube
# builds for the kube caretaker sidecar, but for the plain "docker" target: the
# dockerhost backend's mount-relay caretaker companion (CORNUS_DOCKER_REMOTE,
# pkg/deploy/dockerhost/mounts.go) does not pull it itself (matching the existing
# egress companion's convention on this backend — see ApplyWithEgress), so it must
# already be present in the daemon's local image store. Exports CORNUS_AGENT_IMAGE
# so deploy-mounts-sidecar-docker.star (self-skipping otherwise) picks it up.
prepare_docker_agent_image() {
    if docker image inspect cornus:e2e >/dev/null 2>&1; then
        export CORNUS_AGENT_IMAGE=cornus:e2e
        return
    fi
    log "building cornus:e2e agent image for the docker-target sidecar mount scenario"
    local ctx
    ctx="$(mktemp -d)"
    cp "$CORNUS_BIN" "$ctx/cornus"
    cp e2e/container/appimage.Dockerfile "$ctx/Dockerfile"
    docker build -t cornus:e2e "$ctx"
    rm -rf "$ctx"
    export CORNUS_AGENT_IMAGE=cornus:e2e
}

# install_multus makes cornus's netdriver `bridge` provider work on kind, using
# ONLY assets baked into this image (no runtime fetch — see the Dockerfile). Three
# steps: (1) copy the CNI reference plugins onto every kind node — kindest/node
# ships only ptp/host-local/portmap/loopback, NOT bridge/macvlan/ipvlan (nor the
# `static` IPAM plugin cornus's pinned per-service addressing delegates to), so
# Multus's delegate would otherwise fail the CNI ADD and annotated pods hang;
# (2) load the Multus image into the cluster (no registry pull); (3) apply the
# pinned, vendored DaemonSet. Gated by E2E_MULTUS=1; the multus scenario
# self-skips without the resulting CRD.
install_multus() {
    local cnidir=/opt/cornus/cni
    local image_tar=/opt/cornus/multus.tar
    local manifest=/work/e2e/container/multus-daemonset-thick.yml

    log "staging CNI reference plugins (bridge/macvlan/ipvlan/static) onto the kind node(s)"
    for node in $(kind get nodes --name "$CLUSTER"); do
        docker cp "$cnidir/." "$node":/opt/cni/bin/
    done

    log "loading the Multus image into kind (no registry pull)"
    kind load image-archive "$image_tar" --name "$CLUSTER"

    log "applying the vendored Multus DaemonSet"
    kubectl apply -f "$manifest"
    log "waiting for the Multus DaemonSet to be ready"
    kubectl -n kube-system rollout status ds/kube-multus-ds --timeout=180s
    # Wait for the CRD to be served before scenarios query it.
    local crd_ok=0
    for _ in $(seq 1 30); do
        if kubectl get crd network-attachment-definitions.k8s.cni.cncf.io >/dev/null 2>&1; then
            crd_ok=1
            break
        fi
        sleep 2
    done
    [ "$crd_ok" = 1 ] || { echo "Multus CRD did not appear" >&2; exit 1; }
    echo "Multus CRD available"

    multus_canary
}

# multus_canary gates readiness deterministically: the DaemonSet reporting Ready
# does NOT mean Multus can yet attach SECONDARY interfaces — its NAD informer
# needs a moment to sync, and a NAD-annotated pod created in that window runs
# with the default network only (no secondary) and stays that way (it is
# Running, so nothing recreates it). So spin a canary Deployment that attaches a
# bridge NAD and wait until its pod actually has a `net1`; if a pod came up
# without it (the race), delete it so the Deployment recreates it once the
# informer has synced. This turns the race into a hard gate before scenarios run.
multus_canary() {
    local ns=cornus-multus-canary
    log "canary: verifying Multus can attach a secondary interface"
    kubectl create ns "$ns" >/dev/null 2>&1 || true
    kubectl apply -f - >/dev/null <<'EOF'
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata: {name: canary, namespace: cornus-multus-canary}
spec:
  config: '{"cniVersion":"0.3.1","name":"canary","type":"bridge","bridge":"canary0","isGateway":true,"ipam":{"type":"host-local","subnet":"10.223.0.0/24"}}'
EOF
    kubectl apply -f - >/dev/null <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata: {name: canary, namespace: cornus-multus-canary}
spec:
  replicas: 1
  selector: {matchLabels: {app: canary}}
  template:
    metadata:
      labels: {app: canary}
      annotations: {k8s.v1.cni.cncf.io/networks: canary}
    spec:
      containers:
      - {name: c, image: alpine:3.20, command: ["sleep", "3600"]}
EOF
    local ok=0 pod
    for _ in $(seq 1 40); do
        pod="$(kubectl -n "$ns" get pod -l app=canary -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
        if [ -n "$pod" ] && kubectl -n "$ns" exec "$pod" -- ip -o addr show net1 >/dev/null 2>&1; then
            ok=1
            break
        fi
        # Running-but-no-net1 == it hit the informer race; recreate it.
        if [ -n "$pod" ] && [ "$(kubectl -n "$ns" get pod "$pod" -o jsonpath='{.status.phase}' 2>/dev/null)" = Running ]; then
            kubectl -n "$ns" delete pod "$pod" --wait=false >/dev/null 2>&1 || true
        fi
        sleep 3
    done
    kubectl delete ns "$ns" --wait=false >/dev/null 2>&1 || true
    [ "$ok" = 1 ] || { echo "Multus canary never attached a secondary interface" >&2; exit 1; }
    echo "Multus ready (canary attached net1)"
}

cleanup_kube() {
    if [ "$KEEP_CLUSTER" != "1" ]; then
        log "deleting kind cluster '$CLUSTER'"
        kind delete cluster --name "$CLUSTER" || true
    fi
}

if [ "$need_dockerd" = 1 ]; then
    start_dockerd
fi

rc=0
for target in $E2E_TARGETS; do
    common=(--target "$target" --cornus "$CORNUS_BIN" --storage "$E2E_STORAGE")
    scenarios=("${SCENARIOS[@]}")
    case "$target" in
        docker)
            prepare_docker_agent_image
            ;;
        kube)
            prepare_kube
            trap cleanup_kube EXIT
            common+=(--cluster "$CLUSTER" --keep)
            ;;
        containerd)
            if ! start_containerd; then
                echo "target 'containerd' had failures" >&2
                rc=1
                continue
            fi
            if [ -z "${E2E_SCENARIOS:-}" ]; then
                scenarios=("${CONTAINERD_SCENARIOS[@]}")
            fi
            ;;
        bare)
            if ! setup_bare; then
                echo "target 'bare' had failures" >&2
                rc=1
                continue
            fi
            if [ -z "${E2E_SCENARIOS:-}" ]; then
                scenarios=("${BARE_SCENARIOS[@]}")
            fi
            ;;
    esac
    log "running E2E ($target): ${scenarios[*]}"
    if ! "$E2E_BIN" "${common[@]}" "$@" "${scenarios[@]}"; then
        echo "target '$target' had failures" >&2
        rc=1
    fi
    if [ "$target" = kube ]; then
        cleanup_kube
        trap - EXIT
    fi
done

exit "$rc"
