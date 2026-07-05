#!/usr/bin/env bash
# Install Knative Serving plus the Kourier networking layer into the Kubernetes
# cluster the current kubectl context (or $KUBECONFIG) points at, so the
# deploy-knative E2E scenario can round-trip a real serving.knative.dev Service.
#
# Shared by both kube E2E wrappers so there is one install implementation:
#   - the containerized dind+kind runner (e2e/container/entrypoint.sh, gated by
#     E2E_KNATIVE=1), and
#   - the direct host-kind harness (pkg/e2e KubeTarget, via
#     `make e2e-kube E2E_KNATIVE=1`), which invokes this script with KUBECONFIG
#     pointed at the cluster it created.
#
# VENDORED MANIFESTS
# ------------------
# The release manifests are VENDORED into e2e/container/knative/ and applied from
# disk, the same way the Multus DaemonSet is (e2e/container/multus-daemonset-thick.yml,
# staged into the runner image by the Dockerfile). Fetching them from
# github.com/knative at test time made the scenario unrunnable behind a proxy or
# in a network-restricted CI environment, and made a green run depend on an
# upstream release download staying reachable and unchanged.
#
# The vendored copy is PINNED to $VENDORED_VERSION below, and its integrity is
# checked against e2e/container/knative/SHA256SUMS before anything is applied, so
# a bump is a deliberate act with a reviewable diff. To re-vendor:
#
#   ver=knative-vX.Y.Z
#   cd e2e/container/knative
#   for f in serving-crds.yaml serving-core.yaml serving-default-domain.yaml; do
#       curl -fsSL -o "$f" "https://github.com/knative/serving/releases/download/$ver/$f"
#   done
#   curl -fsSL -o kourier.yaml \
#       "https://github.com/knative/net-kourier/releases/download/$ver/kourier.yaml"
#   sha256sum serving-crds.yaml serving-core.yaml serving-default-domain.yaml kourier.yaml > SHA256SUMS
#
# ...then update VENDORED_VERSION here and re-run the scenario.
#
# Setting KNATIVE_VERSION to anything OTHER than the vendored version is the
# escape hatch for trying a new release without re-vendoring first: the script
# then falls back to applying the upstream URLs, which needs network access. It
# says so loudly, because that path is not reproducible.
#
# What is NOT vendored: the container IMAGES the manifests reference. Every
# Knative image is digest-pinned by upstream (gcr.io/knative-releases/...@sha256:...)
# so they are immutable, but the cluster still pulls them, and the
# serving-default-domain Job resolves sslip.io. Staging ~8 image archives would
# add hundreds of MB to a runner image that six CI legs share and only the kube
# leg with E2E_KNATIVE=1 would ever use, so the images stay a runtime pull — the
# same posture as the alpine/nginx images the other kube scenarios pull.
set -euo pipefail

# The release vendored under e2e/container/knative/. Bump deliberately (see above).
VENDORED_VERSION="knative-v1.15.0"

ver="${KNATIVE_VERSION:-$VENDORED_VERSION}"

# Resolve the vendored directory relative to THIS script, so the same file works
# from /work/e2e/container inside the runner image and from the repo checkout the
# host `make e2e-kube` path runs it out of.
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
dir="${KNATIVE_MANIFEST_DIR:-$here/knative}"

if [ "$ver" = "$VENDORED_VERSION" ] && [ -d "$dir" ]; then
    echo ">> using the vendored Knative ${ver} manifests in ${dir} (no network fetch)"
    (cd "$dir" && sha256sum -c SHA256SUMS >/dev/null) \
        || { echo "vendored Knative manifests do not match ${dir}/SHA256SUMS; re-vendor or restore them" >&2; exit 1; }
    crds="$dir/serving-crds.yaml"
    core="$dir/serving-core.yaml"
    domain="$dir/serving-default-domain.yaml"
    kourier="$dir/kourier.yaml"
else
    base="https://github.com/knative/serving/releases/download/${ver}"
    echo ">> KNATIVE_VERSION=${ver} differs from the vendored ${VENDORED_VERSION} (or ${dir} is missing);"
    echo ">> falling back to the upstream release manifests — THIS NEEDS NETWORK ACCESS and is not reproducible"
    crds="${base}/serving-crds.yaml"
    core="${base}/serving-core.yaml"
    domain="${base}/serving-default-domain.yaml"
    kourier="https://github.com/knative/net-kourier/releases/download/${ver}/kourier.yaml"
fi

echo ">> installing Knative Serving ${ver} (CRDs + core)"
kubectl apply -f "$crds"
# The webhook/controller Deployments reference the CRDs; wait for them to be
# established before applying core.
kubectl wait --for=condition=Established --timeout=120s \
    crd/services.serving.knative.dev crd/configurations.serving.knative.dev \
    crd/revisions.serving.knative.dev crd/routes.serving.knative.dev
kubectl apply -f "$core"

echo ">> installing the Kourier networking layer"
kubectl apply -f "$kourier"
kubectl patch configmap/config-network -n knative-serving --type merge \
    -p '{"data":{"ingress-class":"kourier.ingress.networking.knative.dev"}}'

echo ">> waiting for Knative Serving to be ready"
kubectl -n knative-serving rollout status deployment/controller --timeout=300s
kubectl -n knative-serving rollout status deployment/webhook --timeout=300s
kubectl -n kourier-system rollout status deployment/3scale-kourier-gateway --timeout=300s

echo ">> configuring sslip.io magic DNS so each ksvc gets a resolvable URL"
kubectl apply -f "$domain"

# Gate on the Service CRD being served before scenarios query it.
crd_ok=0
for _ in $(seq 1 30); do
    if kubectl get crd services.serving.knative.dev >/dev/null 2>&1; then
        crd_ok=1
        break
    fi
    sleep 2
done
[ "$crd_ok" = 1 ] || { echo "Knative Serving CRD did not appear" >&2; exit 1; }
echo ">> Knative Serving available"
