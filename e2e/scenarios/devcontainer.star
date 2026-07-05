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

devcontainer_dir = "e2e/scenarios/devcontainer"

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

    # up -d: the single-container devcontainer deploys as service "devcontainer";
    # its workspace bind mount is streamed over 9P by a background helper (never a
    # hostPath). -p dc pins the project so the deploy name is deterministic.
    devcontainer_up(dir = devcontainer_dir, project = "dc", detach = True)

    # The synthesized service reaches running under the project-qualified name.
    st = wait(name = "dc-devcontainer", running = 1, timeout = "240s")
    assert_eq(st["running"], 1, "devcontainer service not running")
    log("✓ devcontainer up: single-container service running with workspace 9P mount")

    # ps reports the service through the same code path Compose uses.
    ps = devcontainer_ps(dir = devcontainer_dir, project = "dc")
    assert_contains(ps, "devcontainer")
    log("✓ devcontainer ps reports the service")

    devcontainer_down(dir = devcontainer_dir, project = "dc")
    wait_gone("dc-devcontainer")
    log("✓ devcontainer down stopped the helper and removed the deployment")
