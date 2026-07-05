# A MOUNTED service reached by its short and bare name through a SOCKS5 conduit.
#
# `compose up -d` hands a service with client-local bind mounts to the background
# agent, which runs its deploy-attach (9P) session AND registers the service's
# short-name alias with the conduit. That alias registration used to be gated on
# ForwardPorts, so a mounted service with NO published ports never got its alias
# (reachable only by fully-qualified name). This is the end-to-end regression for
# that fix (unit: TestProjectMountedServiceRegistersAlias): nginx serves a
# bind-mounted docroot, and one GET through the proxy proves BOTH the 9P mount and
# the alias at once — the served body is the mounted marker.
#
# Kube-only: client-local mounts are realized by the privileged 9P caretaker sidecar
# in the pod. The docker path needs a root server with the 9p kernel module (skipped
# in the harness), so like the other mount scenarios this gates on the kube target.
#
# Source of truth: cmd/cornus/internal/clientagent (Project.Apply reconcile ->
# exposureController alias registration, project.go + controllers.go),
# pkg/clientconduit + pkg/socks5 (alias registration/resolution).

compose_file = "e2e/scenarios/socks5-mount.yaml"

def wait_gone(name, steps = 90):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed after compose down" % name)

def get_marker(url, proxy, marker, steps = 30):
    # The mounted docroot may become visible a beat after the pod reports running,
    # and http_get does not retry on HTTP status — so poll until the mounted marker
    # is actually served (a 404/empty body before the mount lands is not an error).
    for _ in range(steps):
        r = http_get(url = url, socks5 = proxy, retry = "10s")
        if marker in r["body"]:
            return r
        sleep(duration = "2s")
    fail(msg = "marker %r never served at %s" % (marker, url))

if TARGET != "kube":
    log("socks5-mount: skipped (kube-only; client-local mounts use the privileged 9P sidecar)")
else:
    serve()

    port = free_port()
    proxy = "127.0.0.1:" + port

    # up -d: the agent holds the 9P mount session for `web` AND (post-fix) registers
    # its short-name alias, even though the service publishes no ports.
    compose_up(file = compose_file, project = "smnt", detach = True,
               conduit = "socks5://.shared:" + port)
    wait(name = "smnt-web", running = 1, timeout = "240s")
    log("✓ mounted service is up and the agent holds a SOCKS5 proxy on %s" % proxy)

    # Reach the MOUNTED nginx by its short and bare names through the proxy. The body
    # is the mounted marker, so each GET proves the 9P mount AND the alias for a
    # port-less mounted service in one shot.
    for host in ["web.cornus.internal", "web"]:
        r = get_marker("http://%s:80/" % host, proxy, "SOCKS5-MOUNT-OK")
        assert_eq(r["status"], 200, "%r not reachable through the proxy" % host)
        log("✓ reached the mounted service as %r (mounted marker served)" % host)

    compose_down(file = compose_file, project = "smnt")
    wait_gone("smnt-web")
    log("✓ down tore down the mount session and the proxy")
