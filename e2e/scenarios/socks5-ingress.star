# Reach a workload at its `x-cornus-ingress` HOST through the SOCKS5 conduit, using
# client-side ingress EMULATION (a local HTTP reverse proxy).
#
# The ingress hosts (`app.example.com`, `www.app.example.com`) carry no
# `.cornus.internal` suffix, so the suffix rule cannot route them — only the emulated
# ingress makes them resolve: it registers each host as a published local name in the
# proxy and reverse-proxies to the workload's container port over the conduit's
# dialer. HTTP only (no TLS in the spec). Portable — the reverse proxy dials the
# workload through the server proxy, so no client kube access is needed (docker + kube).
#
# Source of truth: pkg/ingressemu (Resolve/Handler/Serve), pkg/clientconduit
# (socks5Conduit.AddIngress emulate), cmd/cornus/internal/clientagent
# (exposureController.ensure), cmd/cornus/internal/composecli.

compose_file = "e2e/scenarios/socks5-ingress.yaml"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed" % name)

serve()

port = free_port()
proxy = "127.0.0.1:" + port

# Foreground `compose up --conduit socks5://...`, backgrounded, with ingress emulation
# enabled on the held client via CORNUS_INGRESS_CONDUIT. The held session deploys the
# service, holds a private SOCKS5 proxy, and registers the emulated ingress hosts.
handle = compose_up_bg(
    file = compose_file,
    conduit = "socks5://" + proxy,
    env = {"CORNUS_INGRESS_CONDUIT": "emulate"},
)
wait(name = "e2e-web", running = 1, timeout = "180s")
log("✓ compose up is holding a socks5 proxy with ingress emulation on %s" % proxy)

# Each ingress host resolves ONLY through the emulated ingress (they bear no suffix),
# and the reverse proxy forwards to nginx (container port 80). Reaching them proves
# the whole emulate path: AddIngress -> memlisten -> reverse proxy -> PortForward.
# retry_5xx: the reverse proxy answers 502 while nginx is still starting behind the
# freshly published port; that is transient, so retry it (bounded by `retry`) the
# same way a connection refusal is retried, instead of failing on the startup race.
for host in ["app.example.com", "www.app.example.com"]:
    resp = http_get(url = "http://%s/" % host, socks5 = proxy, retry = "30s", retry_5xx = True)
    assert_eq(resp["status"], 200, "%r not reachable through the emulated ingress" % host)
    assert_contains(resp["body"], "nginx", "%r did not reach the web service" % host)
    log("✓ reached the web service at ingress host %r through the conduit" % host)

# A host that is NOT an ingress host is not published: it falls through to a direct
# dial (real DNS of a reserved .invalid name), which fails — proving only registered
# ingress hosts route inward, not every hostname.
miss = http_get(url = "http://no-such.invalid/", socks5 = proxy, retry = "1s", allow_error = True)
assert_true("error" in miss, "an unregistered host must not resolve through the proxy")
log("✓ an unregistered host correctly did not route through the conduit")

# `compose down` removes the service; the held foreground `up` then self-terminates,
# withdrawing the ingress registrations with the session.
compose_down(file = compose_file)
res = compose_up_wait(handle = handle, timeout = "60s")
assert_eq(res["code"], 0, "backgrounded `up` should exit cleanly once its services are removed")
wait_gone("e2e-web")
log("✓ down withdrew the ingress registrations and the proxy exited with the session")
