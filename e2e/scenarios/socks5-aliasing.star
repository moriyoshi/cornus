# Reach a compose service by its SHORT and BARE name through a SOCKS5 conduit.
#
# A service `web` in project `e2e` is deployed under the project-prefixed name
# `e2e-web`. The split-tunnel proxy registers the short compose name as a session
# alias, so all three forms reach the workload through the proxy:
#   - e2e-web.cornus.internal  (fully-qualified deployment name, via the suffix rule)
#   - web.cornus.internal      (short name, suffix rule strips -> alias remaps)
#   - web                      (bare single-label name, matched only as a live alias)
# The `socks5://127.0.0.1:PORT` conduit is session-local: `up` holds its own private
# proxy. Any port-forward-capable target (docker + kube).
#
# Source of truth: pkg/socks5 (alias table + Resolve), pkg/clientconduit
# (socks5Conduit.Add registers the alias), cmd/cornus/internal/composecli.

compose_file = "e2e/scenarios/compose-app.yaml"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed" % name)

serve()

port = free_port()
proxy = "127.0.0.1:" + port

# Foreground `compose up --conduit socks5://127.0.0.1:PORT`, backgrounded: it
# deploys the services and holds a private SOCKS5 proxy reaching them by name.
handle = compose_up_bg(file = compose_file, conduit = "socks5://" + proxy)
wait(name = "e2e-web", running = 1, timeout = "180s")
log("✓ compose up is holding a session-local SOCKS5 proxy on %s" % proxy)

# Every name form reaches nginx (container port 80) THROUGH the proxy — the host is
# resolved by the proxy, not DNS, so this proves the split-tunnel name routing.
for host in ["e2e-web.cornus.internal", "web.cornus.internal", "web"]:
    resp = http_get(url = "http://%s:80/" % host, socks5 = proxy, retry = "30s")
    assert_eq(resp["status"], 200, "%r not reachable through the proxy" % host)
    assert_contains(resp["body"], "nginx", "%r did not reach the web service" % host)
    log("✓ reached the web service as %r through the session-local proxy" % host)

# `compose down` removes the services; the held foreground `up` then self-terminates.
compose_down(file = compose_file)
res = compose_up_wait(handle = handle, timeout = "60s")
assert_eq(res["code"], 0, "backgrounded `up` should exit cleanly once its services are removed")
wait_gone("e2e-web")
log("✓ down withdrew the aliases and the proxy exited with the session")
