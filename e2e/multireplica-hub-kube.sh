#!/usr/bin/env bash
# Real end-to-end validation of the multi-replica hub with the KUBERNETES-NATIVE
# store (KubeStore) -- the API server IS the registry, no Redis. Designed to run
# INSIDE the e2e dind container (which ships dockerd + kind + kubectl); the cornus
# binary (with KubeStore) is expected at /usr/local/bin/cornus.
#
# It creates a kind cluster (the store), then runs replica A and replica B, two
# `cornus serve` processes with CORNUS_HUB_STORE=kube pointed at that cluster
# (distinct CORNUS_REPLICA_ID / CORNUS_HUB_FORWARD_URL). A spoke REGISTERS
# "greeter" via A (delivery -> A owns it); a spoke REACHES it via B. Dialing B's
# reach listener must forward B -> A -> the hosting spoke -> a local echo and
# round-trip -- proving cross-replica delivery forwarding with the API server as the
# shared registry and native Leases for liveness.
set -euo pipefail

CORNUS="${CORNUS:-/usr/local/bin/cornus}"
CLUSTER="mr-kube"
# NOT under /tmp: dockerd startup remounts /tmp as tmpfs in the dind image, wiping
# anything created before it. Use HOME and create it after dockerd/kind are up.
WORK="${HOME:-/root}/mr-work"
PIDS=()
cleanup() {
    for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
    rm -rf "$WORK"
}
trap cleanup EXIT

echo "== start dockerd =="
if ! docker version >/dev/null 2>&1; then
    dockerd-entrypoint.sh dockerd >/var/log/dockerd.log 2>&1 &
    for _ in $(seq 1 60); do docker version >/dev/null 2>&1 && break; sleep 1; done
fi

echo "== create kind cluster (the store) =="
# kind create cluster writes the default ~/.kube/config context; use it directly.
kind create cluster --name "$CLUSTER"
export KUBECONFIG="${HOME:-/root}/.kube/config"
kubectl cluster-info >/dev/null 2>&1 && echo "  kube API reachable"

rm -rf "$WORK"; mkdir -p "$WORK"   # after dockerd/kind so it is not wiped

echo "== echo target =="
# A tiny static TCP echo binary (mounted at /echo; the e2e image has no
# socat/python3). Falls back to socat if the binary is absent.
if [ -x "${ECHO_BIN:-/echo}" ]; then
    "${ECHO_BIN:-/echo}" 127.0.0.1:9000 & PIDS+=($!)
elif command -v socat >/dev/null 2>&1; then
    socat TCP-LISTEN:9000,fork,reuseaddr EXEC:cat >/dev/null 2>&1 & PIDS+=($!)
else
    echo "no echo tool (mount a static echo at /echo, or install socat)"; exit 2
fi

echo "== two serve replicas, KubeStore over the kind API server =="
export CORNUS_HUB_STORE=kube CORNUS_K8S_NAMESPACE=default
CORNUS_REPLICA_ID=repA CORNUS_HUB_FORWARD_URL=ws://127.0.0.1:5001 \
    "$CORNUS" serve --addr 127.0.0.1:5001 --storage mem:// >"$WORK/A.log" 2>&1 & PIDS+=($!)
CORNUS_REPLICA_ID=repB CORNUS_HUB_FORWARD_URL=ws://127.0.0.1:5002 \
    "$CORNUS" serve --addr 127.0.0.1:5002 --storage mem:// >"$WORK/B.log" 2>&1 & PIDS+=($!)
# Give both time to self-install the CRD, start informers, and register the Lease.
for _ in $(seq 1 30); do
    kubectl get crd hubendpoints.cornus.dev >/dev/null 2>&1 && break; sleep 1
done
kubectl get crd hubendpoints.cornus.dev >/dev/null 2>&1 && echo "  CRD hubendpoints.cornus.dev installed"
sleep 3

echo "== spokes: register greeter via A (delivery), reach via B =="
"$CORNUS" hub --server ws://127.0.0.1:5001 --register greeter=127.0.0.1:9000 >"$WORK/s1.log" 2>&1 & PIDS+=($!)
"$CORNUS" hub --server ws://127.0.0.1:5002 --reach greeter=127.0.0.1:9500 >"$WORK/s2.log" 2>&1 & PIDS+=($!)

echo "== HubEndpoint CRs + Leases in the cluster =="
for _ in $(seq 1 15); do
    kubectl get hubendpoints -A --no-headers 2>/dev/null | grep -q . && break; sleep 1
done
kubectl get hubendpoints -A 2>/dev/null || true
kubectl get leases -A 2>/dev/null | grep -i cornus-hub || true

echo "== cross-replica reach (dial B:9500 -> B -> forward to A -> echo) =="
got=""
for _ in $(seq 1 25); do
    got="$(printf 'PING-KUBESTORE\n' | nc -w2 127.0.0.1 9500 2>/dev/null || true)"
    [ -n "$got" ] && break
    sleep 1
done

if [ "$got" = "PING-KUBESTORE" ]; then
    echo "PASS: cross-replica forwarding with the Kubernetes API server as the hub registry works"
    exit 0
fi
echo "FAIL: reach did not round-trip (got: [$got])"
echo "--- A.log ---"; tail -20 "$WORK/A.log" || true
echo "--- B.log ---"; tail -20 "$WORK/B.log" || true
exit 1
