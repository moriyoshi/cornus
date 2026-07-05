# A web terminal session's FOREGROUND program and working directory, end to end.
#
# This is the only test of that chain that runs it for real. Every unit test on
# either side drives a fake: the BFF's tests feed a canned /proc capture, and
# pkg/shells' tests run the wrapper against the host's own shell. Neither can prove
# the parts meet — that the SERVER actually wraps the launch, that a busybox `sh`
# in a real image emits the private OSC, that the BFF's scanner picks it out of a
# live TTY stream, and that an auxiliary exec can read /proc inside that container
# and find the pid the wrapper announced.
#
# Backend-agnostic on purpose, and that is the point of running it on kube as well
# as docker: the whole reason the pid is announced from INSIDE (rather than taken
# from api.ExecState.Pid) is that ExecState.Pid is a host pid and is 0 on kubernetes
# and incus. A version of this that only ever ran on docker would pass just as
# happily with the unsound design.
#
# Opt-in (make e2e-web-terminal); not in the default SCENARIOS list.

if TARGET == "local":
    log("web terminal introspection: skipped (needs a real backend)")
else:
    serve()

    deploy(name = "wterm", image = "alpine:3.20", command = ["sleep", "3600"])
    wait(name = "wterm", running = 1, timeout = "240s")

    base = web()

    # The session's foreground program is deliberately NOT its shell. `exec sleep`
    # replaces the shell in place, so the pid the wrapper announced ends up naming
    # `sleep` — which means "sleep" can only be reported by actually reading /proc.
    # A session running a plain shell could not tell that apart from a BFF echoing
    # back the basename of the command it was asked to launch.
    #
    # The directory is reached with `cd` INSIDE the command rather than by passing
    # `dir`, because `dir` is not portable: kubernetes' pods/exec subresource has no
    # working-directory field, so that backend warns and ignores it (see the comment
    # on createTermRequest.Dir). An earlier version of this scenario passed
    # dir=/usr, which made it pass on docker and fail on kube reporting the image's
    # default "/" — a scenario bug that looked exactly like a feature bug. `cd` is
    # honoured by every backend because it is just part of the command.
    created = http(
        method = "POST",
        url = base + "/.cornus/web/terminals",
        body = '{"workload":"wterm","cmd":["/bin/sh","-c","cd /usr && exec sleep 300"]}',
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(created["status"], 200, "create a persistent terminal session")

    # The probe is lazy: it is driven by the session LIST request and its answer
    # lands on a later poll, so this loop is the browser's 2s poll, sped up. It also
    # covers the wrapper's OSC needing to arrive on the stream first.
    body = ""
    for i in range(60):
        listed = http_get(url = base + "/.cornus/web/terminals")
        assert_eq(listed["status"], 200, "list terminal sessions")
        body = listed["body"]
        if body.find('"title":"sleep"') != -1 and body.find('"cwd":"/usr"') != -1:
            break
        sleep(duration = "500ms")

    assert_contains(
        body,
        '"title":"sleep"',
        "the session's foreground program was never reported: the launch wrapper, its OSC pid announcement, or the /proc probe did not survive this backend",
    )
    assert_contains(
        body,
        '"cwd":"/usr"',
        "the session's working directory was never reported (the probe reads /proc/<tpgid>/cwd inside the container)",
    )
    log("✓ foreground program (sleep) and cwd (/usr) read from /proc inside the container")

    # The wrapper is an implementation detail of HOW the command is spelled, never
    # part of what the session IS. The UI shows `cmd` in its Command column and in
    # the close dialog, so a wrapper leaking into it would be visible to users on
    # every terminal — and this is the assertion that catches it.
    assert_contains(
        body,
        '"cmd":["/bin/sh","-c","cd /usr \\u0026\\u0026 exec sleep 300"]',
        "the session list must report the argv the caller asked for, not the pid-announcing wrapper the server built",
    )
    assert_true(
        body.find("cornus-pid") == -1,
        "the pid announcement leaked into the session list",
    )
    assert_true(
        body.find("printf") == -1,
        "the wrapper script leaked into the session list",
    )
    log("✓ the launch wrapper stays invisible: `cmd` is the argv the caller asked for")

    # A second session in a DIFFERENT directory, and one that contradicts the `dir`
    # it was handed. Without this the cwd assertion above is satisfied by any
    # implementation that reports a constant, or that echoes `dir` back instead of
    # reading the process. Here `dir` says /usr and the process is in /etc, so only
    # reading the live process gives the right answer — on kube `dir` is ignored
    # outright, so the session starts at / and `cd` is what puts it in /etc either way.
    created2 = http(
        method = "POST",
        url = base + "/.cornus/web/terminals",
        body = '{"workload":"wterm","cmd":["/bin/sh","-c","cd /etc && exec sleep 300"],"dir":"/usr"}',
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(created2["status"], 200, "create a second terminal session")

    body2 = ""
    for i in range(60):
        listed2 = http_get(url = base + "/.cornus/web/terminals")
        assert_eq(listed2["status"], 200, "list terminal sessions")
        body2 = listed2["body"]
        if body2.find('"cwd":"/etc"') != -1:
            break
        sleep(duration = "500ms")

    # It started in /usr (dir) and cd'd to /etc before exec'ing, so /etc is only
    # reachable by reading the live process. Reporting /usr here would mean the BFF
    # is repeating the requested directory back.
    assert_contains(
        body2,
        '"cwd":"/etc"',
        "the reported cwd follows the requested `dir` rather than the live process: a session that cd'd away still reported where it was asked to start",
    )
    log("✓ the reported cwd tracks the live process, not the directory the session was asked to start in")
