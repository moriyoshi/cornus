# `cornus compose` drives a Dev Container repo with NO Compose file: it
# auto-translates .devcontainer/devcontainer.json into the same compose.Project
# the Compose path uses. A single-container (image-based) devcontainer always
# bind-mounts the workspace at workspaceFolder, so it deploys over the
# deploy-attach 9P path — realized on Kubernetes by a privileged native-sidecar
# mount-agent (never a hostPath), just like compose bind mounts.
#
# kube-only: the workspace mount rides the sidecar 9P path (the interesting one).
# Other targets skip — a dockerhost would need root to kernel-9p-mount on the
# host, mirroring deploy-mounts.star / compose-mounts.star.
#
# It also proves the `runArgs` mapping REACHES the backend. `runArgs` is
# unit-tested at the compose-plan level (pkg/devcontainer TestRunArgsMapped),
# which shows what cornus WRITES DOWN; it cannot show that the backend honored
# it. So every assertion below reads the running container's own kernel view —
# /proc/self/status, a statfs of /dev/shm, /proc/sys/kernel/hostname — never the
# generated object. The flags were chosen for surviving the KUBERNETES
# translation specifically (see the fixture's comment for the ones deliberately
# left out because they are docker-only).

devcontainer_dir = "e2e/scenarios/devcontainer"
APP = "dc-devcontainer"

# Capability bit positions (uapi/linux/capability.h). SYS_PTRACE is NOT in the
# default container capability set and MKNOD IS, so one added and one dropped
# capability distinguishes "the flags were applied" from both "nothing was
# applied" and "everything is on" (a privileged container would pass a bare
# cap-add assertion).
CAP_MKNOD = 1 << 27
CAP_SYS_PTRACE = 1 << 19

def wait_gone(name, steps = 90):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed after devcontainer down" % name)

if TARGET != "kube":
    log("devcontainer: skipped (kube-only; the workspace mount rides the sidecar 9P path)")
else:
    serve()

    # Up-front cleanup: Starlark has no defer, so a run that failed midway leaves
    # the deployment behind, and a leftover pod would satisfy the wait() below
    # (and be read by the runArgs assertions) without this run having deployed
    # anything.
    if status(name = APP)["total"] > 0:
        remove(name = APP)
        wait_gone(APP)

    # up -d: the single-container devcontainer deploys as service "devcontainer";
    # its workspace bind mount is streamed over 9P by a background helper (never a
    # hostPath). -p dc pins the project so the deploy name is deterministic.
    devcontainer_up(dir = devcontainer_dir, project = "dc", detach = True)

    # The synthesized service reaches running under the project-qualified name.
    st = wait(name = APP, running = 1, timeout = "240s")
    assert_eq(st["running"], 1, "devcontainer service not running")
    log("✓ devcontainer up: single-container service running with workspace 9P mount")

    # ps reports the service through the same code path Compose uses.
    ps = devcontainer_ps(dir = devcontainer_dir, project = "dc")
    assert_contains(ps, "devcontainer")
    log("✓ devcontainer ps reports the service")

    # --- runArgs reached the backend -------------------------------------------
    # 1. --cap-add SYS_PTRACE / --cap-drop MKNOD.
    #    CapEff is the effective set the kernel gave THIS process, so it is the
    #    container's own account of what it got, not cornus's account of what it
    #    asked for. Read as one line and parsed here so the failure message can
    #    print the mask that was actually observed.
    capline = pod_exec(app = APP, cmd = "grep ^CapEff: /proc/self/status").strip()
    fields = capline.split()
    assert_true(len(fields) == 2, "unexpected CapEff line %r" % capline)
    capeff = int(fields[1], 16)
    assert_true(
        capeff & CAP_SYS_PTRACE != 0,
        ("runArgs --cap-add SYS_PTRACE never reached the container: CapEff=%s has bit 19 clear. " +
         "The flag is parsed by pkg/devcontainer and mapped to " +
         "securityContext.capabilities.add by the kubernetes backend, so a clear bit means " +
         "one of those hops dropped it.") % fields[1],
    )
    assert_true(
        capeff & CAP_MKNOD == 0,
        ("runArgs --cap-drop MKNOD never reached the container: CapEff=%s still has bit 27 set. " +
         "MKNOD is in the default container capability set, so this is the assertion that " +
         "distinguishes a real cap_drop from a container that was simply given everything.") % fields[1],
    )
    log("✓ runArgs cap-add/cap-drop reached the container (CapEff=%s)" % fields[1])

    # 2. --shm-size 256m.
    #    On kubernetes this is an emptyDir{medium: Memory, sizeLimit: 256Mi}
    #    mounted at /dev/shm, so what proves it landed is the SIZE of the
    #    filesystem the container sees. statfs (block size x total blocks) is
    #    exact and needs only coreutils; `df` would round. The default /dev/shm a
    #    pod gets is 64 MiB, so an exact 256 MiB is unambiguous — and an
    #    unexpected value (e.g. half of node RAM, which is what a kubelet that
    #    ignored the sizeLimit would give) fails loudly instead of passing a
    #    "bigger than the default" check.
    shm = pod_exec(app = APP, cmd = "stat -f -c '%S %b' /dev/shm").strip().split()
    assert_true(len(shm) == 2, "unexpected statfs output for /dev/shm: %r" % shm)
    shm_bytes = int(shm[0]) * int(shm[1])
    assert_eq(
        shm_bytes, 256 * 1024 * 1024,
        "runArgs --shm-size 256m did not reach /dev/shm (got %d bytes; the pod default is 64 MiB)" % shm_bytes,
    )
    log("✓ runArgs --shm-size reached the container (/dev/shm = %d bytes)" % shm_bytes)

    # 3. --hostname devbox -> podSpec.hostname. A third, independent hop
    #    (pod spec rather than securityContext or a volume), read from the
    #    container's own UTS namespace.
    hn = pod_exec(app = APP, cmd = "cat /proc/sys/kernel/hostname").strip()
    assert_eq(hn, "devbox", "runArgs --hostname never reached the pod (hostname is %r)" % hn)
    log("✓ runArgs --hostname reached the container (hostname=%s)" % hn)

    devcontainer_down(dir = devcontainer_dir, project = "dc")
    wait_gone(APP)
    log("✓ devcontainer down stopped the helper and removed the deployment")
