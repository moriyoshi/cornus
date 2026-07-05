# The REAL VS Code devcontainer toolchain against cornus: @devcontainers/cli
# (the engine VS Code's Dev Containers extension shells out to) drives
# `devcontainer up` / `devcontainer exec` with DOCKER_HOST pointed at the
# `cornus daemon docker` proxy. This is the headless equivalent of "open this
# repo in a VS Code devcontainer backed by cornus" — distinct from
# devcontainer.star, which tests cornus's OWN `cornus compose --devcontainer`
# translation.
#
# What it proves end to end: image inspect returns the real image config, the
# CLI's create-time Entrypoint override survives translation, `docker ps`
# label filters locate the container (the CLI's only lookup mechanism),
# postCreateCommand runs, the workspace bind mount is live read-write in BOTH
# directions, and `devcontainer exec` propagates exit codes.
#
# docker-target-only. Also self-skips unless root + 9p: the proxy's start rides
# the deploy-attach path, whose caller-local mounts the dockerhost side realizes
# with a kernel 9p mount (euid 0 + 9p in /proc/filesystems). The privileged CI
# container runner satisfies both; unprivileged local runs skip.

def wait_gone(name, steps = 90):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed after docker rm" % name)

skip = ""
if TARGET != "docker":
    skip = "docker-only; drives @devcontainers/cli against the dockerd proxy"
else:
    r = sh(cmd = "id -u")
    if r["output"] != "0":
        skip = "needs root (dockerhost kernel-9p-mounts the caller-local workspace bind)"
    elif "9p" not in read_file(path = "/proc/filesystems").split():
        skip = "needs 9p filesystem support in the kernel (modprobe 9p)"

if skip != "":
    log("devcontainer-vscode: skipped (%s)" % skip)
else:
    serve()
    host = dockerd_up()

    # A temp workspace with the fixture .devcontainer: postCreate writes into
    # the workspace, so the committed tree must stay untouched.
    ws = temp_dir()
    r = sh(cmd = "cp -r e2e/scenarios/devcontainer-vscode/.devcontainer " + ws + "/")
    assert_eq(r["code"], 0, "copy fixture .devcontainer into the temp workspace")
    log("workspace: " + ws)

    # `devcontainer up`: image inspect (real config), create (Entrypoint +
    # labels + workspace bind), start (deploy-attach on the server), then
    # postCreateCommand via docker exec. The CLI reports a JSON outcome line.
    out = devcontainer_cli("up", "--workspace-folder", ws)
    assert_contains(out, '"outcome":"success"', "devcontainer up did not succeed")
    log("✓ devcontainer up succeeded against the cornus docker proxy")

    # The CLI's own lookup mechanism: ps filtered by the devcontainer.local_folder
    # label (exercises the modern map-form filter encoding end to end). The
    # server-side deploy name comes from inspect (an unnamed proxy create
    # deploys as cornus-<short id>, and inspect's Name reports it).
    r = sh(cmd = "DOCKER_HOST=" + host + " docker ps -q --filter label=devcontainer.local_folder=" + ws)
    cid = r["output"]
    assert_true(cid != "", "no container found by devcontainer.local_folder label filter")
    dep = sh(cmd = "DOCKER_HOST=" + host + " docker inspect --format {{.Name}} " + cid)["output"].strip("/")
    st = wait(name = dep, running = 1, timeout = "60s")
    assert_eq(st["running"], 1, "devcontainer workload not running on the server")
    log("✓ label-filter lookup found the container; deploy %s running on the server" % dep)

    # container -> host: postCreateCommand wrote into the workspace inside the
    # container; the file must appear in the host dir through the live mount.
    marker = read_file(path = ws + "/postcreate.txt", default = "MISSING")
    assert_contains(marker, "POSTCREATE_OK", "postCreate marker did not reach the host workspace")
    log("✓ postCreateCommand ran; write is visible on the host (container->host)")

    # host -> container: a file written on the host is readable in the container
    # via `devcontainer exec` (which resolves the container by label itself).
    write_file(path = ws + "/from-host.txt", content = "HOST_TO_CONTAINER")
    out = devcontainer_cli("exec", "--workspace-folder", ws, "cat", "/workspaces/app/from-host.txt")
    assert_contains(out, "HOST_TO_CONTAINER", "host write not visible inside the container")
    log("✓ host write visible inside the container (host->container)")

    # containerEnv from devcontainer.json reaches the exec environment.
    out = devcontainer_cli("exec", "--workspace-folder", ws, "printenv", "CORNUS_E2E_MARKER")
    assert_contains(out, "vscode-dc", "containerEnv did not reach the container")
    log("✓ containerEnv visible via devcontainer exec")

    # Exit-code propagation through CLI -> proxy exec tunnel -> exec-inspect.
    # sh() captures the rc without failing the scenario.
    r = sh(cmd = "DOCKER_HOST=" + host + " devcontainer exec --workspace-folder " + ws + " sh -c 'exit 7'")
    assert_eq(r["code"], 7, "devcontainer exec exit code not propagated")
    log("✓ devcontainer exec propagated exit code 7")

    # Teardown: the devcontainer CLI has no `down`; VS Code removes the container
    # through the daemon, so mirror that with docker rm -f via the proxy.
    docker("-H", host, "rm", "-f", cid)
    wait_gone(dep)
    log("✓ docker rm -f tore the devcontainer workload down on the server")
