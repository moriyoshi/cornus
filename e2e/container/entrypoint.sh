#!/usr/bin/env bash
#
# Entrypoint for the all-in-one cornus E2E runner image.
#
# Starts an in-container dockerd (Docker-in-Docker), then runs the Starlark E2E
# harness for each requested target. Every scenario runs through the harness's
# own preflight first, so a missing tool or capability fails fast and legibly.
#
# Configuration (env):
#   E2E_TARGETS   space-separated: any of "docker", "podman", "podman-rootless",
#                 "containerd", "bare", "incus", "kube", "local"
#                 (default "docker")
#   E2E_SCENARIOS explicit scenario glob/paths (default: all e2e/scenarios/*.star;
#                 the containerd target defaults to its backend-agnostic subset)
#   E2E_STORAGE   registry storage backend for serve() (default "mem://")
#   KEEP_CLUSTER  "1" to keep the kind cluster on exit (default: delete)
#   E2E_STRICT    "1" to turn every "this target cannot run here" SELF-SKIP into a
#                 hard failure (default 0 = self-skip, the behaviour a full-suite
#                 run wants). Set it on a DEDICATED CI leg whose entire purpose is
#                 one target: there, a daemon that is missing or too old must be a
#                 RED run, not a green one that silently exercised nothing. It
#                 covers the incus OCI-version gate below and the bare target's
#                 companion agent image (whose absence self-skips two scenarios).
#   E2E_PREFLIGHT_ONLY
#                 "1" to do the full per-target setup (start/initialize the daemon)
#                 and then run the harness's own `--preflight` probe INSTEAD of the
#                 scenarios, exiting non-zero when a required capability is missing.
#                 The cheap up-front "is this leg's daemon actually here?" gate;
#                 pair it with E2E_STRICT=1.
#
# Any extra CLI args are appended to every `cornus-e2e` invocation.
set -euo pipefail

E2E_TARGETS="${E2E_TARGETS:-docker}"
E2E_STORAGE="${E2E_STORAGE:-mem://}"
E2E_STRICT="${E2E_STRICT:-0}"
E2E_PREFLIGHT_ONLY="${E2E_PREFLIGHT_ONLY:-0}"
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
PODMAN_ROOTLESS_SCENARIOS=(
    e2e/scenarios/deploy.star
    e2e/scenarios/lifecycle.star
    e2e/scenarios/exec.star
    e2e/scenarios/compose.star
    e2e/scenarios/compose-dns-resolution.star
    e2e/scenarios/deploy-portforward-rootless-podman.star
    e2e/scenarios/deploy-mounts-local-podman-rootless.star
    e2e/scenarios/deploy-mounts-idmap-podman-rootless.star
    e2e/scenarios/credentials-env-host.star
    e2e/scenarios/credentials-endpoint-host.star
)

PODMAN_SCENARIOS=(
    e2e/scenarios/mcp-stdio-protocol.star
    e2e/scenarios/mcp-stdio-tools.star
    e2e/scenarios/deploy.star
    e2e/scenarios/deploy-stats.star
    e2e/scenarios/lifecycle.star
    e2e/scenarios/lifecycle-restart.star
    e2e/scenarios/exec.star
    e2e/scenarios/compose.star
    e2e/scenarios/compose-down-volumes.star
    e2e/scenarios/compose-exec.star
    e2e/scenarios/compose-dns-resolution.star
    e2e/scenarios/deploy-portforward-rootless-podman.star
    e2e/scenarios/server-in-container-podman.star
    e2e/scenarios/credentials-env-host.star
    e2e/scenarios/credentials-endpoint-host.star
)

CONTAINERD_SCENARIOS=(
    e2e/scenarios/mcp-stdio-protocol.star
    e2e/scenarios/mcp-stdio-tools.star
    e2e/scenarios/deploy.star
    e2e/scenarios/deploy-stats.star
    e2e/scenarios/lifecycle.star
    e2e/scenarios/lifecycle-restart.star
    e2e/scenarios/deploy-server-restart.star
    e2e/scenarios/exec.star
    e2e/scenarios/compose.star
    e2e/scenarios/compose-down-volumes.star
    e2e/scenarios/registry-host-native-containerd.star
    e2e/scenarios/compose-exec.star
    e2e/scenarios/server-in-container-containerd.star
    e2e/scenarios/credentials-env-host.star
    e2e/scenarios/credentials-endpoint-host.star
    e2e/scenarios/compose-dependson.star
    e2e/scenarios/health-restart-rearm.star
    e2e/scenarios/health-unhealthy.star
)

