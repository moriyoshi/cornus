# Workload-to-workload hub overlay over UDP. Same shape as deploy-hub.star but the
# exported service speaks UDP: `hubsrv` runs a UDP echo (socat) offered to the
# overlay as "udpecho" via delivery (the hub relays inbound to the pod, which dials
# it on localhost); `hubcli` IMPORTS "udpecho" over UDP.
#
# UDP has datagram boundaries but the hub relay is a byte stream, so datagrams are
# length-prefix framed (a 2-byte big-endian length + payload, pkg/wire
# WriteDatagram/ReadDatagram) over the byte-agnostic relay. The importing caretaker
# binds a UDP Reach listener on the peer's synthetic IP and keeps a per-source flow
# so replies route back to the right sender; the exporting caretaker bridges the
# framed stream to a connected UDP socket at the far end. We exec into hubcli and
# send a datagram with `nc -u`, proving the end-to-end UDP overlay path.
#
# kube-only. Needs the cornus:e2e sidecar image the kube target already loads, and
# CORNUS_ADVERTISE_URL set (the in-cluster hub URL the caretaker dials) — the
# harness provides it for the sidecar mount/hub paths.

if TARGET != "kube":
    log("deploy-hub-udp: skipped (kube-only; workload-to-workload UDP hub overlay)")
else:
    serve()

    # Exporter: a UDP echo on :9000 (socat forks a PIPE per datagram source and
    # echoes each datagram back to its sender), offered to the overlay as "udpecho"
    # via delivery (the hub relays to this pod, which dials 127.0.0.1:9000).
    deploy(
        name = "hubsrv",
        image = "alpine/socat:latest",
        # alpine/socat's ENTRYPOINT is already `socat`; command supplies only its
        # arguments (a leading `socat` would run `socat socat …`).
        command = ["-T", "5", "UDP4-RECVFROM:9000,fork,reuseaddr", "PIPE"],
        hub_identity = "hubsrv",
        hub_export = ["udpecho=9000/udp:deliver"],
    )
    wait(name = "hubsrv", running = 1, timeout = "240s")
    log("✓ UDP exporter Running (caretaker registered udpecho on the hub)")

    # Importer: reaches "udpecho" through the hub over UDP. The caretaker binds a
    # synthetic-IP UDP Reach listener for it and answers DNS for the name, so a
    # plain `nc -u udpecho 9000` routes into the overlay.
    deploy(
        name = "hubcli",
        image = "busybox:1.36",
        command = ["sleep", "3600"],
        hub_identity = "hubcli",
        hub_import = ["udpecho=9000/udp"],
    )
    wait(name = "hubcli", running = 1, timeout = "240s")
    log("✓ UDP importer Running (caretaker synthetic-IP DNS + UDP Reach listener)")

    # Send a datagram by name from inside the importer's app container and read the
    # echo. Retry to let both caretakers connect + register on the hub. busybox nc
    # supports -u (UDP); -w2 bounds the wait for the reply.
    got = ""
    for _ in range(20):
        got = pod_exec(app = "hubcli", cmd = "echo -n HELLO-UDP-HUB | timeout 3 nc -u -w2 udpecho 9000 || echo PENDING")
        if "HELLO-UDP-HUB" in got:
            break
        sleep(duration = "3s")
    assert_contains(got, "HELLO-UDP-HUB", "importer must reach the UDP exporter through the hub, got %r" % got)
    log("✓ workload-to-workload UDP traffic proven through the cornus hub overlay")
