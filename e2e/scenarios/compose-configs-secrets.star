# Prove file-based Compose `configs:` and `secrets:` are actually materialized
# inside the running container at their default mount paths:
#   - a config -> /<config-name>            (here /app_cfg)
#   - a secret -> /run/secrets/<secret-name> (here /run/secrets/db_pw)
# The sources are files next to the compose file. `cornus compose` realises ALL
# bind mounts — including config/secret file grants — over the client-local 9P
# deploy-attach path, so the server host kernel-9p-mounts them: that needs root +
# 9p (exactly like the dockerhost caller-local bind in devcontainer-vscode.star).
# The privileged CI container runner satisfies both; an unprivileged local run
# skips. If a mount is present but missing/empty in the container, that is a real
# implementation bug — the assertions fail loud rather than being weakened.

skip = ""
if TARGET == "local":
    skip = "needs a real backend"
elif TARGET == "kube":
    # File-based configs/secrets bind a SINGLE file. The kube mount sidecar
    # propagates a 9P DIRECTORY mount into the app container via a shared
    # emptyDir; it cannot place one file at an arbitrary rootfs target (e.g. the
    # config default path /<name> at the filesystem root) the way a container
    # runtime's file bind can. The server rejects such mounts on the kube backend
    # (rejectFileMounts) — so this scenario is dockerhost-only.
    skip = "single-file config/secret binds are not realizable over the kube 9P mount sidecar (directory-propagation only)"
elif TARGET == "docker":
    if sh(cmd = "id -u")["output"] != "0":
        skip = "needs root (cornus compose realises config/secret binds as caller-local 9P mounts; dockerhost kernel-9p-mounts them)"
    elif "9p" not in read_file(path = "/proc/filesystems").split():
        skip = "needs 9p filesystem support in the kernel (modprobe 9p)"

if skip != "":
    log("compose-configs-secrets: skipped (%s)" % skip)
else:
    compose_file = "e2e/scenarios/compose-configs-secrets.yaml"

    addr = serve()
    host = {"CORNUS_HOST": "http://" + addr}
    srv = "http://" + addr

    cornus("compose", "-f", compose_file, "up", "-d", env = host)
    wait(name = "ccs-app", running = 1, timeout = "120s")

    def exec_out(cmd):
        r = exec_tty(argv = ["cornus", "exec", "--server", srv, "ccs-app", "sh", "-c", cmd])
        return r["output"]

    # config default mount path /<name>.
    assert_contains(
        exec_out("cat /app_cfg"),
        "CONFIG_CONTENT_OK",
        "config app_cfg not mounted at /app_cfg with its file content",
    )
    log("✓ config mounted at /app_cfg")

    # secret default mount path /run/secrets/<name>.
    assert_contains(
        exec_out("cat /run/secrets/db_pw"),
        "SECRET_CONTENT_OK",
        "secret db_pw not mounted at /run/secrets/db_pw with its file content",
    )
    log("✓ secret mounted at /run/secrets/db_pw")

    cornus("compose", "-f", compose_file, "down", env = host)
    assert_eq(status(name = "ccs-app")["total"], 0, "ccs-app still present after down")
    log("✓ compose configs + secrets are materialized in the container")
