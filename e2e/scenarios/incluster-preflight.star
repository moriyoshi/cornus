# RBAC permission-preflight regression guard: an in-cluster cornus running under a
# Role that grants every deploy resource EXCEPT networking.k8s.io/ingresses must, at
# startup, WARN about that one missing grant in its OWN logs — and must NOT warn
# about any core grant (all present). kube-only.
#
# This proves the proactive-diagnostic path end to end on a real cluster: the server
# runs a SelfSubjectAccessReview per grant it needs (pkg/deploy/kubernetes/preflight.go)
# and logs a gap as either a required-permission WARN (core) or an optional-feature
# INFO (ingresses / secrets). Before the preflight, a missing grant was invisible
# until a deploy failed opaquely with `forbidden` (and on the stateless path was not
# even logged server-side) — the exact blind spot that made the missing ingresses
# grant so costly to find. Complements incluster-ingress.star (deploy actually fails
# forbidden without the grant); this one guards the WARNING that points at it first.

NS = "cornus-e2e"

def logs_contain(dep, needle, steps = 30):
    # The preflight runs in a goroutine at startup, so its lines may lag the pod
    # becoming Ready by a moment; poll the logs until the needle appears.
    for _ in range(steps):
        out = kubectl("-n", NS, "logs", "deploy/" + dep, "--tail=200")
        if needle in out:
            return out
        sleep(duration = "2s")
    return kubectl("-n", NS, "logs", "deploy/" + dep, "--tail=200")

if TARGET != "kube":
    log("incluster-preflight: skipped (kube-only; needs an in-cluster cornus under a real RBAC Role)")
else:
    manifest = "e2e/scenarios/incluster-cornus-restricted.yaml"
    kubectl("-n", NS, "apply", "-f", manifest)
    kubectl("-n", NS, "rollout", "status", "deploy/cornus-preflight", "--timeout=180s")
    log("✓ in-cluster cornus rolled out under a Role missing the ingresses grant")

    # The preflight must name the missing ingresses grant as an optional-feature gap.
    out = logs_contain("cornus-preflight", "missing an optional-feature permission")
    assert_contains(out, "missing an optional-feature permission",
                    "preflight must WARN about the missing grant at startup")
    assert_contains(out, "ingresses.networking.k8s.io",
                    "the preflight gap must name the ingresses resource")
    assert_contains(out, "native ingress",
                    "the ingresses gap must be reported as the native-ingress FEATURE")
    log("✓ preflight surfaced the missing ingresses grant as an optional-feature gap")

    # And it must NOT false-positive on the core grants, which are all present.
    if "missing a required permission" in out:
        fail(msg = "preflight wrongly reported a required-permission gap despite all core grants present:\n%s" % out)
    log("✓ preflight reported no required-permission gap (all core grants present)")

    kubectl("-n", NS, "delete", "-f", manifest, "--ignore-not-found")
    log("torn down")
