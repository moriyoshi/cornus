# Many client-local bind mounts on ONE workload, with NESTED targets — the
# regression guard for "the caretaker mounts the first volume, then hangs on the
# second and later". This is the deploy-attach-tier companion to
# compose-mounts-multi.star (which adds the compose translation + an interspersed
# anonymous volume); here the caretaker mount path is exercised directly.
#
# Topology mirrors a real `init`-style service: /app plus three mounts nested
# under it and one in a separate tree, mixed ro/rw. Each local dir is streamed
# over its own 9P stream, all multiplexed over the SINGLE caretaker connection.
# The pod's caretaker fans out one runMountStream goroutine per mount
# (pkg/caretaker/caretaker.go), each blocking in Mount9P until its 9P handshake
# completes. The sidecar startup probe (caretaker.Ready) reports ready only once
# EVERY mount is a live mountpoint, so deploy_attach cannot return "running"
# until all mounts are up. Deterministic reproduction: if any mount past the
# first stalls, deploy_attach TIMES OUT here rather than reaching the assertions.
#
# deploy-mounts.star already covers N=2 flat (mnt2); this scales past two AND
# nests targets under a common parent so a head-of-line / propagation-ordering
# stall is caught instead of racing green.
#
# kube target: the mounts are realized inside the pod by the privileged native
# mount-agent sidecar and relayed back through the cornus server.
# Other targets: skipped (dockerhost needs root to kernel-9p-mount on the host).

if TARGET != "kube":
    log("deploy-mounts-multi: skipped (kube-only; the sidecar mount path)")
else:
    serve()

    # (host-dir, container-target, read-only) mirroring the real service's nested
    # layout: three mounts under /app plus one in a separate tree.
    specs = [
        ("app", "/app", False),
        ("data", "/app/data", False),
        ("config", "/app/config", True),
        ("extra", "/opt/extra", True),
    ]
    mounts = []
    for name, target, ro in specs:
        d = temp_dir()
        write_file(path = d + "/marker", content = "MOUNT-" + name)
        spec = d + ":" + target
        if ro:
            spec += ":ro"
        mounts.append(spec)
    log("serving %d nested client-local mounts on one workload" % len(mounts))

    # A stall on the 2nd+ mount makes readiness never fire -> this call times out.
    deploy_attach(
        name = "mntmulti",
        image = "cornus:e2e",
        entrypoint = ["sleep"],  # override cornus:e2e's `cornus` ENTRYPOINT
        command = ["3600"],
        local_mount = mounts,
        timeout = "240s",
    )
    log("✓ all %d mounts reached readiness (deploy_attach returned running)" % len(mounts))

    # Read every marker back from inside the running container: proves each mount
    # is independently live and correctly routed (not just the first), including
    # the nested ones layered under /app.
    for name, target, _ in specs:
        got = pod_exec(app = "mntmulti", cmd = "cat " + target + "/marker")
        assert_eq(got, "MOUNT-" + name, "mount at %r must be live and carry its own content" % target)
    log("✓ every nested client-local mount live and correctly routed over the shared caretaker connection")

    # All N mounts must ride ONE caretaker sidecar (a single multiplexed
    # connection), not one sidecar per mount — assert exactly one caretaker
    # init-container carries the whole set.
    caretaker_names = kubectl(
        "-n",
        "cornus-e2e",
        "get",
        "deployment",
        "mntmulti",
        "-o",
        "jsonpath={range .spec.template.spec.initContainers[?(@.name=='cornus-caretaker')]}{.name}{'\\n'}{end}",
    )
    assert_eq(len(caretaker_names.split()), 1, "expected exactly one caretaker sidecar carrying all mounts, got %r" % caretaker_names)
    log("✓ all mounts multiplexed over a single caretaker sidecar")

    # Graceful disconnect must tear the deployment down.
    attach_stop(name = "mntmulti")
    log("torn down")
