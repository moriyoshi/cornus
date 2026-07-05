# KNOWN-FAILING reproducer: a NON-ZERO self-exit is unobservable through the
# Docker Engine API proxy, because a plain `docker run` never gets Docker's own
# default restart policy.
#
# Docker's default is `no`: a container that exits stays exited, and `docker wait`
# prints its code. pkg/dockerproxy/translate.go drops that:
#
#     if rp := req.HostConfig.RestartPolicy.Name; rp != "" && rp != "no" {
#         spec.Restart = rp
#     }
#
# so both the absent policy and an explicit `--restart=no` leave spec.Restart
# empty, and deploy.RestartPolicy (pkg/deploy/deploy.go) then defaults it to
# "unless-stopped". The workload is handed to dockerhost with a restart policy
# Docker was never asked for, so an exited container is restarted forever. It
# never settles, api.DeployStatus.TerminalExitCode never resolves (a restarting
# container inspects as State.Running=true, so InstanceStatus.ExitCode stays nil),
# and the proxy's `docker wait` blocks until the client gives up.
#
# The consequence is that the exit code just threaded end to end is unreachable on
# the one path that should always show it: `docker run` a failing container and
# ask what happened. `--restart=on-failure` does not help — that policy is
# SUPPOSED to restart a failure — so there is no way to express "let it die with
# its code" through the proxy at all. (A second, smaller defect compounds it:
# translate.go also drops RestartPolicy.MaximumRetryCount, so even
# `--restart=on-failure:2` retries forever.)
#
# The assertion below is what CORRECT behaviour looks like and was deliberately
# NOT weakened while it failed — a passing test pinned to a defect would turn the
# defect into a specification.
#
# FIXED 2026-07-28: pkg/dockerproxy/translate.go now carries Docker's default
# (unset) and explicit "no" restart policies through as spec.Restart="no", so a
# container Docker would let die is no longer resurrected by the
# deploy.RestartPolicy "unless-stopped" default. This scenario flipped from
# failing to passing against that fix and is now a normal SCENARIOS member and
# ungated. Verified live on the docker target: `docker wait` printed 7 and
# `docker inspect` State.ExitCode reported 7.
#
# Still open (does NOT affect this scenario): translate.go drops
# RestartPolicy.MaximumRetryCount, so `--restart=on-failure:2` retries forever.

NAME = "wexit7"

if TARGET != "docker":
    log("dockerd-exit-code-nonzero: skipped (docker-only)")
else:
    # Up-front cleanup (Starlark has no defer): a restart-looping leftover from a
    # previous run of THIS scenario would otherwise keep burning the daemon.
    sh(cmd = "docker rm -f $(docker ps -aq -f label=cornus.app=%s) 2>/dev/null; true" % NAME)

    serve()
    host = dockerd_up()
    D = "docker -H '" + host + "' "

    # Docker's own default restart policy, stated explicitly so the intent is
    # unambiguous: this container must be allowed to die.
    docker("-H", host, "run", "-d", "--restart=no", "--name", NAME, "alpine:3.20", "sh", "-c", "sleep 2; exit 7")

    r = sh(cmd = "timeout -k 5 90 " + D + "wait " + NAME + " 2>&1")
    assert_eq(r["code"], 0, "`docker wait %s` never answered (rc %d, output %r) — the container is being restarted instead of being allowed to exit" % (NAME, r["code"], r["output"]))
    assert_eq(r["output"], "7", "`docker wait` must print the container's real exit code 7, got %r" % r["output"])
    log("✓ `docker wait` printed the real non-zero exit code 7")

    ins = sh(cmd = "timeout -k 5 30 " + D + "inspect " + NAME + " --format '{{.State.ExitCode}}'")
    assert_eq(ins["output"], "7", "`docker inspect` State.ExitCode must be 7 after the wait resolved it, got %r" % ins["output"])
    log("✓ `docker inspect` State.ExitCode reported 7")

    sh(cmd = "timeout -k 5 60 " + D + "rm -f " + NAME + " >/dev/null 2>&1; true")
