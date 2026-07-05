# Automatic ingress creation on the kubernetes backend. This scenario proves the
# Kubernetes-only ingress feature end to end against a real cluster: that a deploy
# opting into ingress produces a networking.k8s.io/v1 Ingress with the expected
# host (auto-derived from CORNUS_INGRESS_DOMAIN or given explicitly), backend
# Service + port, ingress class, and cert-manager TLS; and that the Ingress is
# garbage-collected with the Deployment on remove (owner-ref cascade). kube-only
# (needs a real cluster with a working GC controller); other targets skip.
#
# Section 6 goes one step further and fetches THROUGH a controller when one is
# installed, closing the "the object is right but is it SERVED?" gap. It is gated
# on the controller actually being present in the cluster, not on the target:
# this scenario runs on BOTH kube CI legs — the plain one (no controller, object
# assertions only) and kube-ingress (E2E_INGRESS_NGINX=1) — so the gate has to be
# the thing that varies between them.

NS = "cornus-e2e"  # KubeTarget's default namespace
DOMAIN = "preview.example.test"

# The section-6 workload: a real HTTP server (whoami echoes the request line it
# received, so the body proves the request reached the backend rather than being
# answered by the controller itself) reached at its AUTO-DERIVED ingress host.
EDGE = "edge"
EDGE_HOST = EDGE + "." + DOMAIN

def controller_http_base():
    """The installed ingress-nginx's HTTP endpoint, or "" when none is installed.

    Probed off the cluster rather than off E2E_INGRESS_NGINX so the fetch is
    gated on the controller EXISTING: the flag installs it, but this scenario is
    also run (by `make e2e-kube`, and by the plain kube CI leg) against clusters
    where it was never set. --ignore-not-found on a cluster-scoped namespace read
    is the one probe that exits 0 either way, so it comes first.

    kind runs each node as a container on a docker network the harness shares, so
    the node InternalIP plus the controller Service's http nodePort is a direct
    route in — no tunnel, no port-forward, no DNS. Same route
    ingress-settings-controller.star uses.
    """
    if kubectl("get", "namespace", "ingress-nginx", "--ignore-not-found", "-o", "name").strip() == "":
        return ""
    svc = kubectl("-n", "ingress-nginx", "get", "svc", "ingress-nginx-controller",
                  "--ignore-not-found", "-o", "name").strip()
    if svc == "":
        return ""
    ip = kubectl("get", "nodes", "-o",
                 "jsonpath={.items[0].status.addresses[?(@.type=='InternalIP')].address}").strip()
    port = kubectl("-n", "ingress-nginx", "get", "svc", "ingress-nginx-controller", "-o",
                   "jsonpath={.spec.ports[?(@.name=='http')].nodePort}").strip()
    # The controller is installed, so a missing address is a real failure, not a
    # reason to silently fall back to the object-only path.
    assert_true(ip != "" and port != "",
                "ingress-nginx is installed but unreachable (node ip=%r http nodePort=%r)" % (ip, port))
    return "http://%s:%s" % (ip, port)

if TARGET != "kube":
    log("deploy-ingress: skipped (kube-only; asserts the generated Ingress object)")
