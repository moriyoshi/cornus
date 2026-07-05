# Knative Serving round-trip on the kubernetes backend. This scenario proves that
# a deploy opting into Knative (spec.Knative, set here via the deploy() knative=
# kwarg — the same block the `cornus deploy -f <ksvc.yaml>` descriptor loader
# populates) produces a real serving.knative.dev/v1 Service on a Knative-enabled
# cluster: the revision template carries the workload image, the autoscaling
# annotations, containerConcurrency, and the cornus.app label (so exec/logs/
# status keep resolving the revision's pods); the Route reports a status.url; and
# the ksvc is garbage-collected on remove.
#
# kube-only, and opt-in: it needs a cluster with Knative Serving installed, so it
# self-skips unless E2E_KNATIVE=1 (the containerized runner installs Knative when
# that flag is set — see e2e/container/entrypoint.sh). Mirrors the E2E_MULTUS
# gating of the deploy-multus scenarios.

NS = "cornus-e2e"  # KubeTarget's default namespace
# The canonical Knative sample: a tiny HTTP server that answers on 8080, so the
# revision passes Knative's readiness probe (a sleeping container never would).
IMAGE = "ghcr.io/knative/helloworld-go:latest"

if TARGET != "kube":
    log("deploy-knative: skipped (kube-only; asserts the generated ksvc object)")
elif getenv("E2E_KNATIVE") != "1":
    log("deploy-knative: skipped (set E2E_KNATIVE=1 on a cluster with Knative Serving installed)")
else:
    serve()

    # Deploy as a Knative Service with an autoscaling floor of 1 (so a replica is
    # always up for wait()/status), a ceiling of 3, and a per-replica concurrency
    # limit of 50.
    st = deploy(
        name = "kn-hello",
        image = IMAGE,
        ports = ["8080:8080"],
        env = {"TARGET": "cornus-e2e"},
        knative = {"min_scale": "1", "max_scale": "3", "concurrency": "50", "class": "kpa"},
    )
    assert_eq(st["backend"], "kubernetes", "knative round-trip is a kubernetes-backend feature")

    # 1. A serving.knative.dev/v1 Service was created (not a Deployment).
    ksvc = kubectl("-n", NS, "get", "ksvc", "kn-hello", "--ignore-not-found", "-o", "name")
    assert_contains(ksvc, "kn-hello", "a knative deploy must produce a serving.knative.dev Service")
    dep = kubectl("-n", NS, "get", "deployment", "kn-hello", "--ignore-not-found", "-o", "name")
    assert_eq(dep, "", "a knative deploy must NOT create a plain Deployment (got %r)" % dep)

    # 2. Revision template shape: image + single routed port.
    img = kubectl("-n", NS, "get", "ksvc", "kn-hello", "-o",
                  "jsonpath={.spec.template.spec.containers[0].image}")
    assert_contains(img, "helloworld-go", "ksvc container image must be the workload image (got %r)" % img)
    port = kubectl("-n", NS, "get", "ksvc", "kn-hello", "-o",
                   "jsonpath={.spec.template.spec.containers[0].ports[0].containerPort}")
    assert_eq(port, "8080", "the single routed containerPort must be 8080 (got %r)" % port)

    # 3. Autoscaling annotations + containerConcurrency. kubectl jsonpath cannot
    #    reliably address dotted annotation keys, so dump the annotations map and
    #    substring-match (as deploy-ingress does for the cert-manager annotation).
    anns = kubectl("-n", NS, "get", "ksvc", "kn-hello", "-o",
                   "jsonpath={.spec.template.metadata.annotations}")
    assert_contains(anns, "minScale", "minScale autoscaling annotation missing (got %r)" % anns)
    assert_contains(anns, "kpa.autoscaling.knative.dev", "class annotation must be fully qualified (got %r)" % anns)
    cc = kubectl("-n", NS, "get", "ksvc", "kn-hello", "-o",
                 "jsonpath={.spec.template.spec.containerConcurrency}")
    assert_eq(cc, "50", "containerConcurrency must map from knative.concurrency (got %r)" % cc)

    # 4. The revision template carries cornus.app so cornus can select the pods.
    applabel = kubectl("-n", NS, "get", "ksvc", "kn-hello", "-o",
                       "jsonpath={.spec.template.metadata.labels.cornus\\.app}")
    assert_eq(applabel, "kn-hello", "the revision template must carry cornus.app=<name> (got %r)" % applabel)
    log("✓ ksvc created: image, routed port, autoscaling annotations, concurrency, cornus.app label")

    # 5. Readiness: with minScale=1 a revision pod runs; cornus resolves it by
    #    cornus.app and reports it running (statusOfKnative).
    st = wait(name = "kn-hello", running = 1, timeout = "300s")
    assert_eq(st["running"], 1, "the knative revision pod never became ready")

    # 6. The Route reports a status.url — the serverless front door.
    url = ""
    for _ in range(30):
        url = kubectl("-n", NS, "get", "ksvc", "kn-hello", "-o", "jsonpath={.status.url}")
        if url != "":
            break
        sleep("2s")
    assert_contains(url, "kn-hello", "the ksvc must report a status.url once its Route is ready (got %r)" % url)
    # cornus surfaces that same URL in the deploy status.
    assert_contains(st["url"], "kn-hello", "cornus status must surface the ksvc URL (got %r)" % st["url"])
    log("✓ knative revision ready and Route URL surfaced: %s" % url)

    # 7. Restart cuts a new revision (stamps the revision template).
    restart(name = "kn-hello")
    log("✓ restart issued (new revision)")

    # 8. Removing the workload deletes the ksvc; Knative GC cascades its
    #    Configuration/Revisions/Route.
    remove(name = "kn-hello")
    gone = "x"
    for _ in range(30):
        gone = kubectl("-n", NS, "get", "ksvc", "kn-hello", "--ignore-not-found", "-o", "name")
        if gone == "":
            break
        sleep("2s")
    assert_eq(gone, "", "the ksvc must be deleted on remove (still present: %r)" % gone)
    log("✓ ksvc deleted on remove")
