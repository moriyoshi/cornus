# Drive the Docker Compose-compatible CLI end to end against the running server.
# Uses public images so it works on both targets without a build.

compose_file = "e2e/scenarios/compose-app.yaml"

serve()

# -d: web publishes a port, and a foreground `up` now holds the auto-forward of
# published ports until Ctrl-C (docker-compose-like); detached mode hands the
# forward to the per-project background helper instead, so this command returns
# (and `down` releases it).
compose_up(file = compose_file, detach = True)

# Both services should be deployed (project-qualified names e2e-web / e2e-cache).
ps = compose_ps(file = compose_file)
log(ps)
assert_contains(ps, "web")
assert_contains(ps, "cache")

# web is published; give it a moment then confirm the deployment is up via status.
st = wait(name = "e2e-web", running = 1, timeout = "180s")
assert_eq(st["running"], 1)

# The published port (8080:80) is reachable on the harness host on BOTH
# targets: on docker via dockerhost's own host publish (the background helper's
# auto-forward skips on EADDRINUSE there), and on kube via the auto-forward the
# helper holds — kubernetes has no host publish, so this proves the forward.
resp = http_get(url = "http://127.0.0.1:8080/", retry = "30s")
assert_eq(resp["status"], 200, "compose-published port did not serve")
assert_contains(resp["body"], "nginx")
log("✓ compose-published port served live over HTTP")

compose_down(file = compose_file)

# After down, the deployment should be gone. Deletion is asynchronous on some
# backends (Kubernetes removes the Deployment in the background), so poll rather
# than assert synchronously.
def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "expected %s removed after compose down" % name)

wait_gone("e2e-web")
