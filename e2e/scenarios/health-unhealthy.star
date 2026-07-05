# The FAILURE half of the health state machine.
#
# Every other health scenario asserts the happy path — healthy after up, healthy
# after a stop/start, healthy again after a server restart. A backend that could
# only ever report `healthy` would pass all of them. This one is the counterweight:
# it drives a probe that FAILS and asserts the states that follow.
#
# Two rules are pinned, and the second is the one the design notes called the
# easiest to get wrong:
#
#   1. `retries` consecutive failures flip a workload to `unhealthy`, and a later
#      success flips it back to `healthy` — which also proves the consecutive-failure
#      counter is RESET rather than merely stopped.
#   2. A failure DURING `start_period` does not count toward `retries`. Without that
#      rule, workload B below would be unhealthy in about two seconds; with it, it
#      cannot be unhealthy before its start period elapses. Getting this wrong makes
#      every slow-starting workload flap to unhealthy, and no assertion anywhere
#      else would notice.
#
# It runs on the probe-engine backends AND on docker, deliberately. Docker runs its
# own healthcheck, so the same assertions passing there is evidence that cornus's
# state machine matches the one `depends_on: condition: service_healthy` is defined
# against — a differential the unit tests cannot provide, since they test cornus's
# engine against cornus's reading of Docker's rules.

if TARGET not in ["docker", "podman", "containerd", "bare", "incus"]:
    log("health-unhealthy: skipped (needs a backend that reports health; kube maps readiness, not this state machine)")
else:
    addr = serve()

    def health(name):
        insts = status(name = name)["instances"]
        if len(insts) == 0:
            return "NO-INSTANCES"
        return insts[0]["health"]

    def wait_health(name, want, steps = 120):
        for _ in range(steps):
            if health(name) == want:
                return True
            sleep("0.5s")
        return False

    # ---- A: a probe that fails, then starts passing -------------------------
    #
    # The check tests for a file the workload does not create, so it fails from the
    # first probe. Touching the file later flips it, which is what makes the
    # recovery arm observable without redeploying.
    deploy(
        name = "hz-fail",
        image = "alpine:3.20",
        # entrypoint, not command: incus can only replace the whole argv, so a
        # command-only override is ignored there.
        entrypoint = ["sh", "-c", "sleep 3600"],
        healthcheck = {"test": "CMD,test,-f,/tmp/ok", "interval": "1s", "timeout": "5s", "retries": "2"},
    )
    wait(name = "hz-fail", running = 1, timeout = "240s")

    if not wait_health("hz-fail", "unhealthy"):
        fail(msg = "a workload whose probe always fails never reported unhealthy (got %s) — the " % health("hz-fail") +
                   "failure half of the state machine does nothing, and every other health scenario " +
                   "would still pass")
    log("✓ consecutive probe failures flipped the workload to unhealthy")

    # The container is still RUNNING while unhealthy. Health is reporting, not
    # control: nothing restarts a container for going unhealthy, and a backend that
    # conflated the two would show up right here.
    st = status(name = "hz-fail")
    assert_eq(st["running"], 1, "an unhealthy workload must still be running: health reports, it does not act")
    log("✓ unhealthy did not stop or restart the workload")

    # Recovery. The probe starts passing, so the state returns to healthy — which
    # only happens if the failure counter is cleared rather than latched.
    exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "hz-fail", "sh", "-c", "touch /tmp/ok"])
    if not wait_health("hz-fail", "healthy"):
        fail(msg = "a passing probe did not restore healthy (got %s) — the consecutive-failure count " % health("hz-fail") +
                   "is latched, so a workload that recovers stays marked unhealthy forever")
    log("✓ a passing probe restored healthy, so the failure count resets")

    # ---- B: failures during the start period must not count ------------------
    #
    # retries=1 and interval=1s, so WITHOUT the start-period rule this is unhealthy
    # within ~2 seconds. start_period=30s, so WITH the rule it cannot be unhealthy
    # for 30. Sampling in between is therefore decisive in both directions rather
    # than a race: the two outcomes are ~28 seconds apart.
    deploy(
        name = "hz-slow",
        image = "alpine:3.20",
        entrypoint = ["sh", "-c", "sleep 3600"],
        healthcheck = {"test": "CMD,test,-f,/tmp/ok", "interval": "1s", "timeout": "5s",
                       "retries": "1", "start_period": "30s"},
    )
    wait(name = "hz-slow", running = 1, timeout = "240s")

    sleep("8s")
    got = health("hz-slow")
    if got == "unhealthy":
        fail(msg = "a workload failing its probe INSIDE its 30s start period was already marked " +
                   "unhealthy after 8s: start-period failures are counting toward retries, which " +
                   "makes every slow-starting workload flap")
    if got != "starting":
        fail(msg = "health during the start period is %r, want 'starting'" % got)
    log("✓ probe failures inside the start period did not count toward retries")

    # And it is not stuck: once the start period elapses, the same failures DO
    # count. Without this the assertion above would also pass on a backend that
    # never left `starting` at all.
    if not wait_health("hz-slow", "unhealthy", steps = 240):
        fail(msg = "the workload never left `starting` (got %s) — a state machine stuck there " % health("hz-slow") +
                   "would satisfy the start-period assertion above while reporting nothing real")
    log("✓ after the start period elapsed, the same failures flipped it to unhealthy")

    remove(name = "hz-fail")
    remove(name = "hz-slow")
