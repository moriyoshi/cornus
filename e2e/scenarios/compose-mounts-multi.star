# Regression guard for: the caretaker mounts the FIRST client-local volume, then
# hangs on the second and later ones. Reproduces a real service — four
# client-local bind mounts with NESTED targets under /app, an anonymous volume
# interspersed between them, and mixed ro/rw. See compose-mounts-multi.yaml for
# the topology.
#
# Failure signal: `cornus compose up -d` streams every local dir over 9P and a
# single privileged sidecar mounts them all; the app pod is not Running until
# EVERY mount is live (caretaker.Ready gates the app container). So a stall on
# the 2nd+ mount makes `wait` time out here rather than reaching the read-backs.
# Reading each nested marker back then proves every mount is independently live
# and correctly routed (not just the first).
#
# kube target only: the sidecar 9P mount path (never a hostPath). Other targets
# need root to kernel-9p-mount on the host, and compose-mounts.star is kube-only
# for the same reason.

def wait_gone(name, steps = 90):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed after compose down" % name)

if TARGET != "kube":
    log("compose-mounts-multi: skipped (kube-only; sidecar mount path, never hostPath)")
else:
    serve()
    compose_up(file = "e2e/scenarios/compose-mounts-multi.yaml", project = "cmm", detach = True)

    # If any mount past the first stalls, readiness never fires and this times out.
    wait(name = "cmm-init", running = 1, timeout = "240s")
    log("✓ all client-local mounts reached readiness (pod Running)")

    # Every nested mount must be independently live and carry its own content.
    checks = [
        ("/app/marker", "MM-APP"),
        ("/app/data/marker", "MM-DATA"),
        ("/app/config/marker", "MM-CONFIG"),
        ("/opt/extra/marker", "MM-EXTRA"),
    ]
    for path, want in checks:
        got = pod_exec(app = "cmm-init", cmd = "cat " + path)
        assert_contains(got, want)
    log("✓ all four nested client-local mounts live and correctly routed")

    # The interspersed anonymous volume must be present as a live dir, proving a
    # non-bind entry in the mix did not derail the later client-local mounts.
    # (cornus:e2e is minimal; test with a shell builtin, not `mountpoint`.)
    cache = pod_exec(app = "cmm-init", cmd = "[ -d /app/cache ] && echo PRESENT || echo NO")
    assert_contains(cache, "PRESENT")
    log("✓ interspersed anonymous volume present alongside the client-local mounts")

    compose_down(file = "e2e/scenarios/compose-mounts-multi.yaml", project = "cmm")
    wait_gone("cmm-init")
    log("✓ compose down stopped the helper and removed the deployment")
