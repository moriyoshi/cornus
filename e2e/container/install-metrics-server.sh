#!/usr/bin/env bash
# Install metrics-server into the Kubernetes cluster the current kubectl context
# (or $KUBECONFIG) points at, so observability-metrics.star can exercise the
# kubernetes backend's REAL metric source.
#
# Why this exists at all: every other cornus deploy backend reads resource usage
# from something it owns (a cgroup, a containerd task, an Incus instance state).
# The kubernetes backend cannot — the cornus server is not on the node — so
# (*kubernetes.Backend).SampleMetrics asks the metrics.k8s.io aggregated API,
# which ONLY exists when metrics-server is installed (see pkg/deploy/kubernetes/
# stats.go). Without it every sample 404s, and the scenario is exercising
# nothing. So the metrics half of the kube leg is precisely as real as this
# install is.
#
# Shared by both kube E2E wrappers so there is one install implementation:
#   - the containerized dind+kind runner (e2e/container/entrypoint.sh, gated by
#     E2E_METRICS_SERVER=1), and
#   - the direct host-kind harness (pkg/e2e KubeTarget, via
#     `make e2e-kube E2E_METRICS_SERVER=1`), which invokes this script with
#     KUBECONFIG pointed at the cluster it created.
#
# VENDORED MANIFEST
# -----------------
# components.yaml is VENDORED into e2e/container/metrics-server/ and applied from
# disk, the same way the Knative release manifests and the Multus DaemonSet are,
# so a green run does not depend on an upstream release download staying
# reachable and unchanged. The copy is PINNED to $VENDORED_VERSION and verified
# against SHA256SUMS before anything is applied. To re-vendor:
#
#   ver=v0.7.2
#   cd e2e/container/metrics-server
#   curl -fsSL -o components.yaml \
#     "https://github.com/kubernetes-sigs/metrics-server/releases/download/$ver/components.yaml"
#   sha256sum components.yaml > SHA256SUMS
#
# ...then update VENDORED_VERSION here. Setting METRICS_SERVER_VERSION to
# anything else falls back to the upstream URL (needs network, not reproducible).
#
# What is NOT vendored: the metrics-server container image, which the cluster
# pulls — the same posture as every other kube add-on here.
set -euo pipefail

# The release vendored under e2e/container/metrics-server/. Bump deliberately.
VENDORED_VERSION="v0.7.2"

ver="${METRICS_SERVER_VERSION:-$VENDORED_VERSION}"

# Resolve the vendored directory relative to THIS script, so the same file works
# from /work/e2e/container inside the runner image and from the repo checkout the
# host `make e2e-kube` path runs it out of.
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
dir="${METRICS_SERVER_MANIFEST_DIR:-$here/metrics-server}"

if [ "$ver" = "$VENDORED_VERSION" ] && [ -d "$dir" ]; then
    echo ">> using the vendored metrics-server ${ver} manifest in ${dir} (no network fetch)"
    (cd "$dir" && sha256sum -c SHA256SUMS >/dev/null) \
        || { echo "vendored metrics-server manifest does not match ${dir}/SHA256SUMS; re-vendor or restore it" >&2; exit 1; }
    manifest="$dir/components.yaml"
else
    echo ">> METRICS_SERVER_VERSION=${ver} differs from the vendored ${VENDORED_VERSION} (or ${dir} is missing);"
    echo ">> falling back to the upstream release manifest — THIS NEEDS NETWORK ACCESS and is not reproducible"
    manifest="https://github.com/kubernetes-sigs/metrics-server/releases/download/${ver}/components.yaml"
fi

echo ">> installing metrics-server ${ver}"
kubectl apply -f "$manifest"

# kind's kubelets serve their metrics endpoint with a self-signed certificate
# that is not signed by the cluster CA, so metrics-server's default (verifying)
# scrape fails with an x509 error and the Deployment never becomes Ready. This is
# the one kind-specific adjustment; upstream ships the same advice.
echo ">> patching metrics-server for kind (--kubelet-insecure-tls)"
kubectl -n kube-system patch deployment metrics-server --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'

echo ">> waiting for the metrics-server rollout"
kubectl -n kube-system rollout status deployment/metrics-server --timeout=300s

# Ready is NOT sufficient: the aggregated API is served through an APIService,
# and cornus reaches metrics ONLY through /apis/metrics.k8s.io. Gate on that
# APIService reporting Available, so a scenario never races the aggregation
# layer and mistakes "not registered yet" for "no metrics".
echo ">> waiting for the metrics.k8s.io APIService to become Available"
kubectl wait --for=condition=Available --timeout=300s apiservice/v1beta1.metrics.k8s.io

# Finally, wait for a first scrape to land. metrics-server reports nothing until
# one --metric-resolution window has elapsed, and "installed but has never
# scraped" reaches a scenario as an empty series — the exact ambiguity the
# scenario's own gate exists to remove, so remove it here instead.
echo ">> waiting for the first node scrape to land"
ok=0
for _ in $(seq 1 60); do
    if kubectl get --raw /apis/metrics.k8s.io/v1beta1/nodes 2>/dev/null | grep -q '"usage"'; then
        ok=1
        break
    fi
    sleep 5
done
[ "$ok" = 1 ] || { echo "metrics-server never produced a node sample" >&2; exit 1; }
echo ">> metrics-server ready (metrics.k8s.io is serving samples)"
