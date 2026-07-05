# An ingress tunnel fronting a REAL cluster ingress controller — the path where
# cornus does no routing of its own at all.
#
# Everywhere else the server's own ingress mux answers (see
# ingress-front-door.star and ingress-tunnel-ssh.star). Here ingress-nginx is
# installed in the cluster, so `resolveIngressFront` discovers it, `DialIngress`
# reaches it, and the tunnel becomes a RAW SPLICE: the controller receives the
# visitor's bytes untouched and does the Host/path routing itself against the
# real networking.k8s.io/v1 Ingress objects the kubernetes backend created.
#
# The discriminator between the two fronts is deliberate and behavioural rather
# than cosmetic: an unknown Host gets 421 from our mux (we refuse names we do not
# serve) but a 404 from ingress-nginx's default backend. Asserting that is how
# this scenario proves it is really on the controller and has not silently fallen
# back — which would otherwise make it a duplicate of the mux scenarios.
#
# Needs E2E_INGRESS_NGINX=1 (see e2e/container/install-ingress-nginx.sh); without
# a controller the scenario self-skips rather than quietly testing the fallback.
#
# Source of truth: pkg/deploy/kubernetes/ingressgateway.go (DialIngress,
# IngressHosts), pkg/server/ingress_tunnel.go (resolveIngressFront, ingressBridge).

compose_file = "e2e/scenarios/ingress-front-door.yaml"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed" % name)

def run(ssh):
    # class_name must match the controller's IngressClass or ingress-nginx
    # ignores the Ingress objects entirely and every route 404s.
    serve(env = {
        "CORNUS_INGRESS_CLASS": "nginx",
        "CORNUS_TUNNEL_BACKEND": "ssh",
        "CORNUS_TUNNEL_SSH_ADDR": ssh["addr"],
        "CORNUS_TUNNEL_SSH_USER": ssh["user"],
        "CORNUS_TUNNEL_SSH_BIND": "127.0.0.1:" + free_port(),
        "CORNUS_TUNNEL_SSH_KNOWN_HOSTS": ssh["known_hosts"],
        "CORNUS_TUNNEL_SSH_URL_TEMPLATE": "http://127.0.0.1:{port}",
    })

    compose_up(file = compose_file, detach = True)
    wait(name = "e2e-ing-web", running = 1, timeout = "240s")
    wait(name = "e2e-ing-api", running = 1, timeout = "240s")
    log("✓ project up; the cluster should now hold real Ingress objects")

    url = ingress_tunnel(project = "e2e-ing", authtoken_file = ssh["identity"])
    assert_true(url.startswith("http"), "expected a tunnel URL, got %r" % url)
    log("✓ ingress tunnel published at %s" % url)

    # A raw splice to the controller passes the request through untouched, so the
    # Host must be the one the Ingress declares for the controller to route it.
    # Ingress-nginx needs a moment after the Ingress objects appear before it has
    # them in its routing table, hence the generous retry.
    r = http_get(url = url + "/", host = "front.example.com", retry = "120s", retry_5xx = True)
    assert_eq(r["status"], 200, "the root path should reach web THROUGH the controller")
    assert_contains(r["body"], "nginx", "the root path did not reach the web service")
    log("✓ / routed to web by the real ingress controller")

    r = http_get(url = url + "/api", host = "front.example.com", retry = "120s", retry_5xx = True)
    assert_eq(r["status"], 200, "/api should reach the api service THROUGH the controller")
    assert_contains(r["body"], "api-service", "/api did not reach the api service")
    log("✓ /api routed to api by the real ingress controller")

    # The proof that this is the controller and not our fallback: our mux answers
    # 421 for a Host it does not serve, ingress-nginx answers 404 from its default
    # backend.
    r = http_get(url = url + "/", host = "not-served.example.com", retry = "10s")
    assert_eq(r["status"], 404, "an unknown Host should hit the controller's default backend (421 would mean we fell back to the cornus mux)")
    log("✓ an unknown Host was answered by the controller's default backend, not by our mux")

    compose_down(file = compose_file)
    wait_gone("e2e-ing-web")
    wait_gone("e2e-ing-api")
    log("✓ project removed")

if TARGET != "kube":
    log("ingress-tunnel-controller: skipped (kube-only: needs a real ingress controller)")
elif getenv(name = "E2E_INGRESS_NGINX") != "1":
    log("ingress-tunnel-controller: skipped (set E2E_INGRESS_NGINX=1 to install a controller)")
else:
    ssh = sshd()
    if ssh == None:
        log("ingress-tunnel-controller: skipped (no sshd binary)")
    else:
        run(ssh)
