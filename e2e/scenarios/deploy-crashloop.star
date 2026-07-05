# Deploy readiness reporting on kubernetes: a workload whose container exits on
# every start crash-loops, and cornus must report WHY instead of claiming the
# workload is up. This guards the readiness path end to end:
#
#   - statusOf attaches a per-instance diagnostic (the container's Waiting reason,
#     here CrashLoopBackOff) surfaced through DeployStatus.Instances[].Message.
#     That is the signal pkg/server's awaitReady streams to a deploy-attach client
#     so a crash loop is reported live instead of the session falsely reporting
#     success (the historical bug: initContainer args were [caretaker] with no
#     `cornus` entrypoint, the caretaker crash-looped, yet the client said "up").
#   - a crash-looping instance is reported not-running (running == 0), never "up".
#
# kube-only. Runs on the plain cluster; needs the cornus:e2e image the kube target
# already loads. The app entrypoint is overridden to /bin/false so the container
# exits non-zero on every start and Kubernetes backs it off into CrashLoopBackOff.

if TARGET != "kube":
    log("deploy-crashloop: skipped (kube-only; crash-loop readiness diagnostic)")
else:
    serve()

    # Detached deploy (NOT deploy_attach): the Deployment is created and the pod
    # starts crash-looping. deploy_attach would block the harness on a running
    # count; here we deliberately want a workload that never becomes ready.
    deploy(
        name = "crashloop",
        image = "cornus:e2e",
        entrypoint = ["/bin/false"],  # exits 1 on every start
    )

    # Poll until the instance reports CrashLoopBackOff. Kubernetes needs a few
    # fast-exit restarts before it enters the back-off state, hence the retry loop.
    msg = ""
    running = 1
    for _ in range(60):
        st = status(name = "crashloop")
        running = st["running"]
        insts = st["instances"]
        if len(insts) > 0:
            msg = insts[0]["message"]
        if "CrashLoopBackOff" in msg:
            break
        sleep(duration = "2s")

    assert_contains(msg, "CrashLoopBackOff", "crash-looping instance must report a CrashLoopBackOff diagnostic, got %r" % msg)
    assert_eq(running, 0, "a crash-looping workload must report 0 running, got %d" % running)
    log("✓ crash-looping workload reports not-running with a CrashLoopBackOff diagnostic: %r" % msg)

    remove(name = "crashloop")
    log("removed")
