# Ingress SETTINGS propagation, judged by a REAL controller.
#
# deploy-ingress.star already proves the kubernetes backend writes the right
# fields onto the networking.k8s.io/v1 Ingress — it reads them straight back with
# `kubectl -o jsonpath`. What no scenario proved is the step after that: that a
# real controller, handed those objects, actually BEHAVES differently because of
# them. A field can be spelled correctly and still be inert (wrong annotation
# key, a pathType the backend never forwards, a port number that names nothing).
#
# So every assertion here is behavioural. Each service in the compose file pins
# one setting and is reached over HTTP through the installed ingress-nginx; the
# setting is proved by what comes back, not by what the object says. The one
# place this scenario reads the object at all is to distinguish "created but not
# served" from "never created" for the className case, where there is no
# positive request that could anchor it.
#
# NOT a duplicate of ingress-tunnel-controller.star: that one proves the ingress
# TUNNEL splices to the controller, and reaches it through the tunnel. This one
# takes the tunnel out of the path entirely and dials the controller's NodePort
# directly, so a failure here is a settings-propagation failure and cannot be a
# tunnel failure.
#
# Needs E2E_INGRESS_NGINX=1 (see e2e/container/install-ingress-nginx.sh); without
# a controller there is nothing to judge the settings, so the scenario self-skips
# rather than degrade into the object-shape assertions deploy-ingress.star owns.
#
# Source of truth: pkg/deploy/kubernetes/kubernetes.go (buildIngress),
# pkg/api/deploy.go (IngressSpec), pkg/compose/project.go (translateIngress).

NS = "cornus-e2e"  # KubeTarget's default namespace
COMPOSE = "e2e/scenarios/ingress-settings-controller.yaml"

# The BYO key pair the TLS case loads into its Secret. Its SAN fixes the host the
# tlsapp service declares; it is self-signed with CA:TRUE, so the same file
# doubles as the client's trust root.
CERT = "e2e/scenarios/certs/ingress-byo.crt"
KEY = "e2e/scenarios/certs/ingress-byo.key"
TLS_SECRET = "ingset-tls"
TLS_HOST = "app.native-cert.example.test"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed" % name)

def controller_base_urls():
    """The controller's HTTP and HTTPS endpoints, as reachable from the harness.

    kind runs each node as a container on a docker network the harness shares, so
    the node's InternalIP plus the controller Service's nodePort is a direct route
    to ingress-nginx — no tunnel, no port-forward, no DNS. nodePort is read from
    the Service rather than assumed because it is allocated dynamically, and it is
    present whether the manifest types the Service NodePort or LoadBalancer.
    """
    ip = kubectl("get", "nodes", "-o",
                 "jsonpath={.items[0].status.addresses[?(@.type=='InternalIP')].address}").strip()
    assert_true(ip != "", "could not resolve a kind node InternalIP to reach the controller on")
    http_port = kubectl("-n", "ingress-nginx", "get", "svc", "ingress-nginx-controller", "-o",
                        "jsonpath={.spec.ports[?(@.name=='http')].nodePort}").strip()
    https_port = kubectl("-n", "ingress-nginx", "get", "svc", "ingress-nginx-controller", "-o",
                         "jsonpath={.spec.ports[?(@.name=='https')].nodePort}").strip()
    assert_true(http_port != "" and https_port != "",
                "ingress-nginx-controller exposes no nodePorts (http=%r https=%r)" % (http_port, https_port))
    return ("http://%s:%s" % (ip, http_port), "https://%s:%s" % (ip, https_port))

