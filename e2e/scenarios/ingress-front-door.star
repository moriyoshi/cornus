# The SERVER-SIDE ingress front door: the cornus server serving `x-cornus-ingress`
# itself, with host/path routing, reached over CORNUS_INGRESS_LISTEN.
#
# Before this existed the host backends warned and ignored IngressSpec entirely,
# and the only non-cluster realization was client-side (pkg/ingressemu) behind a
# SOCKS5 proxy. Here the SERVER routes: two services share one ingress host and
# split it by path, so a request's path decides which workload answers — which
# only works if the server resolves overlapping path rules the way a real ingress
# controller does (longest match wins).
#
# Runs on docker AND kube, deliberately. The routing table is identical, but what
# sits under it is not: on a host backend the proxy bridges through the daemon,
# while on kubernetes it bridges through an SPDY port-forward into a pod. On
# kubernetes the server also creates a real Ingress object, and the front door is
# the FALLBACK — used when no ingress controller is discoverable, which is exactly
# the kind cluster this harness builds. That fallback has no other coverage.
#
# Nothing is tunnelled here: this pins the routing on its own, so a failure in the
# ingress-tunnel scenario can be told apart from a failure in the routing under it.
#
# Source of truth: pkg/ingressmux (Table/Proxy), pkg/ingressroute (Resolve/
# PathMatches), pkg/server/ingress.go (ingressManager, serveIngressListener).

compose_file = "e2e/scenarios/ingress-front-door.yaml"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed" % name)

def run():
    # Bind the front door on a free port. The server serves its routing table
    # there directly, with no tunnel and no client-side proxy in the path.
    front_addr = "127.0.0.1:" + free_port()
    serve(env = {"CORNUS_INGRESS_LISTEN": front_addr})
    front = "http://" + front_addr

    compose_up(file = compose_file, detach = True)
    wait(name = "e2e-ing-web", running = 1, timeout = "240s")
    wait(name = "e2e-ing-api", running = 1, timeout = "240s")
    log("✓ both services are up; the server should now be routing front.example.com")

    # host= sends the ingress hostname while still dialling the front door's
    # address, which is exactly what a browser with a matching DNS entry would do.
    # retry_5xx: the proxy answers 502 while a workload is still coming up behind
    # its port — transient in the same way a connection refusal is.
    resp = http_get(url = front + "/", host = "front.example.com", retry = "90s", retry_5xx = True)
    assert_eq(resp["status"], 200, "the root path should reach the web service")
    assert_contains(resp["body"], "nginx", "the root path did not reach nginx")
    log("✓ / routed to the web service")

    resp = http_get(url = front + "/api", host = "front.example.com", retry = "90s", retry_5xx = True)
    assert_eq(resp["status"], 200, "/api should reach the api service")
    assert_contains(resp["body"], "api-service", "/api did not reach the api service")
    log("✓ /api routed to the api service on the same host (longest path wins)")

    # A host the front door does not serve is refused with 421 Misdirected
    # Request, not silently routed to whatever happens to be first. This is what
    # keeps a shared front door from being probed for what else it fronts.
    resp = http_get(url = front + "/", host = "not-served.example.com", retry = "5s")
    assert_eq(resp["status"], 421, "an unknown Host must be refused with 421")
    log("✓ an unserved Host was refused with 421")

    # A path with no rule of its own falls under web's "/" prefix rule and is
    # answered by nginx — whose 404 page says so. Asserting the BODY is what
    # distinguishes "routed to web, which had no such page" from "the front door
    # found no rule", since both surface as a bare 404.
    resp = http_get(url = front + "/nope", host = "front.example.com", retry = "5s")
    assert_eq(resp["status"], 404, "/nope reaches web, which has no such page")
    assert_contains(resp["body"], "nginx", "the 404 should come from nginx, not from the front door")
    log("✓ an unrouted path fell under the root rule and was answered by web itself")

    # Removing the deployments withdraws their routes: the host stops being served
    # at all, so it reads as unknown (421) rather than as a dead upstream (502).
    compose_down(file = compose_file)
    wait_gone("e2e-ing-web")
    wait_gone("e2e-ing-api")
    resp = http_get(url = front + "/", host = "front.example.com", retry = "30s")
    assert_eq(resp["status"], 421, "routes must be withdrawn when the deployments are removed")
    log("✓ down withdrew the ingress routes from the front door")

if TARGET != "docker" and TARGET != "kube":
    log("ingress-front-door: skipped (needs the docker or kube target)")
else:
    run()
