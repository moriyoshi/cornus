# Interactive exec through the native `cornus exec` CLI. This is target-agnostic:
# it runs on BOTH the docker and kube backends (the SAME scenario exercises the
# dockerhost exec path on the docker target and the remotecommand exec path on the
# kube target), proving the interactive `-it` session and its exit-code propagation
# work end to end regardless of backend. The local target has no runtime backend,
# so it is skipped.

if TARGET == "local":
    log("exec: skipped (needs a real backend)")
else:
    addr = serve()

    # A long-lived container to exec into.
    deploy(name = "ex", image = "alpine:3.20", command = ["sleep", "3600"])
    wait(name = "ex", running = 1, timeout = "240s")

    # Interactive TTY exec WITH a window size: drive `cornus exec -i -t ex sh` under a
    # real 24x100 PTY, TYPE `stty size` into the live shell, and read it back. stty
    # prints "<rows> <cols>" from the remote TTY's window size, so seeing "24 100"
    # proves the local PTY dimensions propagated through ExecResize all the way to the
    # container's TTY; seeing EXEC_TTY_OK proves the interactive `-i` stdin bridge
    # actually delivered the typed command. (The exec_tty harness answers the shell's
    # startup cursor-position query, ESC[6n, the way a real terminal does — busybox
    # ash under TERM=xterm blocks on it otherwise.)
    r = exec_tty(
        argv = ["cornus", "exec", "-i", "-t", "--server", "http://" + addr, "ex", "sh"],
        input = "stty size; echo EXEC_TTY_OK; exit\n",
        rows = 24,
        cols = 100,
    )
    assert_contains(r["output"], "EXEC_TTY_OK", "interactive TTY exec did not run the typed command")
    assert_contains(r["output"], "24 100", "PTY window size did not propagate to the remote TTY (stty size)")
    log("✓ interactive `cornus exec -it`: typed command ran and the 24x100 window size propagated")

    # Exit-code propagation (non-TTY): `cornus exec` os.Exit()s with the remote
    # process's status, so a non-zero exit must surface as the command's exit code.
    ec = exec_tty(
        argv = ["cornus", "exec", "--server", "http://" + addr, "ex", "sh", "-c", "echo NONINT; exit 7"],
    )
    assert_contains(ec["output"], "NONINT", "non-interactive exec did not run the command")
    assert_eq(ec["code"], 7, "exec exit code not propagated from the backend (got %r)" % ec["code"])
    log("✓ `cornus exec` exit code 7 propagated from the backend")

    # A failed command (the canonical `/bin/false`, exit 1) must surface as a
    # non-zero exit — exec used to discard the exit-status inspect result and
    # report success. `cornus exec` maps the remote status to its own exit code,
    # so a non-zero remote command must never exit 0.
    fl = exec_tty(
        argv = ["cornus", "exec", "--server", "http://" + addr, "ex", "/bin/false"],
    )
    assert_true(fl["code"] != 0, "`cornus exec ... /bin/false` must exit non-zero, not report success (got %r)" % fl["code"])
    log("✓ `cornus exec ... /bin/false` surfaces a non-zero exit (not reported as success)")
