# Deploy-spec configuration surface — env vars, published ports, host bind
# mounts (read-only AND read-write), command override, and restart policy —
# verified end to end through the real dockerhost backend. Docker-only: it
# http_get's a published host port and introspects/execs the created containers
# with the `docker` builtin, both of which assume the deploy backend is the local
# Docker host. Uses public images (no build), so it needs only a reachable Docker
# daemon.

# wait_file polls a host file until it contains want (a container writes it at
# startup, so there's a brief window before it lands).
def wait_file(path, want, steps = 30):
    for _ in range(steps):
        got = read_file(path = path, default = "")
        if want in got:
            return got
        sleep(duration = "1s")
    fail(msg = "timed out: %s never contained %s" % (path, want))

# wait_logs polls a container's logs until they contain want.
def wait_logs(container, want, steps = 30):
    for _ in range(steps):
        out = docker("logs", container)
        if want in out:
            return out
        sleep(duration = "1s")
    fail(msg = "timed out: logs of %s never contained %s" % (container, want))

if TARGET != "docker":
    log("deploy-config: skipped (docker-only; publishes a host port and inspects containers)")
else:
    serve()

    # --- env + ports + read-only host bind mount, proven live on one workload -
    # A local dir served read-only into nginx's docroot, published on a host
    # port. A successful HTTP fetch of the marker proves the port binding AND the
    # host mount are both live; docker inspect confirms the env + restart policy.
    # temp_dir() is 0755 (not mktemp's 0700) precisely so nginx's non-root worker
    # can traverse the docroot dir to serve index.html (else it 403s).
    htmldir = temp_dir()
    write_file(path = htmldir + "/index.html", content = "DEPLOY-CFG-MOUNT-OK")

    deploy(
        name = "cfg",
        image = "nginx:1.27-alpine",
        ports = ["18080:80"],
        env = {"CORNUS_E2E": "env-ok"},
        mounts = [htmldir + ":/usr/share/nginx/html:ro"],
        restart = "always",
    )
    st = wait(name = "cfg", running = 1, timeout = "180s")
    assert_eq(st["running"], 1, "cfg not running")

    # Published port + host mount, live over the network.
    resp = http_get(url = "http://127.0.0.1:18080/")
    assert_eq(resp["status"], 200, "published port did not serve")
    assert_contains(resp["body"], "DEPLOY-CFG-MOUNT-OK")
    log("✓ published port + read-only host bind mount served live over HTTP")

    # The read-only mount must actually reject writes from inside the container
    # (the sh always exits 0 so the docker exec itself succeeds; we assert on the
    # printed outcome).
    ro = docker("exec", "cornus-cfg-0", "sh", "-c",
                "echo x > /usr/share/nginx/html/probe 2>/dev/null && echo WROTE || echo RO-DENIED")
    assert_contains(ro, "RO-DENIED")
    log("✓ read-only host mount rejects writes from inside the container")

    # Env var landed in the container config.
    env = docker("inspect", "cornus-cfg-0", "--format", "{{json .Config.Env}}")
    assert_contains(env, "CORNUS_E2E=env-ok")
    log("✓ env var present in the container")

    # Port binding + restart policy configured as requested.
    binds = docker("inspect", "cornus-cfg-0", "--format", "{{json .HostConfig.PortBindings}}")
    assert_contains(binds, "18080")
    policy = docker("inspect", "cornus-cfg-0", "--format", "{{.HostConfig.RestartPolicy.Name}}")
    assert_eq(policy, "always", "restart policy not applied")
    log("✓ port binding + restart policy applied")

    remove(name = "cfg")

    # --- read-write host bind mount: a container write reaches the host --------
    rwdir = temp_dir()
    deploy(
        name = "cfgrw",
        image = "alpine:3.20",
        command = ["sh", "-c", "echo WROTE-FROM-CONTAINER > /data/marker && sleep infinity"],
        mounts = [rwdir + ":/data"],  # no :ro -> read-write
    )
    wait(name = "cfgrw", running = 1, timeout = "120s")
    # The container's write must surface in the host dir (proves the rw bind).
    wait_file(rwdir + "/marker", "WROTE-FROM-CONTAINER")
    log("✓ read-write host bind mount: container write visible on the host")
    remove(name = "cfgrw")

    # --- command override actually replaces the image default -----------------
    # The rw case above already proved the override runs; here we also confirm it
    # lands verbatim in the container config.
    deploy(
        name = "cmd",
        image = "alpine:3.20",
        command = ["sh", "-c", "echo CMD-OVERRIDE-OK && sleep infinity"],
    )
    wait(name = "cmd", running = 1, timeout = "120s")
    cfg = docker("inspect", "cornus-cmd-0", "--format", "{{json .Config.Cmd}}")
    assert_contains(cfg, "CMD-OVERRIDE-OK")
    wait_logs("cornus-cmd-0", "CMD-OVERRIDE-OK")
    log("✓ command override applied and executed")

    remove(name = "cmd")
    log("torn down")
