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
# It applies the upstream release manifests from the internet (Knative's install
# is large and is not vendored, unlike Multus), so it needs network access.
# Override the release with KNATIVE_VERSION (default below).
set -euo pipefail

ver="${KNATIVE_VERSION:-knative-v1.15.0}"
base="https://github.com/knative/serving/releases/download/${ver}"
kourier="https://github.com/knative/net-kourier/releases/download/${ver}/kourier.yaml"

echo ">> installing Knative Serving ${ver} (CRDs + core)"
kubectl apply -f "${base}/serving-crds.yaml"
# The webhook/controller Deployments reference the CRDs; wait for them to be
# established before applying core.
kubectl wait --for=condition=Established --timeout=120s \
    crd/services.serving.knative.dev crd/configurations.serving.knative.dev \
    crd/revisions.serving.knative.dev crd/routes.serving.knative.dev
kubectl apply -f "${base}/serving-core.yaml"

echo ">> installing the Kourier networking layer"
kubectl apply -f "$kourier"
kubectl patch configmap/config-network -n knative-serving --type merge \
    -p '{"data":{"ingress-class":"kourier.ingress.networking.knative.dev"}}'

echo ">> waiting for Knative Serving to be ready"
kubectl -n knative-serving rollout status deployment/controller --timeout=300s
kubectl -n knative-serving rollout status deployment/webhook --timeout=300s
kubectl -n kourier-system rollout status deployment/3scale-kourier-gateway --timeout=300s

echo ">> configuring sslip.io magic DNS so each ksvc gets a resolvable URL"
kubectl apply -f "${base}/serving-default-domain.yaml"

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
