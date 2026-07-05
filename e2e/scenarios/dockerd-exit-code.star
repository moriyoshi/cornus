# `docker wait` exit-code propagation through the Docker Engine API proxy, against
# a REAL backend. The code is threaded api.DeployStatus.TerminalExitCode ->
# deploywire.Event.ExitCode -> pkg/dockerproxy wait/inspect and is unit-tested at
# every hop with a fake attacher — but a fake cannot reach the one thing that only
# exists here: the proxy's awaitExit racing a POLL of real backend statuses against
# the deploy-attach session's Done(). This scenario drives both arms of that race
# with a real dockerhost workload and asserts what `docker wait` actually prints.
#
#   - POLL arm (a container that exits on its own while the session is still held):
#     awaitExit's ticker reads the backend status, TerminalExitCode resolves, and
#     `docker wait` prints the REAL code. Covered here for a clean exit (0).
#   - Done() arm (an explicitly stopped container): cornus tears the whole
#     deployment down, so no backend ever observes a termination. That is the
#     "unknown" contract in pkg/dockerproxy/containers.go: it MUST answer
#     unknownExitCode (125) plus an Error, and MUST NOT fabricate a 0 — reporting
#     an unknown outcome as success is precisely the bug the exit-code work fixed.
#   - Both arms are then re-asked: a repeat wait must return the SAME answer off
#     rec.setExitCode/lastExitCode, not re-derive a different one.
#
# NOT covered here: a NON-ZERO self-exit. It is unreachable through the proxy today
# — see dockerd-exit-code-nonzero.star, the known-failing reproducer, and the
# restart-policy translation defect it documents.
#
# Docker-only: it needs a backend that actually reports InstanceStatus.ExitCode
# (dockerhost does; barehost/incushost never fill it) and a real `docker` CLI
# pointed at the proxy socket.

CLEAN = "wexit0"    # exits 0 on its own -> the poll arm
UNKNOWN = "wexitunk"  # torn down while running -> the Done() arm

if TARGET != "docker":
    log("dockerd-exit-code: skipped (docker-only; needs a backend that reports exit codes + a real docker CLI)")
else:
    # Up-front cleanup (Starlark has no defer, so end-of-scenario cleanup does not
    # run on failure): the Docker daemon is shared across runs, and a container a
    # failed run left behind carries its own exit state into these assertions.
    sh(cmd = "docker rm -f $(docker ps -aq -f label=cornus.app=%s) 2>/dev/null; true" % CLEAN)
    sh(cmd = "docker rm -f $(docker ps -aq -f label=cornus.app=%s) 2>/dev/null; true" % UNKNOWN)

    serve()
    host = dockerd_up()
    D = "docker -H '" + host + "' "
    sock = host[len("unix://"):]
    have_curl = sh(cmd = "command -v curl >/dev/null 2>&1")["code"] == 0

    # --- POLL arm: a container that exits 0 on its own -----------------------
    #
    # `--restart=on-failure` is deliberate, not decoration: it is the ONLY policy
    # expressible through the proxy under which a self-exit stays exited (a plain
    # `docker run` currently lands on cornus's "unless-stopped" default and
    # restart-loops forever — see dockerd-exit-code-nonzero.star). With it, a clean
    # exit is terminal, the backend reports ExitCode 0, and the proxy's poll sees it.
    docker("-H", host, "run", "-d", "--restart=on-failure", "--name", CLEAN, "alpine:3.20", "sh", "-c", "sleep 2; exit 0")
    r = sh(cmd = "timeout -k 5 90 " + D + "wait " + CLEAN + " 2>&1")
    assert_eq(r["code"], 0, "`docker wait %s` did not complete (rc %d): %r" % (CLEAN, r["code"], r["output"]))
    assert_eq(r["output"], "0", "`docker wait` must print the real exit code 0, got %r" % r["output"])
    log("✓ `docker wait` printed the real exit code 0 for a self-exited container (poll arm)")

    # The raw API body must carry StatusCode 0 with NO Error: a clean exit is a
    # KNOWN outcome, and the unknown-encoding must not leak into it.
    if have_curl:
        raw = sh(cmd = "timeout -k 5 60 curl -s --unix-socket " + sock + " -XPOST http://x/containers/" + CLEAN + "/wait")
        assert_contains(raw["output"], "\"StatusCode\":0", "raw wait body must report StatusCode 0, got %r" % raw["output"])
        assert_true("Error" not in raw["output"], "a KNOWN clean exit must carry no Error, got %r" % raw["output"])
        log("✓ raw wait body for the clean exit: %s" % raw["output"])
    else:
        log("! curl absent: skipping the raw wait-body assertions (the docker CLI assertions still ran)")

    # Re-asking must give the same answer off the remembered code, not re-derive one.
    r = sh(cmd = "timeout -k 5 60 " + D + "wait " + CLEAN + " 2>&1")
    assert_eq(r["output"], "0", "a repeat `docker wait` must report the same code 0, got %r" % r["output"])
    log("✓ repeat `docker wait` reported the same code (remembered, not re-derived)")

    # --- Done() arm: an explicitly stopped container -------------------------
    #
    # A `docker stop` through the proxy tears the DEPLOYMENT down, so the workload
    # never terminates in a way any backend observes: the session's Done() fires
    # with nothing to report. Docker would say 137 here; cornus refuses to invent
    # that, and equally refuses to invent 0.
    docker("-H", host, "run", "-d", "--name", UNKNOWN, "alpine:3.20", "sleep", "infinity")
    wait(name = UNKNOWN, running = 1, timeout = "180s")

    # The wait must be BLOCKED before the stop, then answer because of it — so run
    # it in the background and issue the stop underneath (Starlark is
    # single-threaded, so the shell is the only way to overlap the two).
    r = sh(cmd = "( timeout -k 5 90 " + D + "wait " + UNKNOWN + " 2>&1; echo RC=$? ) & " +
                 "sleep 3; timeout -k 5 60 " + D + "stop " + UNKNOWN + " >/dev/null 2>&1; wait")
    assert_contains(r["output"], "RC=0", "the blocked `docker wait` did not complete after the stop: %r" % r["output"])
    got = r["output"].replace("RC=0", "").strip()
    assert_true(got != "0", "an UNKNOWN exit status must never be reported as 0 — that is the exact bug this guards (got %r)" % got)
    assert_contains(got, "125", "an unknown exit status must surface as 125, got %r" % got)
    log("✓ a container torn down while running reported 125, never 0 (Done() arm)")

    # The raw body is where the contract is fully visible: 125 AND an Error saying
    # the status is unknown. The docker CLI collapses that to the bare code.
    if have_curl:
        raw = sh(cmd = "timeout -k 5 60 curl -s --unix-socket " + sock + " -XPOST http://x/containers/" + UNKNOWN + "/wait")
        assert_contains(raw["output"], "\"StatusCode\":125", "unknown wait body must report 125, got %r" % raw["output"])
        assert_contains(raw["output"], "the exit status is unknown, not 0", "unknown wait body must explain itself, got %r" % raw["output"])
        log("✓ raw wait body for the unknown exit: %s" % raw["output"])

    sh(cmd = "timeout -k 5 60 " + D + "rm -f " + CLEAN + " " + UNKNOWN + " >/dev/null 2>&1; true")
    log("✓ dockerd-exit-code done")
