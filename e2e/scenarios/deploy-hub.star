# Workload-to-workload hub overlay on kubernetes. Two workloads join the overlay:
# `hubsrv` EXPORTS an HTTP service "greeter" (delivered — the hub relays inbound to
# the pod, which dials it on localhost); `hubcli` IMPORTS "greeter". cornus injects
# a caretaker into each: hubsrv registers the service, hubcli gets a synthetic-IP DNS
# record for "greeter" plus a Reach listener on it, so the app's plain dial of
# `greeter` funnels through the cornus server hub to hubsrv. We exec into hubcli and
# curl `greeter` to prove the end-to-end overlay path.
#
# kube-only. Needs the cornus:e2e sidecar image the kube target already loads, and
# CORNUS_ADVERTISE_URL set (the in-cluster hub URL the caretaker dials) — the harness
# provides it for the sidecar mount/hub paths.

if TARGET != "kube":
    log("deploy-hub: skipped (kube-only; workload-to-workload hub overlay)")
else:
    serve()

    # Exporter: a tiny HTTP server on :8080, offered to the overlay as "greeter"
    # via delivery (the hub relays to this pod, which dials 127.0.0.1:8080).
    deploy(
        name = "hubsrv",
        image = "hashicorp/http-echo:1.0",
        # http-echo's ENTRYPOINT is already `/http-echo`; command supplies only
        # its flags (a leading `/http-echo` would run `/http-echo /http-echo …`).
        command = ["-listen=:8080", "-text=HELLO-FROM-HUB"],
        hub_identity = "hubsrv",
        hub_export = ["greeter=8080:deliver"],
    )
    wait(name = "hubsrv", running = 1, timeout = "240s")
    log("✓ exporter Running (caretaker registered greeter on the hub)")

    # Importer: reaches "greeter" through the hub. The caretaker binds a synthetic-IP
    # loopback listener for it and answers DNS for the name, so `curl greeter` routes
    # into the overlay.
    deploy(
        name = "hubcli",
        image = "curlimages/curl:8.10.1",
        # curlimages/curl's ENTRYPOINT runs curl; override it with sleep so the
        # importer idles (pod_exec still invokes curl below to reach the hub).
        entrypoint = ["sleep"],
        command = ["3600"],
        hub_identity = "hubcli",
        hub_import = ["greeter=8080"],
    )
    wait(name = "hubcli", running = 1, timeout = "240s")
    log("✓ importer Running (caretaker synthetic-IP DNS + Reach listener for greeter)")

    # Curl the peer by name from inside the importer's app container. Retry to let
    # both caretakers connect + register on the hub.
    got = ""
    for _ in range(20):
        got = pod_exec(app = "hubcli", cmd = "curl -s --max-time 3 http://greeter:8080/ || echo PENDING")
        if "HELLO-FROM-HUB" in got:
            break
        sleep(duration = "3s")
    assert_contains(got, "HELLO-FROM-HUB", "importer must reach the exporter through the hub (curl greeter), got %r" % got)
    log("✓ workload-to-workload traffic proven through the cornus hub overlay")
