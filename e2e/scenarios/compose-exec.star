# `cornus compose exec` — the Docker Compose exec fidelity path. Mirrors exec.star
# (which covers the native `cornus exec`) but drives the command through the
# compose CLI: it resolves a SERVICE to its deployment and execs into the first
# instance. Target-agnostic: runs on BOTH the docker backend (dockerhost exec) and
# the kube backend (remotecommand exec), proving the interactive `-it` session,
# the piped `-T` non-TTY path, exit-code propagation, and `--env` plumbing work
# end to end regardless of backend. The local target has no runtime backend, so it
# is skipped.

if TARGET == "local":
    log("compose exec: skipped (needs a real backend)")
else:
    compose_file = "e2e/scenarios/compose-exec.yaml"
    addr = serve()
    host = "http://" + addr

    # Bring the service up detached (mount-free, port-free: deploys and returns),
    # then wait for the container. The deployment resource is <project>-<service>.
    compose_up(file = compose_file, detach = True)
    wait(name = "e2e-exec-app", running = 1, timeout = "240s")

    # Interactive TTY exec WITH a window size: drive `cornus compose exec app sh`
    # under a real 24x100 PTY (compose exec allocates a TTY by default when stdin is
    # a terminal), TYPE `stty size` into the live shell, and read it back. "24 100"
    # proves the local PTY dimensions propagated through ExecResize to the container
    # TTY; EXEC_OK proves the default stdin bridge delivered the typed command.
    r = exec_tty(
        argv = ["cornus", "compose", "-f", compose_file, "-H", host, "exec", "app", "sh"],
        input = "stty size; echo EXEC_OK; exit\n",
        rows = 24,
        cols = 100,
    )
    assert_contains(r["output"], "EXEC_OK", "interactive TTY compose exec did not run the typed command")
    assert_contains(r["output"], "24 100", "PTY window size did not propagate to the remote TTY (stty size)")
    log("✓ interactive `compose exec app sh`: typed command ran and the 24x100 window size propagated")

    # Non-TTY (`-T`) exit-code propagation: `compose exec` os.Exit()s with the
    # remote process's status, so a non-zero exit must surface as the command's
    # exit code (exec used to discard the inspect result and report success).
    ec = exec_tty(
        argv = ["cornus", "compose", "-f", compose_file, "-H", host, "exec", "-T", "app", "sh", "-c", "echo NONINT; exit 7"],
    )
    assert_contains(ec["output"], "NONINT", "non-TTY compose exec did not run the command")
    assert_eq(ec["code"], 7, "compose exec exit code not propagated from the backend (got %r)" % ec["code"])
    log("✓ `compose exec -T` exit code 7 propagated from the backend")

    # `--env` plumbing: -e FOO=bar must reach the command's environment inside the
    # container (the remote sh expands $FOO from ExecConfig.Env).
    ev = exec_tty(
        argv = ["cornus", "compose", "-f", compose_file, "-H", host, "exec", "-T", "-e", "FOO=bar", "app", "sh", "-c", "echo VAL=$FOO"],
    )
    assert_contains(ev["output"], "VAL=bar", "compose exec --env did not reach the command environment")
    log("✓ `compose exec -e FOO=bar` reached the command environment")

    # A non-zero remote command (`/bin/false`, exit 1) must never report success.
    fl = exec_tty(
        argv = ["cornus", "compose", "-f", compose_file, "-H", host, "exec", "-T", "app", "/bin/false"],
    )
    assert_true(fl["code"] != 0, "`compose exec ... /bin/false` must exit non-zero, not report success (got %r)" % fl["code"])
    log("✓ `compose exec ... /bin/false` surfaces a non-zero exit")

    # An unknown service is rejected before any exec is created.
    ns = exec_tty(
        argv = ["cornus", "compose", "-f", compose_file, "-H", host, "exec", "-T", "nope", "true"],
    )
    assert_true(ns["code"] != 0, "compose exec into an unknown service must fail")
    assert_contains(ns["output"], "no such service", "unknown-service error not surfaced")
    log("✓ `compose exec <unknown>` is rejected")

    compose_down(file = compose_file)
    log("✓ compose exec fidelity path verified")
