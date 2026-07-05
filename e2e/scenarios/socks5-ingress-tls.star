# Emulate-mode ingress-via-conduit with TLS TERMINATION: an `x-cornus-ingress` with
# a `tls: {}` block makes the client-side emulated ingress serve HTTPS on :443 with a
# locally-generated certificate (signed by a persisted session CA) and reverse-proxy
# the plaintext request to the workload over the conduit.
#
# The host `secure.example.com` bears no `.cornus.internal` suffix, so only the
# emulated ingress routes it. We reach it over https THROUGH the socks5 proxy
# (insecure=True skips verification of the generated cert — cert validity itself is
# unit-tested in pkg/ingressemu); a 200 with the nginx body proves the :443 TLS server
# terminated the handshake and proxied to the backend. XDG_DATA_HOME is pinned to a
# temp dir so the generated CA never touches the runner's home. docker + kube.
#
# Source of truth: pkg/ingressemu (Serve TLS branch + cert.go), pkg/clientconduit
# (socks5Conduit.addEmulatedIngress), cmd/cornus/internal/composecli.

compose_file = "e2e/scenarios/socks5-ingress-tls.yaml"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed" % name)

# The kubernetes backend realizes the x-cornus-ingress as a real Ingress next to
# the workload, and a `tls: {}` block needs a secretName or cluster-issuer to
# validate — so give the server a default issuer (dockerhost ignores ingress
# entirely, making this a harmless no-op there). This is orthogonal to what the
# scenario tests: the client-side EMULATED TLS termination (a generated cert on
# :443 through the conduit) is independent of the backend's real Ingress.
serve(env = {"CORNUS_INGRESS_TLS_ISSUER": "letsencrypt-test"})

port = free_port()
proxy = "127.0.0.1:" + port
data = temp_dir()

handle = compose_up_bg(
    file = compose_file,
    conduit = "socks5://" + proxy,
    env = {"CORNUS_INGRESS_CONDUIT": "emulate", "XDG_DATA_HOME": data},
)
wait(name = "e2e-web", running = 1, timeout = "180s")
log("✓ compose up is holding a socks5 proxy with TLS-terminating ingress emulation on %s" % proxy)

# https through the proxy: proxy resolves secure.example.com:443 -> the emulated
# ingress's TLS server (KindLocal) -> reverse proxy -> nginx.
resp = http_get(url = "https://secure.example.com/", socks5 = proxy, insecure = True, retry = "30s")
assert_eq(resp["status"], 200, "https ingress host not reachable through the emulated ingress")
assert_contains(resp["body"], "nginx", "https ingress did not reach the web service")
log("✓ reached the web service over https (emulated TLS termination) through the conduit")

compose_down(file = compose_file)
res = compose_up_wait(handle = handle, timeout = "60s")
assert_eq(res["code"], 0, "backgrounded `up` should exit cleanly once its services are removed")
wait_gone("e2e-web")
log("✓ down withdrew the ingress registrations and the proxy exited with the session")
