# Health probing survives a stop/start — the path where a misplaced lifecycle hook
# fails SILENTLY.
#
# containerd, bare and incus run cornus's own probe engine
# (pkg/deploy/healthengine). Stop disarms it, Start re-arms it, and the healthcheck
# itself is recovered from where the deploy persisted it: a container LABEL on
# containerd, the instance RECORD on bare, the instance CONFIG on incus.
#
# If Start did not re-arm, health would sit at "" forever and nothing would error —
# the workload would run fine, and a compose `depends_on: service_healthy` against
# it would simply hang much later.
#
# The EMPTY reading after stop is asserted too, because it is what distinguishes
# "re-armed" from "never disarmed" — without it this scenario would pass on a
# backend that just kept probing through the stop.

if TARGET not in ["bare", "containerd", "incus"]:
    log("health-restart-rearm: skipped (containerd/bare/incus only; the backends running cornus's own probe engine)")
else:
    compose_file = "e2e/scenarios/compose-dependson.yaml"
    addr = serve()
    host = {"CORNUS_HOST": "http://" + addr}
    cornus("compose", "-f", compose_file, "up", "-d", env = host)
    wait(name = "e2edep-db", running = 1, timeout = "180s")

    def health():
        insts = status(name = "e2edep-db")["instances"]
        if len(insts) == 0:
            return "NO-INSTANCES"
        return insts[0]["health"]

    def wait_health(want):
        for _ in range(240):
            if health() == want:
                return True
            sleep("0.5s")
        return False

    if not wait_health("healthy"):
        fail("db never reported healthy after up; got %s" % health())
    log("✓ healthy after up")

    stop(name = "e2edep-db")
    stopped = health()
    log("health after stop: %s" % stopped)
    if stopped != "":
        fail("health is %s after stop, want empty: a stopped instance must be DISARMED, or the re-arm below proves nothing" % stopped)

    start(name = "e2edep-db")
    wait(name = "e2edep-db", running = 1, timeout = "180s")
    if not wait_health("healthy"):
        fail("db did not report healthy again after restart; got %s — the probe was not re-armed, and the healthcheck was not recovered from where the deploy persisted it" % health())
    log("✓ healthy again after stop/start: the restart re-armed the probe")

    cornus("compose", "-f", compose_file, "down", env = host)
