# Port-forward across the rootful/rootless podman divide, asserted as a CONTRAST.
#
# Rootless podman keeps a workload's network namespace behind pasta/slirp4netns,
# so a cornus running as a HOST process cannot route to the container's address.
# That is what rootless means; it is not a misconfiguration. Measured on Podman
# 5.8.2 with netavark, against one workload at 10.89.0.2:
#
#	from the rootless host netns      -> TimeoutError
#	from a container on that network  -> reachable
#
# Cornus therefore refuses the forward up front, naming CORNUS_PODMAN_REMOTE,
# rather than dialing into a timeout. The timeout is the failure mode worth
# preventing: it reads as "the workload is down" and sends the operator into
# their own application, where there is nothing to find.
#
# WHY THIS IS ONE SCENARIO AND NOT TWO. A refusal is only correct if it fires on
# exactly the topology that cannot work. The first version of this check refused
# on `rootless && !remote` alone, which also turned away forwards that DO work —
# and no test that looked only at the rootless leg could see that, because the
# refusal it asserted was present and correctly worded. So both legs assert here,
# against the same deployment and the same command:
#
#	TARGET == "podman"           -> the forward must SUCCEED
#	TARGET == "podman-rootless"  -> the forward must be REFUSED, by name
#
# Removing the refusal breaks the second leg; over-broadening it breaks the
# first. Neither leg alone pins the behavior.
#
# WHERE THE REFUSAL IS ASSERTED, and why it is not the CLI's output. Measured:
# the refusal never reaches the client. A TCP port-forward is a raw passthrough
# with no post-preamble error channel (pkg/server/deploy_exec.go), so ANY setup
# failure — this one, a kube RBAC denial, a missing pod — manifests to the CLI
# only as the tunnel closing, and the diagnostic is logged server-side. That is
# the designed behavior for every backend, not a podman gap, so this asserts on
# server_log(): the stream the message is actually written to.
#
# The consequence is worth stating plainly, because the assertion below can look
# weaker than it is: an operator running `cornus port-forward` against a remote
# server sees a dead tunnel and no remedy. The refusal still beats the
# alternative — the cause is recorded rather than lost in a dial timeout — but
# the message reaches whoever reads the server log, not whoever ran the command.

if TARGET != "podman" and TARGET != "podman-rootless":
    log("deploy-portforward-rootless-podman: skipped (podman-only; the rootless divide has no analogue on the other backends)")
else:
    rootless = TARGET == "podman-rootless"
    addr = serve()

    # No published ports: reaching :80 has to go through the forward, so the
    # result is about the forward path and not about a host port mapping that
    # would work on either daemon.
    deploy(name = "pfrl", image = "nginx:alpine")
    wait(name = "pfrl", running = 1, timeout = "240s")

    local_port = free_port()
    # ONE temp dir, bound once: temp_dir() mints a fresh directory per call, so
    # calling it again for the pidfile would write the pid somewhere the kill
    # never looks — leaving the forward running past the scenario.
    work = temp_dir()
    logfile = work + "/portforward.log"
    pidfile = work + "/portforward.pid"

    # Start the forward and drive ONE connection through it: the backend's
    # ForwardPort runs per accepted connection, so without a connection there is
    # nothing to refuse and the log would be empty for the right reason.
    #
    # curl's own result is recorded but is NOT the assertion. On the rootless leg
    # a refusal and a dial timeout produce the same thing here — a closed tunnel
    # — which is precisely why the check has to read the server's log.
    r = sh(cmd = " ".join([
        CORNUS_BIN, "port-forward", "--server", "http://" + addr,
        "pfrl", local_port + ":80", ">", logfile, "2>&1", "&",
        "echo $! > " + pidfile + ";",
        "sleep 3;",
        "curl -s -o /dev/null -w '%{http_code}' -m 20 http://127.0.0.1:" + local_port + "/ || echo CURL_FAILED;",
        "kill $(cat " + pidfile + ") 2>/dev/null || true;",
        "sleep 1;",
        "cat " + logfile,
    ]))
    client_out = r["output"]
    out = server_log()

    if rootless:
        # The headline: the operator is told which knob turns this on. Asserting
        # the variable NAME rather than the sentence is deliberate — the wording
        # may be improved, but an error that does not name a remedy has failed at
        # the only job it has.
        assert_contains(
            out, "CORNUS_PODMAN_REMOTE",
            "a host-run cornus on a ROOTLESS podman must refuse the forward and name the remedy, " +
            "not dial into a timeout that reads as a dead workload.\nserver log: %r\nclient: %r" % (out, client_out),
        )
        # ...and it must be a refusal, not the timeout wearing a hint. A dial that
        # was attempted and expired would have produced curl's own failure after
        # the full 20s with nothing from the server.
        assert_contains(
            out, "rootless",
            "the refusal must say WHY the forward cannot work, so the operator does not " +
            "go looking for a misconfiguration that is not there.\nserver log: %r" % out,
        )
        log("✓ rootless podman: the forward is refused up front, naming CORNUS_PODMAN_REMOTE")
    else:
        # The counterweight. Rootful podman routes to the container from the host,
        # so the identical command must work — this is the leg that catches a
        # refusal broadened past the topology it belongs to.
        # Success is client-visible, so this half asserts on curl's status.
        assert_contains(
            client_out, "200",
            "on ROOTFUL podman the same forward must SUCCEED (the host can route to the " +
            "container); a failure here means the rootless refusal is firing where it must not: %r" % client_out,
        )
        assert_true(
            "CORNUS_PODMAN_REMOTE" not in out,
            "rootful podman must not be told to configure remote mode — that refusal belongs " +
            "to the rootless topology only.\nserver log: %r" % out,
        )
        log("✓ rootful podman: the same forward succeeds, so the refusal is scoped to rootless")

    remove(name = "pfrl")
