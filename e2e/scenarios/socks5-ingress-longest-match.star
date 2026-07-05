# Two emulated ingresses that SHARE a host must resolve overlapping paths by longest
# match, the way a real Kubernetes ingress does — not let whichever registered last
# shadow the other.
#
# web-api is rooted at `/api` and web-root at `/`, both on `shared.example.com`. The
# host bears no `.cornus.internal` suffix, so only the emulated ingress can route it.
# Because the SOCKS5 router keys published names on host:port (path is not, and cannot
# be, a routing dimension there — CONNECT happens before any HTTP is read), the client
# folds both ingresses onto ONE listener via ingressemu.Mux and dispatches each request
# to the longest matching path. Before that fix, the second AddIngress clobbered the
# first and one path silently broke, order-dependently.
#
# traefik/whoami answers 200 on every path and prints its own `Hostname:` (set by
# compose `hostname:`), so the response body reveals which backend served — the only
# way to prove routing picked the right one. Portable (docker + kube): the reverse
# proxy dials the workloads through the server proxy, no client kube access needed.
#
# Source of truth: pkg/ingressemu (Mux/longestMatch/Handler), pkg/clientconduit
# (socks5Conduit.addEmulatedIngress -> mux().Add).

compose_file = "e2e/scenarios/socks5-ingress-longest-match.yaml"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed" % name)

serve()

port = free_port()
proxy = "127.0.0.1:" + port

# Foreground `compose up --conduit socks5://...` with ingress emulation, backgrounded.
# The held session deploys both services and registers both emulated ingresses on the
# same host through the shared Mux.
handle = compose_up_bg(
    file = compose_file,
    conduit = "socks5://" + proxy,
    env = {"CORNUS_INGRESS_CONDUIT": "emulate"},
)
wait(name = "e2e-web-root", running = 1, timeout = "180s")
wait(name = "e2e-web-api", running = 1, timeout = "180s")
log("✓ both services up; two ingresses share shared.example.com (/ and /api)")

# Longest-match routing: each request goes to the backend whose ingress path is the
# longest match. retry_5xx rides out the transient 502 while a freshly published
# backend is still starting. `Hostname: backend-*` (from compose `hostname:`) proves
# WHICH backend served, independent of the preserved ingress Host header.
cases = [
    ("/api", "backend-api"),  # exact request to the longer prefix -> web-api
    ("/api/v1/x", "backend-api"),  # deeper under /api -> still web-api
    ("/", "backend-root"),  # only / matches -> web-root
    ("/other", "backend-root"),  # /api is not a prefix of /other -> web-root
    ("/apix", "backend-root"),  # element-boundary: /apix is NOT under /api -> web-root
]
for reqpath, backend in cases:
    resp = http_get(url = "http://shared.example.com%s" % reqpath, socks5 = proxy, retry = "30s", retry_5xx = True)
    assert_eq(resp["status"], 200, "%r not reachable through the emulated ingress" % reqpath)
    assert_contains(
        resp["body"],
        "Hostname: %s" % backend,
        "%r must route to %s by longest match, but a different backend answered" % (reqpath, backend),
    )
    log("✓ %r -> %s (longest match honored)" % (reqpath, backend))

# `compose down` removes both services; the held foreground `up` then self-terminates,
# withdrawing both ingress registrations (and closing the shared listener) with the
# session.
compose_down(file = compose_file)
res = compose_up_wait(handle = handle, timeout = "60s")
assert_eq(res["code"], 0, "backgrounded `up` should exit cleanly once its services are removed")
wait_gone("e2e-web-root")
wait_gone("e2e-web-api")
log("✓ down withdrew both ingress registrations and the proxy exited with the session")