def run():
    # CORNUS_INGRESS_CLASS is the server-wide default the controller answers to.
    # Every service except the two className cases leaves class_name unset, so
    # each of them also rides this default — if it stopped propagating, all of
    # them would 404 at once.
    serve(env = {"CORNUS_INGRESS_CLASS": "nginx"})

    # The TLS Secret is BYO here: provisioning certificates is cert-manager's job,
    # not cornus's, and what this case tests is that cornus points the Ingress at
    # the named Secret. Delete-then-create keeps a re-run idempotent.
    kubectl("-n", NS, "delete", "secret", TLS_SECRET, "--ignore-not-found")
    kubectl("-n", NS, "create", "secret", "tls", TLS_SECRET, "--cert=" + CERT, "--key=" + KEY)
    log("✓ BYO TLS Secret %s loaded for %s" % (TLS_SECRET, TLS_HOST))

    http_base, https_base = controller_base_urls()
    log("controller reachable at %s (and %s)" % (http_base, https_base))

    compose_up(file = COMPOSE, detach = True)
    for svc in ["rewrite", "exact", "port", "classok", "classbad", "tlsapp"]:
        wait(name = "e2e-ingset-" + svc, running = 1, timeout = "240s")
    log("✓ all six services up; the cluster now holds their Ingress objects")

    # ---- 4a. className the controller owns -----------------------------------
    # Deliberately FIRST. It is the cheapest positive route, so a 200 here is what
    # establishes that ingress-nginx has ingested this compose up's Ingress
    # objects at all. Every later negative assertion (a 404 that is supposed to
    # mean "no rule matched") would otherwise be satisfiable by a controller that
    # simply had not caught up yet.
    #
    # retry_until=200 and not retry_5xx alone: the two transient answers here are
    # DIFFERENT statuses. Once the Ingress is ingested but the Service has no ready
    # endpoints, ingress-nginx answers 503 — a 5xx, absorbed either way. Before it
    # is ingested, the host matches no rule at all and the DEFAULT backend answers
    # 404, which retry_5xx returns verbatim and the assertion below then fails on
    # controller sync latency rather than on className propagation. Which of the two
    # windows the first request lands in is a coin flip (~2s after Apply), and it
    # has landed on each side in CI. retry_until covers both.
    r = http_get(url = http_base + "/", host = "classok.settings.example.test",
                 retry = "180s", retry_5xx = True, retry_until = 200)
    assert_eq(r["status"], 200, "an Ingress with the controller's own class must be served")
    assert_contains(r["body"], "GET / HTTP", "classok did not reach the whoami backend")
    log("✓ className=nginx: served by the controller (and the controller is now caught up)")

    # ---- 4b. className nothing matches ---------------------------------------
    # The object must EXIST with the class we asked for; only then does a 404 mean
    # "the controller declined it on class" rather than "the deploy never created
    # it", which would make this case pass for the wrong reason.
    cls = kubectl("-n", NS, "get", "ingress", "e2e-ingset-classbad", "-o",
                  "jsonpath={.spec.ingressClassName}").strip()
    assert_eq(cls, "cornus-e2e-absent",
              "the unmatched class must still reach the object (got %r)" % cls)
    r = http_get(url = http_base + "/", host = "classbad.settings.example.test", retry = "15s")
    assert_eq(r["status"], 404,
              "an Ingress whose class no IngressClass matches must be ignored by the controller")
    log("✓ className=cornus-e2e-absent: object created, controller declined it")

    # ---- 1. Annotations ------------------------------------------------------
    # whoami echoes the request line, so the rewritten path is directly visible.
    # Asserting the ECHOED path is what separates "the annotation took effect"
    # from "the request was merely routed"; a 200 alone would say nothing.
    # retry_until=200 on every POSITIVE fetch below, for the same reason as 4a: the
    # anchor proves the controller has ingested this batch, but each host is its own
    # rule and nginx reloads asynchronously, so an individual one can still be a
    # moment behind and answer 404 from the default backend. The negative cases
    # deliberately do NOT use it — retrying a 404 you are asserting would make "not
    # yet" and "never" the same observation.
    r = http_get(url = http_base + "/app/deep/path", host = "rewrite.settings.example.test",
                 retry = "120s", retry_5xx = True, retry_until = 200)
    assert_eq(r["status"], 200, "/app should reach the rewrite service")
    assert_contains(r["body"], "GET /rewritten HTTP",
                    "rewrite-target did not take effect: the backend saw the original path")
    log("✓ annotations: nginx rewrite-target rewrote /app/deep/path to /rewritten")

    # ---- 2. pathType Exact ---------------------------------------------------
    r = http_get(url = http_base + "/exact", host = "exact.settings.example.test",
                 retry = "120s", retry_5xx = True, retry_until = 200)
    assert_eq(r["status"], 200, "an Exact rule must match its own path")
    assert_contains(r["body"], "GET /exact HTTP", "/exact did not reach the whoami backend")
    r = http_get(url = http_base + "/exact/sub", host = "exact.settings.example.test", retry = "15s")
    assert_eq(r["status"], 404,
              "an Exact rule must NOT match below its path (404 expected; 200 means it was treated as Prefix)")
    log("✓ pathType=Exact: /exact served, /exact/sub unmatched")

    # ---- 3. Explicit port ----------------------------------------------------
    # The Ingress must name 8080, not the first published port (9999) that nothing
    # listens on. Read the object too: it tells a 502 caused by port selection
    # apart from a 502 caused by the pod being unhealthy.
    bport = kubectl("-n", NS, "get", "ingress", "e2e-ingset-port", "-o",
                    "jsonpath={.spec.rules[0].http.paths[0].backend.service.port.number}").strip()
    assert_eq(bport, "8080", "the explicit ingress port must win over the first published port (got %r)" % bport)
    r = http_get(url = http_base + "/", host = "port.settings.example.test",
                 retry = "120s", retry_5xx = True, retry_until = 200)
    assert_eq(r["status"], 200,
              "the explicitly selected port must serve (a 502 means the ingress was wired to the dead first port)")
    assert_contains(r["body"], "GET / HTTP", "the port service did not reach the whoami backend")
    log("✓ port: the explicitly selected 8080 was wired, not the dead first published port")

    # ---- 5. TLS --------------------------------------------------------------
    # ca_file pins OUR certificate as the only trust root, and host= sends it as
    # SNI. If secret_name or the TLS host failed to propagate, ingress-nginx falls
    # back to its own "Kubernetes Ingress Controller Fake Certificate" — which
    # this client cannot verify, so the GET fails outright rather than quietly
    # passing. That is the whole assertion: a 200 here is only reachable with the
    # Secret's certificate served for this SNI.
    ing_secret = kubectl("-n", NS, "get", "ingress", "e2e-ingset-tlsapp", "-o",
                         "jsonpath={.spec.tls[0].secretName}").strip()
    assert_eq(ing_secret, TLS_SECRET, "tls.secret_name must reach the Ingress (got %r)" % ing_secret)
    ing_tls_host = kubectl("-n", NS, "get", "ingress", "e2e-ingset-tlsapp", "-o",
                           "jsonpath={.spec.tls[0].hosts[0]}").strip()
    assert_eq(ing_tls_host, TLS_HOST, "the TLS entry must cover the rule host (got %r)" % ing_tls_host)
    r = http_get(url = https_base + "/", host = TLS_HOST, ca_file = CERT,
                 retry = "120s", retry_5xx = True, retry_until = 200)
    assert_eq(r["status"], 200, "HTTPS must terminate with the certificate from the named Secret")
    assert_contains(r["body"], "GET / HTTP", "the TLS service did not reach the whoami backend")
    log("✓ tls: the controller served the BYO Secret's certificate for %s" % TLS_HOST)

    compose_down(file = COMPOSE)
    for svc in ["rewrite", "exact", "port", "classok", "classbad", "tlsapp"]:
        wait_gone("e2e-ingset-" + svc)
    kubectl("-n", NS, "delete", "secret", TLS_SECRET, "--ignore-not-found")
    log("torn down")

if TARGET != "kube":
    log("ingress-settings-controller: skipped (kube-only: needs a real ingress controller)")
elif getenv(name = "E2E_INGRESS_NGINX") != "1":
    log("ingress-settings-controller: skipped (set E2E_INGRESS_NGINX=1 to install a controller)")
else:
    run()
