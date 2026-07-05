# RBAC-realism regression guard: deploy a workload with a NATIVE (real)
# x-cornus-ingress AND a managed volume through an IN-CLUSTER cornus that runs under
# a restrictive RBAC Role, and prove the server created the real Ingress object and
# the PVC. kube-only.
#
# Why this scenario exists: the ordinary `kube` target runs `cornus serve` as a HOST
# process with the kind ADMIN kubeconfig, so it can create any object regardless of
# the production RBAC Role — a missing grant in deploy/k8s/cornus.yaml /
# deploy/helm/.../rbac.yaml is invisible there. A real bug slipped through exactly
# this gap: the production Role granted ingress-TLS Secrets but never
# `networking.k8s.io/ingresses`, so every `compose up` with a real ingress failed at
# deploy with `ingresses ... is forbidden` — and no E2E caught it, because the one
# emulate-ingress scenario asserts only the client-side proxy and the admin-kubeconfig
# target never exercises the Role.
#
# This closes that gap: the server runs in-cluster under cornus-incluster's Role
# (incluster-cornus.yaml), whose grants MUST mirror production. Deploying a real
# ingress + volume here forces the server to create an Ingress and a PVC under that
# Role; drop either grant and this scenario fails with `forbidden` — the production
# symptom. It complements socks5-ingress-tls.star, which guards the opposite case
# (an EMULATED ingress must create NO server Ingress).

NS = "cornus-e2e"

if TARGET != "kube":
    log("incluster-ingress: skipped (kube-only; needs an in-cluster cornus under a real RBAC Role)")
else:
    manifest = "e2e/scenarios/incluster-cornus.yaml"
    kubectl("-n", NS, "apply", "-f", manifest)
    kubectl("-n", NS, "rollout", "status", "deploy/cornus-incluster", "--timeout=180s")
    log("✓ in-cluster cornus rolled out under the restrictive cornus-incluster Role")

    # A port-forward connection profile naming the in-cluster Service (no server URL),
    # so the deploy request truly lands on the in-cluster cornus running under the Role.
    workdir = temp_dir()
    cfg = workdir + "/config.yaml"
    env = {"CORNUS_CONFIG": cfg}
    cornus(
        "config", "set-context", "incluster",
        "--pf-namespace", NS, "--pf-service", "cornus-incluster", "--pf-remote-port", "5000",
        env = env,
    )
    cornus("config", "use-context", "incluster", env = env)

    # Plain `compose up -d` (no --ingress-conduit), so the x-cornus-ingress is NATIVE:
    # the backend creates a real cluster Ingress + a PVC for the volume. If the Role
    # lacks the ingresses/pvcs grant, this deploy fails at reconcile with `forbidden`.
    app = "e2e/scenarios/incluster-ingress.yaml"
    cornus("compose", "-f", app, "up", "-d", env = env)
    log("✓ deploy accepted through the in-cluster server (no RBAC forbidden at reconcile)")

    # Prove the in-cluster server actually created the real Ingress under its Role —
    # a false-positive deploy could not have. The Ingress object is created as soon as
    # the deploy is accepted, independent of any ingress controller serving it.
    ing = kubectl("-n", NS, "get", "ingress", "iclingress-web", "-o", "jsonpath={.spec.rules[0].host}")
    assert_eq(ing.strip(), "web.incluster.example.test", "in-cluster server must create the real Ingress; got %r" % ing)
    log("✓ native ingress: in-cluster server created the real Ingress under restrictive RBAC")

    # And the managed volume's PVC (also a Role-gated resource). The server creates
    # it asynchronously during reconcile, so it can lag a moment behind the deploy
    # call returning; retry= polls through that window instead of 404-failing.
    pvc = kubectl("-n", NS, "get", "pvc", "iclingress-web-vol-0", "-o", "jsonpath={.metadata.name}", retry = "30s")
    assert_eq(pvc.strip(), "iclingress-web-vol-0", "in-cluster server must create the volume PVC; got %r" % pvc)
    log("✓ managed volume: in-cluster server created the PVC under restrictive RBAC")

    cornus("compose", "-f", app, "down", env = env)
    kubectl("-n", NS, "delete", "-f", manifest, "--ignore-not-found")
    log("torn down")
