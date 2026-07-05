# Foreground attached `docker run` through the Docker Engine API proxy.
#
# This is the scenario whose ABSENCE cost the most. Every other dockerd*.star
# drives `run -d`, so the create -> attach -> start ordering a foreground run uses
# was exercised by exactly one thing in the whole suite: the devcontainer CLI,
# from inside its own tooling. Two defects lived there for months, and the only
# signal either produced was a third-party CLI timing out after ten minutes.
#
# What was wrong, and why a fake cannot show it. Real dockerd registers the attach
# BEFORE the container's process starts; cornus deploys first (which starts the
# container) and can only attach afterwards. So:
#   1. Output written between container start and tunnel open was DISCARDED. The
#      window measured under 500ms — which is the entire output of anything
#      short-lived. `docker run alpine echo hi` printed nothing at all.
#   2. dockerd does not EOF an attach opened against a container that has ALREADY
#      exited (one opened while it ran EOFs ~20ms after it stops). So a
#      short-lived workload left the proxy's bridge blocked forever and
#      `docker run` never returned — measured at 281s, killable only by SIGKILL.
# Both are properties of a REAL daemon's attach semantics, which is why they are
# asserted here rather than against the fake attacher in pkg/dockerproxy.
#
# The two assertions are deliberately paired everywhere below: output AND
# termination. Either alone is satisfiable by a broken fix — closing the stream on
# the workload's exit makes `docker run` exit promptly while silently truncating
# its output, which is exactly the shape a previous attempt took.

FIRST = "FIRST-LINE-AT-T0"
LATER = "LATER-LINE"

if TARGET != "docker":
    log("dockerd-attach-foreground: skipped (docker-only; needs a real docker CLI against the proxy)")
else:
    # Up-front cleanup: Starlark has no defer, so end-of-scenario cleanup does not
    # run on failure, and the daemon is shared across runs.
    sh(cmd = "docker rm -f $(docker ps -aq -f label=cornus.app=fgattach) 2>/dev/null; true")
    sh(cmd = "docker rm -f $(docker ps -aq -f label=cornus.app=fgecho) 2>/dev/null; true")

    serve()
    host = dockerd_up()
    D = "docker -H '" + host + "' "

    # ---- 1. the shortest possible foreground run ------------------------------
    # `echo` writes once and exits, so ALL of its output is inside the window that
    # used to be lost and the container is gone before the tunnel opens. Both
    # defects fire on this one command; it printed nothing and hung.
    r = sh(cmd = "timeout -k 5 60 " + D + "run --rm --name fgecho alpine:3.20 echo " + FIRST + " 2>&1")
    assert_eq(r["code"], 0,
              "foreground `docker run` did not exit (rc %d): %r — the attach stream never ended" % (r["code"], r["output"]))
    assert_contains(r["output"], FIRST,
                    "foreground `docker run` printed %r: output written before the attach tunnel opened was lost" % r["output"])

    # It must appear EXACTLY once. Replaying the container's log to recover the
    # head is only correct if it does not also re-deliver what the live stream
    # already carried, and a doubled line is the failure mode that would prove it.
    assert_eq(r["output"].count(FIRST), 1,
              "the first line appeared %d times: the replay is duplicating the live stream (%r)"
              % (r["output"].count(FIRST), r["output"]))
    log("✓ a foreground `docker run` delivers a single immediate write, exactly once, and exits")

    # ---- 2. head and tail of a longer run, in order ---------------------------
    # The first line lands in the lost window; the later ones arrive over the live
    # stream. Asserting both — and their ORDER — pins the seam between the replay
    # and the live tail, which is where a re-ordering or gap would show up.
    prog = "echo %s; sleep 2; echo %s-1; sleep 1; echo %s-2" % (FIRST, LATER, LATER)
    r = sh(cmd = "timeout -k 5 90 " + D + "run --rm --name fgattach alpine:3.20 sh -c '" + prog + "' 2>&1")
    assert_eq(r["code"], 0, "foreground `docker run` did not exit (rc %d): %r" % (r["code"], r["output"]))
    for want in [FIRST, LATER + "-1", LATER + "-2"]:
        assert_contains(r["output"], want, "missing %r from the attached output: %r" % (want, r["output"]))
    head = r["output"].index(FIRST)
    mid = r["output"].index(LATER + "-1")
    tail = r["output"].index(LATER + "-2")
    assert_true(head < mid and mid < tail,
                "attached output arrived out of order (%d, %d, %d): %r" % (head, mid, tail, r["output"]))
    log("✓ the pre-attach head and the live tail arrive together, in order")

    # ---- 3. `docker attach` on an established container still works -----------
    # The counterpart to the replay, and the reason it must stay conditional: this
    # path never missed anything, so it must NOT be re-fed the container's history.
    # A container started here writes a marker BEFORE the attach, then again after;
    # only the second may appear.
    docker("-H", host, "run", "-d", "--name", "fgattach2", "alpine:3.20",
           "sh", "-c", "echo PRE-ATTACH; sleep 3; echo POST-ATTACH; sleep 1")
    sh(cmd = "sleep 1")
    r = sh(cmd = "timeout -k 5 60 " + D + "attach --no-stdin fgattach2 2>&1; true")
    assert_contains(r["output"], "POST-ATTACH",
                    "`docker attach` on a running container delivered nothing: %r" % r["output"])
    assert_true("PRE-ATTACH" not in r["output"],
                "`docker attach` replayed history the caller never asked for: %r" % r["output"])
    log("✓ `docker attach` on an established container streams live output and replays nothing")

    sh(cmd = D + "rm -f fgattach2 2>/dev/null; true")
