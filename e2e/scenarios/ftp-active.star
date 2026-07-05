# ACTIVE-mode FTP, end to end through cornus, proving the SERVER->CLIENT
# connect-back direction of the FTP data channel (the mirror of ftp.star's
# passive round-trip). Build the same Go FTP server (github.com/fclairamb/
# ftpserverlib; active mode is enabled on it), deploy it publishing ONLY the
# control port (21), then run an active-mode round-trip: for each transfer the
# test host opens a data listener and PORTs it, and the container SERVER dials
# back to that listener to move the bytes (STOR client->server, RETR
# server->client), and we assert byte-equality.
#
# Docker-only: it publishes a host control port and drives it from the test host.
#
# KEY DIFFERENCE from passive mode: the active data channel is the SERVER
# connecting BACK to the CLIENT, so it does NOT traverse any published port (there
# is no published passive-data port here at all). For the container to reach the
# host-run test client, we advertise the docker-bridge GATEWAY IP — the address a
# container uses to reach the host — as the PORT connect-back host. This is
# inherently environment-sensitive: it assumes the default `bridge` network's
# gateway routes container->host and that no firewall blocks the ephemeral
# connect-back port.

# rt polls ftp_roundtrip a few times: a freshly-published control port often
# accepts the TCP connection before the server is actually serving, so the first
# attempt right after the deploy reports "running" can be refused/reset. Retry
# transient failures, then return the last result for the scenario to assert on.
def rt(payload, gateway, steps = 20):
    r = None
    for _ in range(steps):
        r = ftp_roundtrip(
            addr = "127.0.0.1:12101",
            user = "cornus",
            password = "secret",
            path = "rt.dat",
            content = payload,
            active = True,
            advertise_host = gateway,
        )
        if r["ok"]:
            return r
        sleep(duration = "1s")
    return r

if TARGET != "docker":
    log("ftp-active: skipped (docker-only; active-mode connect-back from container to host client)")
else:
    serve()

    image = build(name = "ftp", context = "e2e/scenarios/ftp")

    # Publish ONLY the control port — active mode needs no published passive-data
    # port (the data channel is the server dialing back to the host client). Use a
    # DIFFERENT host port than ftp.star (12100) so both scenarios can run back to
    # back without a collision.
    deploy(
        name = "ftp-active",
        image = image,
        ports = ["12101:21"],
    )
    wait(name = "ftp-active", running = 1, timeout = "180s")

    # The docker-bridge gateway is the address the container reaches the host at;
    # the server dials it back for the active data channel. Strip whitespace from
    # the `docker network inspect` output.
    gateway = docker("network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}").strip()
    assert_true(gateway != "", "could not determine the docker bridge gateway IP")
    log("• active-mode connect-back host (docker bridge gateway): %s" % gateway)

    # A non-trivial ~2KB payload with varied bytes (not just ASCII text) so a
    # truncating/one-directional transport cannot pass by accident.
    payload = ""
    for i in range(256):
        payload += "cornus-ftp-%d-%s|" % (i, "".join([chr(33 + (i + j) % 94) for j in range(5)]))

    r = rt(payload, gateway)
    assert_true(r["ok"], "ftp active-mode roundtrip failed: " + r["error"])
    assert_eq(r["downloaded"], payload, "downloaded != uploaded — bidirectional data mismatch")
    log("✓ FTP active-mode round-trip: STOR + RETR of %d bytes matched (server->client connect-back verified)" % r["n"])

    remove(name = "ftp-active")
    log("torn down")