# The bare (daemonless OCI-runtime) target runs the subset the backend currently
# implements (keep in sync with the Makefile's SCENARIOS_BARE) unless E2E_SCENARIOS
# overrides. Through M5 + M4: single-container deploy + lifecycle + restart monitor
# (in-process supervisor) + CNI networking / published ports / hosts-file + server
# DNS / volumes + exec / port-forward — the same backend-agnostic subset the
# containerd target runs.
BARE_SCENARIOS=(
    e2e/scenarios/mcp-stdio-protocol.star
    e2e/scenarios/mcp-stdio-tools.star
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
    e2e/scenarios/compose-down-volumes.star
    e2e/scenarios/exec.star
    e2e/scenarios/compose-exec.star
    e2e/scenarios/credentials-env-host.star
    e2e/scenarios/credentials-endpoint-host.star
    e2e/scenarios/compose-dependson.star
    e2e/scenarios/health-restart-rearm.star
    e2e/scenarios/health-unhealthy.star
)

# The incus target runs the subset the backend implements (keep in sync with the
# Makefile's SCENARIOS_INCUS) unless E2E_SCENARIOS overrides: single-container
# deploy + published ports + lifecycle + stats + exec + port-forward + compose.
INCUS_SCENARIOS=(
    e2e/scenarios/mcp-stdio-protocol.star
    e2e/scenarios/mcp-stdio-tools.star
    e2e/scenarios/deploy.star
    e2e/scenarios/deploy-stats.star
    e2e/scenarios/lifecycle.star
    e2e/scenarios/deploy-server-restart.star
    e2e/scenarios/exec.star
    e2e/scenarios/deploy-portforward.star
    e2e/scenarios/compose.star
    e2e/scenarios/compose-exec.star
    e2e/scenarios/server-in-container-incus.star
    e2e/scenarios/credentials-env-host.star
    e2e/scenarios/credentials-endpoint-host.star
    e2e/scenarios/compose-dependson.star
    e2e/scenarios/health-restart-rearm.star
    e2e/scenarios/deploy-fsop-incus.star
    e2e/scenarios/health-unhealthy.star
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
# prepare_cgroup_nesting moves this container's own processes out of the cgroup v2
# root so controllers can be delegated to children.
#
# The kernel forbids a cgroup from having both member processes and controllers
# enabled for its children, and inside a container /sys/fs/cgroup IS such a cgroup:
# it holds the container's processes. runc trips that the moment a container asks for
# any resource controller — "cannot enter cgroupv2 \"/sys/fs/cgroup/default\" with
# domain controllers -- it is in an invalid state" — so `ctr run` fails outright here,
# while cornus's own deploys survive only because they request no controllers (which
# is also why deploy-stats.star reports mem_usage=0 on this leg).
#
# dind's own entrypoint does this for the docker leg; nothing did it for
# containerd-only, which silently reduced server-in-container-containerd.star to a
# self-skip. Best-effort throughout: on cgroup v1, an already-empty root, or any
# failure, the leg behaves exactly as it did before.
prepare_cgroup_nesting() {
    [ -f /sys/fs/cgroup/cgroup.controllers ] || return 0
    # NOT `[ -s ... ]`: cgroupfs pseudo-files report size 0 even when populated, so a
    # size test silently skips the whole thing (it did, and the scenario stayed a
    # self-skip while this function looked like it had run). Read the content.
    [ -n "$(cat /sys/fs/cgroup/cgroup.procs 2>/dev/null)" ] || return 0
    mkdir -p /sys/fs/cgroup/init 2>/dev/null || return 0
    xargs -rn1 < /sys/fs/cgroup/cgroup.procs > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null || true
    if [ -n "$(cat /sys/fs/cgroup/cgroup.procs 2>/dev/null)" ]; then
        log "cgroup root still holds processes; containers requesting resource controllers may fail here"
        return 0
    fi
    # Explicitly delegating the controllers here (writing +cpu etc. to
    # subtree_control) was tried and REMOVED: emptying the root is sufficient, because
    # runc then enables what it needs itself — measured, the containerd leg went from
    # mem_usage=0 to real accounting on the move alone. The explicit version could not
    # be confirmed against a green leg (Docker Hub throttling intervened), and an
    # unverified refinement on a passing path is not worth its risk.
    log "moved this container's processes to /sys/fs/cgroup/init so cgroup controllers can be delegated"
}

start_podman() {
    if ! command -v podman >/dev/null 2>&1; then
        echo "podman binary not present in this image; cannot run the podman target" >&2
        return 1
    fi
    prepare_cgroup_nesting
    # The socket is NOT discovered — the backend refuses to guess which daemon it
    # drives, so the harness names one explicitly and everything downstream (the
    # preflight probe, the server) uses that same endpoint.
    export CORNUS_PODMAN_SOCKET=/run/podman/podman.sock
    if curl -s --unix-socket "$CORNUS_PODMAN_SOCKET" http://d/v5.0.0/libpod/info >/dev/null 2>&1; then
        log "using existing podman API service"
        return 0
    fi
    log "starting in-container podman API service"
    mkdir -p "$(dirname "$CORNUS_PODMAN_SOCKET")"
    # --time=0 is right HERE (unlike the server's own supervised child): this
    # process lives exactly as long as the container, and an idle timeout would
    # kill it between two slow scenarios.
    podman system service --time=0 "unix://$CORNUS_PODMAN_SOCKET" \
        >/var/log/podman-service.log 2>&1 &
    for _ in $(seq 1 30); do
        if curl -s --unix-socket "$CORNUS_PODMAN_SOCKET" http://d/v5.0.0/libpod/info >/dev/null 2>&1; then
            log "podman API service is up"
            return 0
        fi
        sleep 1
    done
    echo "podman API service did not come up; see /var/log/podman-service.log" >&2
    tail -20 /var/log/podman-service.log >&2 || true
    return 1
}

start_podman_rootless() {
    if ! command -v podman >/dev/null 2>&1; then
        echo "podman binary not present in this image; cannot run the podman-rootless target" >&2
        return 1
    fi
    if ! id rootless >/dev/null 2>&1; then
        echo "no 'rootless' user in this image; rebuild it (make e2e-image)" >&2
        return 1
    fi
    prepare_cgroup_nesting
    # Rootless podman needs subuid/subgid ranges to map a user namespace. Without
    # them every container create fails with "no subuid ranges found" — late, at
    # deploy time, rather than here.
    if ! grep -q '^rootless:' /etc/subuid 2>/dev/null; then
        echo "no subuid range for 'rootless'; rootless podman cannot map a user namespace" >&2
        return 1
    fi
    export CORNUS_PODMAN_SOCKET=/run/user/1001/podman/podman.sock
    mkdir -p "$(dirname "$CORNUS_PODMAN_SOCKET")"
    chown -R rootless:rootless /run/user/1001
    if curl -s --unix-socket "$CORNUS_PODMAN_SOCKET" http://d/v5.0.0/libpod/info >/dev/null 2>&1; then
        log "using existing rootless podman API service"
        return 0
    fi
    # Client-local 9P mounts need SHARED propagation, and this is the only leg
    # where that is not automatic. The cornus server (root) makes the kernel 9P
    # mount in THIS mount namespace; rootless podman runs its containers in a
    # separate one held by its pause process. A mount namespace copy joins the
    # peer group of the mounts it was copied from only if those were shared — so
    # this must happen BEFORE podman starts, and cannot be repaired afterwards.
    #
    # Measured with it absent: the mount is `private`, the pause process's
    # mountinfo has no 9p line at all, and the deploy SUCCEEDS with /data silently
    # empty. Measured with it present: the container's /data is the 9P mount
    # itself (same device, same backing socket) and reads the client's bytes.
    #
    # The rootful legs need nothing: their daemon shares this namespace, so there
    # is no propagation step. This is the in-container equivalent of the `:rshared`
    # hint that hostcheck's mount-propagation check gives for a bind-mounted data
    # dir (pkg/hostcheck/hostcheck.go).
    if ! mount --make-rshared /; then
        echo "could not set shared propagation on /; client-local mounts will come up EMPTY" >&2
        return 1
    fi
    log "starting in-container ROOTLESS podman API service"
    # --time=0 for the same reason the rootful one uses it: this process lives as
    # long as the container, and an idle timeout would kill it between two slow
    # scenarios.
    setpriv --reuid=1001 --regid=1001 --init-groups \
        env XDG_RUNTIME_DIR=/run/user/1001 HOME=/home/rootless \
        podman system service --time=0 "unix://$CORNUS_PODMAN_SOCKET" \
        >/var/log/podman-rootless.log 2>&1 &
    for _ in $(seq 1 30); do
        if curl -s --unix-socket "$CORNUS_PODMAN_SOCKET" http://d/v5.0.0/libpod/info >/dev/null 2>&1; then
            log "rootless podman API service is up"
            return 0
        fi
        sleep 1
    done
    echo "rootless podman API service did not come up; see /var/log/podman-rootless.log" >&2
    tail -20 /var/log/podman-rootless.log >&2 || true
    return 1
}

start_containerd() {
    if ! command -v containerd >/dev/null 2>&1; then
        echo "containerd binary not present in this image; cannot run the containerd target" >&2
        return 1
    fi
    prepare_cgroup_nesting
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

# start_incus launches incusd (from the apk `incus` package) and minimally
# initializes it (a `dir` storage pool + a managed NAT bridge, so instances get
# routable IPs that the incus backend's ForwardPort can dial) for the incus
# target. Incus's OCI mode also needs skopeo + umoci on PATH (staged into the
# image). incusd nested in this privileged dind container relies on the outer
# --privileged run for cgroup/dev access. Robust to a base image without incusd:
# it reports and fails the target cleanly instead of masquerading as green.
start_incus() {
    if ! command -v incusd >/dev/null 2>&1; then
        echo "incusd not present in this image; cannot run the incus target" >&2
        return 1
    fi
    for b in skopeo umoci; do
        if ! command -v "$b" >/dev/null 2>&1; then
            echo "$b not present in this image; Incus needs it to run OCI images" >&2
            return 1
        fi
    done
    # OCI application-container support landed in Incus 6.3; older daemons reject
    # the OCI image source with "Unsupported protocol: oci". Skip the target
    # (return 2 = self-skip, not a failure) rather than run every scenario into
    # that error. Alpine stable ships the 6.0 LTS line, so this skip is expected
    # there; a 6.3+ image (see the Dockerfile VERSION CAVEAT) runs the scenarios.
    local ver major minor
    ver="$(incusd --version 2>/dev/null | head -1)"
    major="${ver%%.*}"; minor="${ver#*.}"; minor="${minor%%.*}"
    if [ -z "$major" ] || [ "$major" -lt 6 ] || { [ "$major" -eq 6 ] && [ "${minor:-0}" -lt 3 ]; }; then
        log "incus ${ver:-unknown} lacks OCI application-container support (needs >= 6.3); skipping the incus target"
        return 2
    fi
    if incus info >/dev/null 2>&1; then
        log "using existing incus daemon"
        return 0
    fi
    log "starting in-container incusd"
    incusd >/var/log/incusd.log 2>&1 &
    local up=0
    for _ in $(seq 1 60); do
        if incus info >/dev/null 2>&1; then up=1; break; fi
        sleep 1
    done
    if [ "$up" != 1 ]; then
        echo "incusd did not become ready; last log lines:" >&2
        tail -n 40 /var/log/incusd.log >&2 || true
        return 1
    fi
    # Initialize idempotently (skip when the default storage pool already exists).
    # A preseed rather than `--minimal` because --minimal's managed bridge enables
    # NAT, and clearing/applying the nftables NAT rules fails inside this nested
    # dind netns ("Failed clearing nftables rules ... EOF"). The scenarios do not
    # need instance OUTBOUND NAT — incusd pulls the OCI image itself, and the app
    # only needs an IP the backend's ForwardPort can dial — so the bridge is
    # created with ipv4.nat/ipv4.firewall disabled, avoiding nftables entirely.
    # dnsmasq still runs (ipv4.address set), so instances get a DHCP lease.
    if ! incus storage show default >/dev/null 2>&1; then
        if ! incus admin init --preseed >/var/log/incus-init.log 2>&1 <<'PRESEED'; then
storage_pools:
  - name: default
    driver: dir
networks:
  - name: incusbr0
    type: bridge
    config:
      ipv4.address: 10.0.3.1/24
      ipv4.nat: "false"
      ipv4.firewall: "false"
      ipv6.address: none
profiles:
  - name: default
    devices:
      root:
        type: disk
        path: /
        pool: default
      eth0:
        type: nic
        network: incusbr0
        name: eth0
PRESEED
            echo "incus admin init (preseed) failed; last log lines:" >&2
            tail -n 40 /var/log/incus-init.log >&2 || true
            return 1
        fi
    fi
    echo "incus is up"
    return 0
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
    if ! command -v crane >/dev/null 2>&1; then
        if [ "$E2E_STRICT" = 1 ]; then
            echo "crane absent; the bare companion scenarios would self-skip (E2E_STRICT=1)" >&2
            return 1
        fi
        log "crane absent; bare companion scenarios will self-skip"
        return 0
    fi
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
        rm -rf "$tmp"
        if [ "$E2E_STRICT" = 1 ]; then
            echo "bare agent image build failed (E2E_STRICT=1: the companion scenarios must not self-skip); log:" >&2
            tail -n 40 /tmp/bare-agent-build.log >&2 || true
            return 1
        fi
        log "bare agent image build failed (companion scenarios self-skip); see /tmp/bare-agent-build.log"
        return 0
    fi
    rm -rf "$tmp"
}

setup_bare() {
    if [ -z "${CORNUS_BARE_RUNTIME:-}" ] && ! command -v runc >/dev/null 2>&1; then
        echo "no OCI runtime (runc) present in this image; cannot run the bare target" >&2
        return 1
    fi
    export CORNUS_CNI_BIN_DIR=/opt/cornus/cni
    # Propagate explicitly: setup_bare is called from an `if !` condition, which
    # disables `set -e` for its whole body, so a failing call would otherwise be
    # ignored (and under E2E_STRICT=1 this one is meant to fail the target).
    prepare_bare_agent_image || return 1
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
    if [ "${E2E_INGRESS_NGINX:-0}" = 1 ]; then
        install_ingress_nginx
    fi
    if [ "${E2E_METRICS_SERVER:-0}" = 1 ]; then
        install_metrics_server
    fi
}

# install_metrics_server installs metrics-server into the kind cluster so the
# kubernetes backend has a metric source at all: SampleMetrics reads the
# metrics.k8s.io aggregated API, which does not exist otherwise. Gated by
# E2E_METRICS_SERVER=1. observability-metrics.star probes for it and keeps only
# its backend-independent assertions when it is absent — but it HARD-FAILS if
# metrics-server is present and the readings still do not arrive, so a leg that
# sets this flag cannot go green having exercised nothing. Delegates to the
# shared install-metrics-server.sh (also used by `make e2e-kube
# E2E_METRICS_SERVER=1`), whose release manifest is vendored + checksummed.
install_metrics_server() {
    log "installing metrics-server into the kind cluster"
    bash /work/e2e/container/install-metrics-server.sh
}

# install_ingress_nginx installs a real ingress controller into the kind cluster
# so the ingress scenarios can exercise cornus dialling an actual controller
# rather than the server-side mux it falls back to. Gated by
# E2E_INGRESS_NGINX=1; the controller-specific scenario self-skips without it,
# and the others simply cover the fallback instead. Delegates to the shared
# install-ingress-nginx.sh (also used by `make e2e-kube E2E_INGRESS_NGINX=1`).
install_ingress_nginx() {
    log "installing ingress-nginx into the kind cluster"
    bash /work/e2e/container/install-ingress-nginx.sh
}

# install_knative installs Knative Serving plus the Kourier networking layer into
# the kind cluster so the deploy-knative scenario can round-trip a real
# serving.knative.dev Service. Gated by E2E_KNATIVE=1; the scenario self-skips
# without it. Delegates to the shared install-knative.sh (also used by the direct
# `make e2e-kube E2E_KNATIVE=1` harness path) so there is one implementation.
# Like install_multus, the release manifests are VENDORED into the image
# (/work/e2e/container/knative, pinned to the version recorded in the script and
# checksum-verified before use), so the install itself performs no runtime fetch.
# The cluster still PULLS the (digest-pinned) Knative images, as it does for the
# other kube scenarios' images.
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

# run_harness runs the harness and, when it fails, says so in the operator's own
# terms if the cause was the Docker Hub anonymous pull quota rather than cornus.
#
# Anonymous pulls are capped at 100 per hour PER SOURCE ADDRESS, and running
# several legs back to back blows through it. The resulting failures do not look
# like quota failures — they look like a regression in whatever changed last.
# Measured 2026-08-06 while validating the trixie base bump: `bare` reported 2
# failures that were 429s on alpine:3.20 (14 scenarios in the SAME run had
# already pulled it), `incus` failed 5 scenarios on `toomanyrequests` through
# skopeo, and `kube` never ran a scenario at all — it died building the sidecar
# image on a HEAD for debian:trixie-slim. A green docker leg beside a red incus
# leg reads as "the change broke incus", and the afternoon goes to the wrong
# question.
#
# This fixes NOTHING. It converts a misattributed failure into a named one, which
# is the whole of its value; the structural fixes (a pull-through registry mirror,
# or pre-seeding the fixture images into this image at build time) are tracked in
# TODO.md and both cost infrastructure decisions this does not.
#
# Note the trap it warns about: the HOST and the containers are different
# rate-limit sources, and the difference is total rather than marginal — measured
# the same day, the host egressed IPv6 and reported 100 remaining while a
# container on the same machine egressed IPv4 and reported 0. Checking from the
# host therefore says "plenty of budget" at the exact moment every leg is being
# refused, so a retry launched on that reading fails identically.
#
# stderr is folded into stdout so one capture holds both. The harness's streams
# are already interleaved in a CI log, and `tee` keeps the output live rather
# than withholding it until the leg ends.
run_harness() {
    local target="$1"; shift
    local log_file rc_run=0
    log_file="$(mktemp)"
    "$E2E_BIN" "$@" 2>&1 | tee "$log_file" || rc_run=$?
    if [ "$rc_run" -ne 0 ] && grep -qiE 'toomanyrequests|429 Too Many Requests|pull rate limit|x-envoy-ratelimited' "$log_file"; then
        cat >&2 <<EOF

*** target '$target' hit the Docker Hub anonymous pull quota (HTTP 429). ***
    This is NOT a cornus defect, and the scenario failures above are very likely
    not real. Anonymous pulls are limited to 100 per hour per source address, and
    the limit refills on a rolling 1-hour window — space the legs out and re-run.
    Do NOT check the remaining quota from the host: the host and the containers
    egress from different addresses, so the host can report a full budget while
    every leg here is being refused.
EOF
    fi
    rm -f "$log_file"
    return "$rc_run"
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
        podman)
            if ! start_podman; then
                echo "target 'podman' had failures" >&2
                rc=1
                continue
            fi
            if [ -z "${E2E_SCENARIOS:-}" ]; then
                scenarios=("${PODMAN_SCENARIOS[@]}")
            fi
            ;;
        podman-rootless)
            if ! start_podman_rootless; then
                echo "target 'podman-rootless' had failures" >&2
                rc=1
                continue
            fi
            if [ -z "${E2E_SCENARIOS:-}" ]; then
                scenarios=("${PODMAN_ROOTLESS_SCENARIOS[@]}")
            fi
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
        incus)
            # `|| s=$?` keeps `set -e` from aborting on start_incus's non-zero
            # return (2 = self-skip when the daemon lacks OCI support).
            s=0; start_incus || s=$?
            if [ "$s" -eq 2 ] && [ "$E2E_STRICT" != 1 ]; then
                continue   # self-skip (daemon lacks OCI support); not a failure
            elif [ "$s" -ne 0 ]; then
                # E2E_STRICT=1 lands here for s=2 too: on a leg dedicated to incus,
                # "the daemon is too old to run any of this" is the exact failure the
                # leg exists to report, not a reason to exit green having run nothing.
                echo "target 'incus' had failures" >&2
                rc=1
                continue
            fi
            if [ -z "${E2E_SCENARIOS:-}" ]; then
                scenarios=("${INCUS_SCENARIOS[@]}")
            fi
            ;;
    esac
    if [ "$E2E_PREFLIGHT_ONLY" = 1 ]; then
        # Gate only: the daemon/runtime is up (the per-target setup above ran), so
        # probe the capabilities this target + these scenarios need and exit. The
        # harness's preflight already fails hard on a missing capability, which is
        # precisely the "the daemon this leg exists for is not here" signal.
        log "preflight only ($target): ${scenarios[*]}"
        if ! run_harness "$target" "${common[@]}" --preflight "$@" "${scenarios[@]}"; then
            echo "target '$target' preflight failed" >&2
            rc=1
        fi
        continue
    fi
    log "running E2E ($target): ${scenarios[*]}"
    if ! run_harness "$target" "${common[@]}" "$@" "${scenarios[@]}"; then
        echo "target '$target' had failures" >&2
        rc=1
    fi
    if [ "$target" = kube ]; then
        cleanup_kube
        trap - EXIT
    fi
done

exit "$rc"
