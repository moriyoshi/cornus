# Client-local bind mounts on the bare backend via the CORNUS_BARE_REMOTE
# sidecar path (MountingBackend.ApplyWithMounts): a caretaker companion joins the
# app instance's pinned netns, binds a shared scratch dir with rshared, and does
# the kernel 9P mount there; the app binds the SAME dir rslave. This is the
# opt-in alternative to the co-located host-9P fast path deploy_attach uses for
# bare by default (which deploy-mounts scenarios already cover).
#
# Scope note: this verifies the companion is SPAWNED, tracked, and torn down. It
# deliberately does NOT assert the mounted file CONTENT reaches the app: the
# rshared->rslave propagation between the two containers' mount namespaces does
# not work under the E2E's nested (docker-in-docker) mount setup — the same
# limitation that keeps the docker/containerd sidecar-mount content checks out of
# CI. The caretaker's own 9P mount succeeding is covered by the kube mount
# scenarios + pkg/caretaker unit tests; here we prove the bare wiring.
#
# bare-only + needs CORNUS_AGENT_IMAGE (prepare_bare_agent_image); self-skips
# otherwise.

agent_image = getenv("CORNUS_AGENT_IMAGE", "")
RUNC = "runc --root /run/cornus/bare-runc"

if TARGET != "bare":
    log("deploy-mounts-sidecar-bare: skipped (bare-only; exercises the CORNUS_BARE_REMOTE sidecar mount path)")
elif agent_image == "":
    log("deploy-mounts-sidecar-bare: skipped (no CORNUS_AGENT_IMAGE; prepare_bare_agent_image did not run)")
else:
    addr = serve(env = {
        "CORNUS_BARE_REMOTE": "1",
        "CORNUS_AGENT_IMAGE": agent_image,
    })

    local = temp_dir()
    write_file(path = local + "/marker", content = "LIVE-9P-MOUNT-BARE-SIDECAR")

    # The remote-mode backend realizes the mount via the sidecar companion rather
    # than the co-located fast path. deploy_attach blocks until the app is Running.
    deploy_attach(
        name = "mnt-sidecar",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        local_mount = [local + ":/data:ro"],
        timeout = "240s",
    )
    log("✓ sidecar-mount deploy Ready")

    # The mount-relay caretaker companion exists as a runc container beside the app.
    lst = sh(cmd = RUNC + " list -q 2>/dev/null")
    assert_contains(lst["output"], "cornus-mnt-sidecar-mount-0", "expected a mount-caretaker companion container, got %r" % lst["output"])
    assert_contains(lst["output"], "cornus-mnt-sidecar-0", "expected the app instance container")
    log("✓ mount-caretaker companion present as a runc container")

    # It shares the app instance's netns (joined for the relay dial).
    ns = sh(cmd = """
RUNC="%s"
apppid=$($RUNC state cornus-mnt-sidecar-0 2>/dev/null | grep -o '"pid": [0-9]*' | grep -o '[0-9]*')
comppid=$($RUNC state cornus-mnt-sidecar-mount-0 2>/dev/null | grep -o '"pid": [0-9]*' | grep -o '[0-9]*')
a=$(readlink /proc/$apppid/ns/net 2>/dev/null); c=$(readlink /proc/$comppid/ns/net 2>/dev/null)
[ -n "$a" ] && [ "$a" = "$c" ] && echo "SAME_NETNS" || echo "DIFFERENT app=$a comp=$c"
""" % RUNC)
    assert_contains(ns["output"], "SAME_NETNS", "mount companion must share the app instance netns, got %r" % ns["output"])
    log("✓ mount companion shares the app instance's netns")

    # The mount target is bound into the app (the propagation source); Status still
    # reports only the app, never the companion.
    dta = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "mnt-sidecar", "sh", "-c", "test -d /data && echo DATA_DIR_PRESENT"])
    assert_contains(dta["output"], "DATA_DIR_PRESENT", "the sidecar mount target /data must be bound into the app container")
    st = status(name = "mnt-sidecar")
    assert_eq(st["running"], 1, "Status must report exactly the app instance, not the mount companion")
    log("✓ mount target bound into the app; companion filtered out of Status")

    # Graceful disconnect tears down the app AND its companion.
    attach_stop(name = "mnt-sidecar")
    for _ in range(20):
        left = sh(cmd = RUNC + " list -q 2>/dev/null | grep cornus-mnt-sidecar || true")
        if left["output"] == "":
            break
        sleep(duration = "1s")
    left = sh(cmd = RUNC + " list -q 2>/dev/null | grep cornus-mnt-sidecar || true")
    assert_eq(left["output"], "", "app + mount companion must both be gone after attach_stop, still present: %r" % left["output"])
    log("✓ torn down: app and mount-caretaker companion both reaped")
