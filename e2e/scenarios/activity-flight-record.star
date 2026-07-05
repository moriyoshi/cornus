# The flight recorder, exercised on the one incident it was built for: a server
# is killed while holding a live kernel 9P mount, and the next server has to
# find that mount from the record and undo it.
#
# This cannot be unit-tested. A real 9P mount needs root, so the server-side unit
# tests use an ordinary directory and only ever exercise the "target already
# absent" path. Here the server runs as root inside a privileged container, the
# mount is real, and it genuinely strands on the host when the process dies —
# which is the whole reason the record exists.
#
# The assertions are deliberately about the WORLD, not just the log: the mount
# must still be in the host's mount table after the server dies (otherwise there
# is no incident to recover from and the test proves nothing), and gone after the
# next server starts.
#
# Needs: TARGET == "docker", a prebuilt cornus-embedding image
# (CORNUS_AGENT_IMAGE — e.g. "cornus:e2e"), and privileged Docker. Self-skips
# otherwise, matching server-in-container.star.
#
# CAUTION when running this by hand: the SERVER under test is the one inside
# CORNUS_AGENT_IMAGE, not the `bin/cornus` that `make` rebuilds. A stale image
# silently exercises old server code. Rebuild it from the same tree first — see
# the note in server-in-container.star.

server_image = getenv("CORNUS_AGENT_IMAGE", "")

# Every assertion below is about THIS run's own mountpoint, identified by diffing
# the mount table around the deploy. A substring pattern would be wrong twice
# over: this host can carry 9P mounts stranded by earlier runs (that is the very
# failure being tested, so they are expected to exist), and matching one of those
# would make the test fail — or, worse, pass — for reasons that have nothing to
# do with the code under test.
def mount_points():
    out = sh(cmd = "mount | grep ' type 9p ' | awk '{print $3}' || true")["output"]
    return [ln.strip() for ln in out.split("\n") if ln.strip() != ""]

if TARGET != "docker":
    log("activity-flight-record: skipped (docker-only; needs a containerized server that can really mount)")
elif server_image == "":
    log("activity-flight-record: skipped (set CORNUS_AGENT_IMAGE to a prebuilt cornus-embedding image, e.g. cornus:e2e)")
else:
    # --- a server, and a live mount it owns --------------------------------

    # This scenario deliberately kills a server mid-flight, so a FAILED run can
    # leave its workload behind — with a stranded 9P mount under it. Clear it
    # first rather than depend on the previous run's tidiness.
    #
    # This is now hygiene, not a correctness crutch: deploy_attach no longer
    # waits by polling Status(name) (which a leftover container with the same
    # name satisfies instantly, letting a run sail past the wait before its own
    # deploy had recorded anything). It waits for its own session to report
    # ready — see sawAttachReady in pkg/e2e/harness.go.
    sh(cmd = "docker rm -f cornus-flight-0 >/dev/null 2>&1 || true")

    addr = serve_container(image = server_image)
    server_name = docker("ps", "--filter", "name=cornus-e2e-server", "--format", "{{.Names}}").strip()
    assert_true(server_name != "", "expected the containerized server to be running")

    # Snapshot first, so the mountpoint this deploy creates can be told apart
    # from anything already on the machine.
    before_deploy = mount_points()

    local = temp_dir()
    write_file(path = local + "/marker", content = "FLIGHT-RECORD")
    deploy_attach(
        name = "flight",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        local_mount = [local + ":/data:ro"],
        timeout = "240s",
    )

    # The record must exist WHILE the mount is live and be unfinished — that is
    # the write-ahead property. If it only appeared on teardown, a crash would
    # leave nothing to recover from.
    open_now = cornus("activity", "--server", "http://" + addr, "--unfinished", "--kind", "9p-mount")
    assert_contains(open_now, "9p-mount", "a live mount must be recorded, and recorded as unfinished")
    assert_contains(open_now, "deployment=flight", "the record must say which deployment the mount belongs to")
    log("✓ the live mount is on the record before anything goes wrong")

    fresh = [m for m in mount_points() if m not in before_deploy]
    assert_eq(len(fresh), 1, "expected exactly one new 9P mount in the host mount table for this deploy")
    mountpoint = fresh[0]
    log("this run's mountpoint: " + mountpoint)

    # --- the incident ------------------------------------------------------

    # SIGKILL, not a graceful stop: no unwind runs, exactly as an OOM or a
    # `docker rm -f` would behave.
    docker("kill", server_name)

    assert_true(mountpoint in mount_points(),
                "the mount must OUTLIVE the server — if it did not, there is no incident here and the rest proves nothing")
    log("✓ the server is gone and its mount is stranded on the host")
    docker("rm", "-f", server_name)

    # --- the next server cleans up after it --------------------------------

    # Same data dir (the harness reuses it), so this server reads its
    # predecessor's records.
    addr2 = serve_container(image = server_image)

    assert_true(mountpoint not in mount_points(),
                "startup recovery must have unmounted " + mountpoint + ", but it is still in the mount table")
    log("✓ the next server found the mount in the record and unmounted it")

    # --- and the flight tells the story ------------------------------------

    flight = cornus("activity", "--server", "http://" + addr2)
    assert_contains(flight, "DID NOT EXIT CLEANLY", "the killed run must be legible as an unclean exit")
    assert_contains(flight, "recovered", "the recovery must be recorded, not silently performed")
    log("✓ the record shows the unclean exit and the recovery")

    # Nothing is left open: the unfinished set converges instead of re-reporting
    # the same historical crash on every future startup.
    left = cornus("activity", "--server", "http://" + addr2, "--unfinished", "--kind", "9p-mount")
    assert_contains(left, "no unfinished", "the recovered mount must no longer be reported as unfinished")
    log("✓ the unfinished set converged")

    # The workload outlived its server (the old one never got to remove it), so
    # clean it up explicitly rather than leaving it for the next run.
    docker("rm", "-f", "cornus-flight-0")
