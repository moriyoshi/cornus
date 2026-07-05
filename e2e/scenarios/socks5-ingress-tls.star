# Emulate-mode ingress-via-conduit with TLS TERMINATION: an `x-cornus-ingress` with
# a `tls: {}` block makes the client-side emulated ingress serve HTTPS on :443 with a
# user-provided certificate selected from its DNS SAN and reverse-proxy the plaintext
# request to the workload over the conduit.
#
# The host `app.native-cert.example.test` bears no `.cornus.internal` suffix, so only the
# emulated ingress routes it. We reach it over https THROUGH the socks5 proxy
# while explicitly trusting the fixture certificate; a 200 proves the configured
# certificate was selected, terminated the handshake, and proxied to the backend.
# docker + kube.
#
# Source of truth: pkg/ingressemu (Serve TLS branch + cert.go), pkg/clientconduit
# (socks5Conduit.addEmulatedIngress), cmd/cornus/internal/composecli.

compose_file = "e2e/scenarios/socks5-ingress-tls.yaml"
cert_file = "e2e/scenarios/certs/ingress-byo.crt"
key_file = "e2e/scenarios/certs/ingress-byo.key"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed" % name)

# Emulate mode is realized ENTIRELY client-side (the reverse proxy on :443 through
# the conduit), so the kubernetes backend must create NO real cluster Ingress object
# for it — an `x-cornus-ingress` marked client-emulated is skipped by the deploy
# backend (api.IngressSpec.ClientEmulated, set for emulate mode; see
# ingressEnabled / applyDependents). This is asserted below on the kube target. It
# also means the server needs neither an ingress issuer nor ingress RBAC for an
# emulate deploy — a real Ingress here previously required a CORNUS_INGRESS_TLS_ISSUER
# workaround AND `networking.k8s.io/ingresses` RBAC the emulate workflow never wanted.
serve()

port = free_port()
proxy = "127.0.0.1:" + port
data = temp_dir()

config = data + "/config.yaml"
write_file(path = config, content = """current-context: byo
contexts:
  byo:
    conduit:
      ingress:
        certificates:
          - certificate: %s
            key: %s
""" % (cert_file, key_file))
handle = compose_up_bg(
    file = compose_file,
    conduit = "socks5://" + proxy,
    env = {"CORNUS_CONFIG": config, "CORNUS_INGRESS_CONDUIT": "emulate", "XDG_DATA_HOME": data},
)
wait(name = "e2e-web", running = 1, timeout = "180s")
log("✓ compose up is holding a socks5 proxy with TLS-terminating ingress emulation on %s" % proxy)

# https through the proxy: proxy resolves app.native-cert.example.test:443 -> the emulated
# ingress's TLS server (KindLocal) -> reverse proxy -> nginx.
resp = http_get(url = "https://app.native-cert.example.test/", socks5 = proxy, ca_file = cert_file, retry = "30s")
assert_eq(resp["status"], 200, "https ingress host not reachable through the emulated ingress")
assert_contains(resp["body"], "nginx", "https ingress did not reach the web service")
log("✓ emulated ingress selected and served the SAN-derived BYO certificate")

# Server-side regression guard (kube): an EMULATED ingress must NOT create a real
# cluster Ingress object — it is fully client-side. Before ClientEmulated, the kube
# backend created one, which needed an ingress issuer to validate and (crucially)
# `networking.k8s.io/ingresses` RBAC the emulate workflow never wanted — so a
# restrictive-RBAC server (the Helm/k8s Role) failed the whole `up` at deploy. This
# assertion fails if the backend ever creates a real Ingress for an emulate deploy
# again. (docker has no Ingress objects; guard on the kube target.)
if TARGET == "kube":
    ing = kubectl("-n", "cornus-e2e", "get", "ingress", "e2e-web", "--ignore-not-found", "-o", "jsonpath={.metadata.name}")
    assert_eq(ing.strip(), "", "emulate mode must NOT create a real cluster Ingress, found %r" % ing)
    log("✓ emulate mode created no real cluster Ingress (client-side only)")

compose_down(file = compose_file)
res = compose_up_wait(handle = handle, timeout = "60s")
assert_eq(res["code"], 0, "backgrounded `up` should exit cleanly once its services are removed")
wait_gone("e2e-web")
log("✓ down withdrew the ingress registrations and the proxy exited with the session")