else:
    # Configure the server's ingress defaults: a base wildcard domain for host
    # auto-derivation, a default IngressClass, and a default cert-manager issuer.
    # These are read by the kubernetes backend at construction (serve boots it).
    addr = serve(env = {
        "CORNUS_INGRESS_DOMAIN": DOMAIN,
        "CORNUS_INGRESS_CLASS": "nginx",
        "CORNUS_INGRESS_TLS_ISSUER": "letsencrypt-test",
    })

    # Up-front cleanup: Starlark has no defer, so a run that fails midway leaves
    # its workloads (and their owned Ingresses/Secrets) in the namespace. The
    # sections below assert on freshly created objects and on their GC, both of
    # which a leftover from a previous run can satisfy vacuously.
    for stale in ["shop", "api", "multi", "native-byo", EDGE]:
        if status(name = stale)["total"] > 0:
            remove(name = stale)

    # 1. Bare-enable ingress: no explicit host, so the backend derives
    #    "<name>.<CORNUS_INGRESS_DOMAIN>". This is the preview-environment path —
    #    a per-PR deploy gets a public URL with zero host wiring.
    deploy(
        name = "shop",
        image = "alpine:3.20",
        entrypoint = ["sleep"],
        command = ["3600"],
        ports = ["8080:80"],
        ingress = {},  # {} == enable with all defaults
    )
    st = wait(name = "shop", running = 1, timeout = "240s")
    assert_eq(st["running"], 1, "shop pod never became ready")

    ing = kubectl("-n", NS, "get", "ingress", "shop", "--ignore-not-found", "-o", "name")
    assert_contains(ing, "ingress.networking.k8s.io/shop", "a deploy opting into ingress must get an Ingress")

    host = kubectl("-n", NS, "get", "ingress", "shop", "-o", "jsonpath={.spec.rules[0].host}")
    assert_eq(host, "shop." + DOMAIN, "host must be auto-derived from the base domain (got %r)" % host)

    # Backend wiring: the rule points at the same-named ClusterIP Service on the
    # first published container port.
    bsvc = kubectl("-n", NS, "get", "ingress", "shop", "-o",
                   "jsonpath={.spec.rules[0].http.paths[0].backend.service.name}")
    assert_eq(bsvc, "shop", "ingress backend must be the workload Service (got %r)" % bsvc)
    bport = kubectl("-n", NS, "get", "ingress", "shop", "-o",
                    "jsonpath={.spec.rules[0].http.paths[0].backend.service.port.number}")
    assert_eq(bport, "80", "ingress backend port must be the container port (got %r)" % bport)

    # Ingress class comes from the server default (CORNUS_INGRESS_CLASS).
    cls = kubectl("-n", NS, "get", "ingress", "shop", "-o", "jsonpath={.spec.ingressClassName}")
    assert_eq(cls, "nginx", "ingressClassName must fall back to the server default (got %r)" % cls)

    # Default path is a "/" Prefix rule.
    ptype = kubectl("-n", NS, "get", "ingress", "shop", "-o",
                    "jsonpath={.spec.rules[0].http.paths[0].pathType}")
    assert_eq(ptype, "Prefix", "default pathType must be Prefix (got %r)" % ptype)
    log("✓ bare-enable ingress: host auto-derived, backend/port/class/pathType all mapped")

    # 2. Explicit host + path + TLS via a cert-manager issuer. With only a tls
    #    request (no secret), the secret name defaults to "<name>-tls" and the
    #    server-default issuer is stamped as the cert-manager annotation.
    deploy(
        name = "api",
        image = "alpine:3.20",
        entrypoint = ["sleep"],
        command = ["3600"],
        ports = ["8080:80"],
        ingress = {"host": "api.example.test", "path": "/v1", "tls_issuer": "letsencrypt-test"},
    )
    st = wait(name = "api", running = 1, timeout = "240s")
    assert_eq(st["running"], 1, "api pod never became ready")

    ahost = kubectl("-n", NS, "get", "ingress", "api", "-o", "jsonpath={.spec.rules[0].host}")
    assert_eq(ahost, "api.example.test", "explicit host must override the base domain (got %r)" % ahost)
    apath = kubectl("-n", NS, "get", "ingress", "api", "-o",
                    "jsonpath={.spec.rules[0].http.paths[0].path}")
    assert_eq(apath, "/v1", "explicit path must be honored (got %r)" % apath)
    tlssecret = kubectl("-n", NS, "get", "ingress", "api", "-o", "jsonpath={.spec.tls[0].secretName}")
    assert_eq(tlssecret, "api-tls", "tls secret must default to <name>-tls (got %r)" % tlssecret)
    tlshost = kubectl("-n", NS, "get", "ingress", "api", "-o", "jsonpath={.spec.tls[0].hosts[0]}")
    assert_eq(tlshost, "api.example.test", "tls host must match the rule host (got %r)" % tlshost)

    # cert-manager annotation: dump the annotations map and substring-match, since
    # kubectl jsonpath cannot reliably address a dotted annotation key.
    anns = kubectl("-n", NS, "get", "ingress", "api", "-o", "jsonpath={.metadata.annotations}")
    assert_contains(anns, "letsencrypt-test", "cert-manager cluster-issuer annotation missing (got %r)" % anns)
    log("✓ explicit host + path + TLS via the server-default cert-manager issuer")

    # 3. Multiple hosts including the "@" apex. With a client domain override, "@"
    #    resolves to the domain itself and "shop" is a normal subdomain; each host
    #    becomes its own rule and they share one TLS entry.
    deploy(
        name = "multi",
        image = "alpine:3.20",
        entrypoint = ["sleep"],
        command = ["3600"],
        ports = ["8080:80"],
        ingress = {"hosts": "@,shop.example.test", "domain": "example.test", "tls_issuer": "letsencrypt-test"},
    )
    st = wait(name = "multi", running = 1, timeout = "240s")
    assert_eq(st["running"], 1, "multi pod never became ready")

    h0 = kubectl("-n", NS, "get", "ingress", "multi", "-o", "jsonpath={.spec.rules[0].host}")
    assert_eq(h0, "example.test", "\"@\" apex must resolve to the base domain (got %r)" % h0)
    h1 = kubectl("-n", NS, "get", "ingress", "multi", "-o", "jsonpath={.spec.rules[1].host}")
    assert_eq(h1, "shop.example.test", "second host must be the subdomain (got %r)" % h1)
    # A single TLS entry lists both hosts.
    tlsh = kubectl("-n", NS, "get", "ingress", "multi", "-o", "jsonpath={.spec.tls[0].hosts[*]}")
    assert_contains(tlsh, "example.test", "tls must cover the apex host (got %r)" % tlsh)
    assert_contains(tlsh, "shop.example.test", "tls must cover the subdomain host (got %r)" % tlsh)
    log("✓ multiple hosts with an \"@\" apex, one shared TLS entry")
    remove(name = "multi")

    # 4. A native-ingress certificate rule is client-local configuration. The
    #    detached deploy loads the key pair, derives its selector from the DNS SAN,
    #    sends it only over the loopback server connection, and the kube backend
    #    creates an owned kubernetes.io/tls Secret wired into the Ingress.
    #    The ingress host is spelled in MIXED CASE on purpose: DNS is
    #    case-insensitive, the client normalizes the managed-cert host set to
    #    lowercase, and the backend must canonicalize the rule host to match it
    #    (regression: a mixed-case host once failed with "not an ingress host").
    cert_file = "e2e/scenarios/certs/ingress-byo.crt"
    key_file = "e2e/scenarios/certs/ingress-byo.key"
    work = temp_dir()
    config = work + "/config.yaml"
    spec = work + "/native-byo.yaml"
    write_file(path = config, content = """current-context: native-byo
contexts:
  native-byo:
    server: http://%s
    conduit:
      ingress:
        mode: native
        certificates:
          - certificate: %s
            key: %s
""" % (addr, cert_file, key_file))
    write_file(path = spec, content = """name: native-byo
image: alpine:3.20
entrypoint: [sleep]
command: ["3600"]
ports:
  - host: 0
    container: 80
ingress:
  enabled: true
  hosts: [App.Native-Cert.Example.Test]
  tls: {}
""")
    client_env = {"CORNUS_CONFIG": config}
    cornus("deploy", "-f", spec, "-d", env = client_env)
    st = wait(name = "native-byo", running = 1, timeout = "240s")
    assert_eq(st["running"], 1, "native BYO certificate workload never became ready")

    secret = kubectl("-n", NS, "get", "secret", "-l", "cornus.app=native-byo,cornus.ingress-tls=true",
                     "-o", "jsonpath={.items[0].metadata.name}")
    assert_true(secret != "", "native ingress must create a managed TLS Secret")
    secret_type = kubectl("-n", NS, "get", "secret", secret, "-o", "jsonpath={.type}")
    assert_eq(secret_type, "kubernetes.io/tls", "managed ingress Secret has the wrong type")
    ingress_secret = kubectl("-n", NS, "get", "ingress", "native-byo", "-o",
                             "jsonpath={.spec.tls[0].secretName}")
    assert_eq(ingress_secret, secret, "Ingress TLS entry must reference the managed Secret")
    tls_host = kubectl("-n", NS, "get", "ingress", "native-byo", "-o",
                       "jsonpath={.spec.tls[0].hosts[0]}")
    assert_eq(tls_host, "app.native-cert.example.test", "managed TLS host must match the ingress rule")
    rule_host = kubectl("-n", NS, "get", "ingress", "native-byo", "-o",
                        "jsonpath={.spec.rules[0].host}")
    assert_eq(rule_host, "app.native-cert.example.test",
              "the mixed-case ingress host must be canonicalized to lowercase (got %r)" % rule_host)

    expected_crt = sh("base64 -w 0 " + cert_file)["output"]
    expected_key = sh("base64 -w 0 " + key_file)["output"]
    actual_crt = kubectl("-n", NS, "get", "secret", secret, "-o", "jsonpath={.data.tls\\.crt}")
    actual_key = kubectl("-n", NS, "get", "secret", secret, "-o", "jsonpath={.data.tls\\.key}")
    assert_eq(actual_crt, expected_crt, "managed Secret must contain the selected certificate")
    assert_eq(actual_key, expected_key, "managed Secret must contain the selected private key")
    log("✓ native ingress created and wired an owned TLS Secret from the SAN-derived BYO rule")

    cornus("deploy", "-f", spec, "--delete", env = client_env)
    left = kubectl("-n", NS, "get", "secret", secret, "--ignore-not-found", "-o", "name")
    assert_eq(left, "", "managed TLS Secret must be garbage-collected with its Deployment")

    # 5. Owner-ref GC: removing the Deployment must cascade-delete the Ingress (it
    #    carries the Deployment as its owner reference). cornus delete is a
    #    foreground-propagation Deployment delete, so the real cluster's GC
    #    controller reaps the Ingress with it.
    remove(name = "shop")
    gone = ""
    for _ in range(30):
        gone = kubectl("-n", NS, "get", "ingress", "shop", "--ignore-not-found", "-o", "name")
        if gone == "":
            break
        sleep("2s")
    assert_eq(gone, "", "Ingress must be GC'd with its owning Deployment (still present: %r)" % gone)
    log("✓ Ingress garbage-collected with the Deployment on remove")

    remove(name = "api")

    # 6. Fetch THROUGH the controller.
    #
    # Everything above reads the generated object back with `kubectl -o
    # jsonpath`, which proves the fields are SPELLED right but not that they are
    # SERVED: an Ingress can be perfectly shaped and still route nothing (an
    # ingressClassName no controller claims, a backend port that names no Service
    # port, a host the rule never matches). The only way to tell those apart is to
    # ask the controller for the page.
    #
    # The workload is a real HTTP server this time, and the request is made at the
    # AUTO-DERIVED host from section 1 — so this also closes the loop on host
    # derivation: `edge.preview.example.test` is not merely the string in
    # spec.rules[0].host, it is the name that actually routes.
    deploy(
        name = EDGE,
        image = "traefik/whoami:v1.10.1",
        ports = ["8080:80"],
        ingress = {},  # defaults again: derived host + the server-default class
    )
    st = wait(name = EDGE, running = 1, timeout = "240s")
    assert_eq(st["running"], 1, "edge pod never became ready")

    # Object assertions first, so a later failure of the fetch cannot be blamed on
    # the Ingress not having been created the way the earlier sections describe.
    ehost = kubectl("-n", NS, "get", "ingress", EDGE, "-o", "jsonpath={.spec.rules[0].host}")
    assert_eq(ehost, EDGE_HOST, "edge host must be auto-derived from the base domain (got %r)" % ehost)
    ecls = kubectl("-n", NS, "get", "ingress", EDGE, "-o", "jsonpath={.spec.ingressClassName}")
    assert_eq(ecls, "nginx", "edge ingressClassName must be the server default (got %r)" % ecls)
    eport = kubectl("-n", NS, "get", "ingress", EDGE, "-o",
                    "jsonpath={.spec.rules[0].http.paths[0].backend.service.port.number}")
    assert_eq(eport, "80", "edge backend port must be the container port (got %r)" % eport)

    base = controller_http_base()
    if base == "":
        log("• deploy-ingress: no ingress controller in this cluster — object asserted, " +
            "fetch skipped (E2E_INGRESS_NGINX=1 installs ingress-nginx and enables it)")
    else:
        log("dialling the installed controller at %s with Host: %s" % (base, EDGE_HOST))
        # Two distinct transient answers precede the real one, and they are not
        # both 5xx. Between the Ingress being admitted and its endpoints being
        # programmed the controller answers 503 (retry_5xx). BEFORE it has ingested
        # the Ingress at all, the host matches no rule and its DEFAULT backend
        # answers 404 — a status retry_5xx returns verbatim, which would fail this
        # assertion on controller sync latency rather than on whether the Ingress is
        # served. retry_until=200 treats every non-200 as transient until the
        # deadline and then returns the last one, so a route that never appears
        # still fails honestly with its real status.
        r = http_get(url = base + "/", host = EDGE_HOST, retry = "180s", retry_5xx = True, retry_until = 200)
        assert_eq(r["status"], 200,
                  "the generated Ingress must actually be SERVED by the controller (got %r)" % r["status"])
        # whoami echoes the request line, so this separates "the controller
        # answered" from "the controller reached the workload".
        assert_contains(r["body"], "GET / HTTP",
                        "the request must reach the workload behind the Ingress, not the default backend")
        log("✓ fetched %s through the real controller: the derived host, class and backend port route" % EDGE_HOST)

    remove(name = EDGE)
