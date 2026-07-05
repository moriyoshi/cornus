#!/usr/bin/env bash
# Install the ingress-nginx controller into the kind cluster the current kubectl
# context (or $KUBECONFIG) points at, so the ingress E2E scenarios can exercise
# the REAL-CONTROLLER path — cornus dialling an actual controller Service and
# letting it do the Host/path routing — rather than only the server-side ingress
# mux it falls back to when no controller exists.
#
# Shared by both kube E2E wrappers so there is one install implementation:
#   - the containerized dind+kind runner (e2e/container/entrypoint.sh, gated by
#     E2E_INGRESS_NGINX=1), and
#   - the direct host-kind harness (pkg/e2e KubeTarget, via
#     `make e2e-kube E2E_INGRESS_NGINX=1`), which invokes this script with
#     KUBECONFIG pointed at the cluster it created.
#
# It applies the upstream kind-provider manifest from the internet, so it needs
# network access. Override the release with INGRESS_NGINX_VERSION.
set -euo pipefail

ver="${INGRESS_NGINX_VERSION:-controller-v1.11.3}"
manifest="https://raw.githubusercontent.com/kubernetes/ingress-nginx/${ver}/deploy/static/provider/kind/deploy.yaml"

# The kind-provider manifest pins the controller to a node labelled
# ingress-ready=true (kind's own docs have you set it at cluster-creation time).
# The harness creates its cluster with a plain `kind create cluster`, so label
# the nodes here instead — otherwise the controller pod sits Pending forever.
echo ">> labelling nodes ingress-ready=true"
kubectl label nodes --all ingress-ready=true --overwrite

echo ">> installing ingress-nginx ${ver}"
kubectl apply -f "${manifest}"

echo ">> waiting for the ingress-nginx controller to become ready"
kubectl wait --namespace ingress-nginx \
    --for=condition=ready pod \
    --selector=app.kubernetes.io/component=controller \
    --timeout=300s

# The controller being ready is NOT sufficient. The manifest installs a
# ValidatingWebhookConfiguration with failurePolicy: Fail, whose CA bundle is
# injected by the ingress-nginx-admission-patch Job. Creating an Ingress before
# that Job completes is rejected with an x509/webhook error — which surfaces to a
# scenario as a confusing `compose up` failure rather than anything about
# webhooks. Wait for the Job so the race cannot happen.
echo ">> waiting for the ingress-nginx admission webhook to be patched"
kubectl wait --namespace ingress-nginx \
    --for=condition=complete job/ingress-nginx-admission-patch \
    --timeout=300s

# cornus discovers the controller by well-known (namespace, service) pairs, of
# which ingress-nginx/ingress-nginx-controller is the first. Confirm it exists so
# a manifest reshuffle upstream fails here, loudly, rather than silently sending
# every scenario down the mux fallback.
kubectl get service ingress-nginx-controller --namespace ingress-nginx >/dev/null
echo ">> ingress-nginx ready (service ingress-nginx/ingress-nginx-controller)"
