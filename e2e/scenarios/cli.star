# Exercise the cornus CLI surface the harness normally bypasses (it drives the
# client library directly): `version`, `health`, `push` (load a local image
# tarball into the registry), and `deploy -f` + `--delete` against the local
# Docker host. Docker-only: uses the docker builtin + the local dockerhost path.

if TARGET != "docker":
    log("cli: skipped (docker-only; uses local docker + the dockerhost deploy path)")
else:
    addr = serve()

    # version prints something non-empty.
    v = cornus("version")
    assert_true(len(v) > 0, "version printed nothing")
    log("cornus version: " + v)

    # health probes /healthz on the running server and exits 0 (errors otherwise).
    cornus("health", "--addr", addr)
    log("✓ health probe OK")

    # push a local image tarball into the registry, then confirm its tag lists.
    work = temp_dir()
    docker("pull", "alpine:3.20")
    docker("save", "-o", work + "/alpine.tar", "alpine:3.20")
    cornus("push", work + "/alpine.tar", addr + "/e2e-push/alpine:v1")
    tags = http_get(url = "http://" + addr + "/v2/e2e-push/alpine/tags/list")
    assert_eq(tags["status"], 200, "pushed tag not listed")
    assert_contains(tags["body"], "v1")
    log("✓ cornus push (local tarball -> registry) OK")

    # deploy -f applies a spec to the local Docker host; the instance is named
    # cornus-<name>-0 with the standard labels.
    spec = work + "/spec.yaml"
    write_file(path = spec, content = 'name: clidep\nimage: alpine:3.20\ncommand: ["sh", "-c", "sleep infinity"]\n')
    cornus("deploy", "-f", spec)
    running = docker("inspect", "cornus-clidep-0", "--format", "{{.State.Running}}")
    assert_contains(running, "true")
    log("✓ cornus deploy -f (local dockerhost) OK")

    # --delete removes it. Use sh so a missing-container inspect doesn't error;
    # a non-zero inspect rc is the "gone" signal.
    cornus("deploy", "-f", spec, "--delete")
    chk = sh(cmd = "docker inspect cornus-clidep-0 >/dev/null 2>&1")
    assert_true(chk["code"] != 0, "container survived cornus deploy --delete")
    log("✓ cornus deploy --delete removed it")
