# Automatic ingress creation on the kubernetes backend. This scenario proves the
# Kubernetes-only ingress feature end to end against a real cluster: that a deploy
# opting into ingress produces a networking.k8s.io/v1 Ingress with the expected
# host (auto-derived from CORNUS_INGRESS_DOMAIN or given explicitly), backend
# Service + port, ingress class, and cert-manager TLS; and that the Ingress is
# garbage-collected with the Deployment on remove (owner-ref cascade). kube-only
# (needs a real cluster with a working GC controller); other targets skip.

NS = "cornus-e2e"  # KubeTarget's default namespace
DOMAIN = "preview.example.test"

if TARGET != "kube":
    log("deploy-ingress: skipped (kube-only; asserts the generated Ingress object)")
else:
    # Configure the server's ingress defaults: a base wildcard domain for host
    # auto-derivation, a default IngressClass, and a default cert-manager issuer.
    # These are read by the kubernetes backend at construction (serve boots it).
    serve(env = {
        "CORNUS_INGRESS_DOMAIN": DOMAIN,
        "CORNUS_INGRESS_CLASS": "nginx",
        "CORNUS_INGRESS_TLS_ISSUER": "letsencrypt-test",
    })

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

    # 4. Owner-ref GC: removing the Deployment must cascade-delete the Ingress (it
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
