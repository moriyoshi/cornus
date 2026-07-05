# FTP server, end to end through cornus's published ports, proving BOTH
# directions of a passive-mode transfer AND the separate FTP data channel:
# build a Go FTP server (github.com/fclairamb/ftpserverlib), deploy it publishing
# the control port (21) and a FIXED passive-data port (30000), then run a
# passive-mode round-trip that uploads a payload (STOR, client->server) and
# downloads it back (RETR, server->client) and asserts byte-equality. Docker-only:
# it publishes host ports and drives them from the test host (the passive port must
# be pinned and published, which busybox ftpd cannot do — hence ftpserverlib).

# rt polls ftp_roundtrip a few times: a freshly-published port often accepts the
# TCP connection before the server is actually serving, so the first attempt right
# after the deploy reports "running" can be refused/reset. Retry transient
# failures, then return the last result for the scenario to assert on.
def rt(payload, steps = 20):
    r = None
    for _ in range(steps):
        r = ftp_roundtrip(
            addr = "127.0.0.1:12100",
            user = "cornus",
            password = "secret",
            path = "rt.dat",
            content = payload,
        )
        if r["ok"]:
            return r
        sleep(duration = "1s")
    return r

if TARGET != "docker":
    log("ftp: skipped (docker-only; publishes host control + passive-data ports)")
else:
    serve()

    image = build(name = "ftp", context = "e2e/scenarios/ftp")

    deploy(
        name = "ftp",
        image = image,
        ports = ["12100:21", "30000:30000"],
        env = {"FTP_PASV_ADDRESS": "127.0.0.1"},
    )
    wait(name = "ftp", running = 1, timeout = "180s")

    # A non-trivial ~2KB payload with varied bytes (not just ASCII text) so a
    # truncating/one-directional transport cannot pass by accident.
    payload = ""
    for i in range(256):
        payload += "cornus-ftp-%d-%s|" % (i, "".join([chr(33 + (i + j) % 94) for j in range(5)]))

    r = rt(payload)
    assert_true(r["ok"], "ftp roundtrip failed: " + r["error"])
    assert_eq(r["downloaded"], payload, "downloaded != uploaded — bidirectional data mismatch")
    log("✓ FTP passive-mode round-trip: STOR + RETR of %d bytes matched (control + separate passive data channel verified)" % r["n"])

    remove(name = "ftp")
    log("torn down")
