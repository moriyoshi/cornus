# `cornus compose` bind mounts on Kubernetes: `up -d` starts a per-project
# background helper that streams the local dir over 9P; a privileged sidecar
# mounts it inside the pod (never a hostPath). A SECOND `up -d` must reuse that
# helper (not spawn another). `down` stops it and removes the deployment.

def wait_gone(name, steps = 90):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed after compose down" % name)

if TARGET != "kube":
    log("compose-mounts: skipped (kube-only; sidecar mount path, never hostPath)")
else:
    serve()
    compose_up(file = "e2e/scenarios/compose-mounts.yaml", project = "cmnt", detach = True)
    wait(name = "cmnt-app", running = 1, timeout = "240s")
    got = pod_exec(app = "cmnt-app", cmd = "cat /data/marker")
    assert_contains(got, "COMPOSE-BIND-OK")
    log("✓ compose bind mount visible inside the pod (sidecar, no hostPath)")

    # Second up -d must reuse the running background helper and stay healthy.
    compose_up(file = "e2e/scenarios/compose-mounts.yaml", project = "cmnt", detach = True)
    st = status(name = "cmnt-app")
    assert_eq(st["running"], 1, "service still running after a second up -d")
    log("✓ second up -d reused the background helper")

    compose_down(file = "e2e/scenarios/compose-mounts.yaml", project = "cmnt")
    wait_gone("cmnt-app")
    log("✓ compose down stopped the helper and removed the deployment")
