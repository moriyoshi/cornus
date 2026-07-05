# Client-sourced credentials delivered into a container via the caretaker sidecar.
#
# A credential is MINTED ON THE CLIENT (here the zero-dependency `static` source),
# relayed through the cornus server, and surfaced inside the pod by the caretaker
# credential role two provider-agnostic ways at once:
#   - a generic HTTP endpoint (cornus-native JSON), advertised to the app as
#     $CORNUS_CREDENTIALS_URL;
#   - a file drop at /creds/db.json in a shared volume.
# We read BOTH back from inside the running container to prove the whole
# client -> server -> sidecar -> container path landed the secret — which never
# appears in the pod spec in plaintext.
#
# Kube-only: delivery uses the caretaker sidecar (like the 9P mount scenarios).
# The app image is plain busybox; the caretaker runs the cornus sidecar image.
#
# Source of truth: pkg/credential (client source), pkg/creddelivery (delivery),
# pkg/deploywire + pkg/server (relay), pkg/caretaker (credential role),
# pkg/deploy/kubernetes (injectCredentials).

creds = '[{"name":"db","backend":"static",' + \
        '"config":{"username":"cornus-user","password":"s3cr3t-value"},' + \
        '"deliver":[{"kind":"endpoint","provider":"generic"},' + \
        '{"kind":"file","path":"/creds/db.json","format":"json"}]}]'

def read_until(app, cmd, want, steps = 30):
    # The startup probe gates the app until delivery is live, so this should
    # succeed on the first try; retry a little to absorb pod-churn races.
    for _ in range(steps):
        out = pod_exec(app = app, cmd = cmd)
        if want in out:
            return out
        sleep(duration = "2s")
    fail(msg = "%r never contained %r (last: %r)" % (cmd, want, out))

if TARGET != "kube":
    log("credentials: skipped (kube-only; delivery uses the caretaker sidecar)")
else:
    serve()

    # busybox app kept alive with sleep; the caretaker (cornus:e2e) delivers the
    # credential from the client-held session for the app's lifetime.
    deploy_attach(
        name = "creds-app",
        image = "busybox:1.36",
        command = ["sleep", "3600"],
        credentials_json = creds,
        timeout = "240s",
    )
    wait(name = "creds-app", running = 1, timeout = "240s")
    log("✓ credential workload is up (caretaker delivered the secret)")

    # 1) Generic HTTP endpoint: the neutral {values, expiration} JSON.
    body = read_until("creds-app", "wget -qO- $CORNUS_CREDENTIALS_URL", "cornus-user")
    assert_contains(body, "s3cr3t-value", "generic endpoint served the credential value")
    log("✓ reached the generic credential endpoint at $CORNUS_CREDENTIALS_URL")

    # 2) File drop: the same credential materialized to a shared-volume file.
    filed = read_until("creds-app", "cat /creds/db.json", "s3cr3t-value")
    assert_contains(filed, "cornus-user", "file drop materialized the credential")
    log("✓ read the credential file at /creds/db.json inside the pod")

    attach_stop(name = "creds-app")
    log("✓ disconnect tore the credential workload down")
