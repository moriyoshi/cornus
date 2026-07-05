# Prove depends_on with condition: service_completed_successfully gates a
# service on a one-shot dependency FINISHING (exit 0), and that the gate does not
# stall. `migrate` prints a line and exits 0 (restart: "no", so it stays exited
# instead of being restarted and re-blocking the gate); `app` depends on it with
# service_completed_successfully.
#
# `app` reaching running proves both that the completed-condition gate fires AND
# that the one-shot's own running-gate iteration does NOT burn the full ~120s
# timeout before app is allowed to start. If `up` visibly stalls ~2 minutes or
# `app` never comes up, the completion path is broken — report it, don't weaken.
# Public images, no build.

if TARGET == "local":
    log("compose-depends-completed: skipped (needs a real backend)")
else:
    compose_file = "e2e/scenarios/compose-depends-completed.yaml"

    addr = serve()
    host = {"CORNUS_HOST": "http://" + addr}

    out = cornus("compose", "-f", compose_file, "up", "-d", env = host)
    log(out)

    # A generous timeout: app must come up soon after migrate exits 0, well
    # within this window. Timing out here means the completed-gate stalled.
    wait(name = "cdc-app", running = 1, timeout = "120s")
    log("✓ app started after migrate completed successfully")

    cornus("compose", "-f", compose_file, "down", env = host)
    assert_eq(status(name = "cdc-app")["total"], 0, "cdc-app still present after down")
    assert_eq(status(name = "cdc-migrate")["total"], 0, "cdc-migrate still present after down")
    log("✓ depends_on service_completed_successfully gates and releases")
