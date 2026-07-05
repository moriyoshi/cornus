# `cornus daemon docker`: the Docker Engine API proxy. Point stock `docker` at the
# proxy's socket and container operations become cornus deploys on the running
# server. This drives the proxy end to end — run, ps, inspect, logs, stats, cp,
# stop, rm — and cross-checks each step against the cornus server via the
# harness client. Docker-only: the server must use the dockerhost backend, and we
# shell out to a real `docker` CLI pointed at the proxy.

def wait_gone(name, steps = 90):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed on the server" % name)

if TARGET != "docker":
    log("dockerd: skipped (docker-only; drives real docker against the proxy)")
else:
    serve()
    host = dockerd_up()

    # `docker run -d` through the proxy -> a cornus deploy on the server. The
    # container prints a marker to stdout first so `docker logs` has content.
    docker("-H", host, "run", "-d", "--name", "dproxy", "alpine:3.20", "sh", "-c", "echo HELLO_FROM_PROXY; sleep infinity")

    # The cornus server sees the deployment the proxy created.
    st = wait(name = "dproxy", running = 1, timeout = "180s")
    assert_eq(st["running"], 1, "proxy-created deployment not running on the server")
    log("✓ docker run via proxy -> cornus deploy running")

    # `docker ps` / `docker inspect` via the proxy report the synthesized record.
    ps = docker("-H", host, "ps")
    assert_contains(ps, "dproxy")
    ins = docker("-H", host, "inspect", "dproxy", "--format", "{{.State.Running}}")
    assert_contains(ins, "true")
    log("✓ docker ps + inspect via proxy OK")

    # `docker logs` via the proxy streams the container's stdout back
    # (GET /containers/{id}/logs -> dockerhost pass-through of Docker's stdcopy stream).
    logs = docker("-H", host, "logs", "dproxy")
    assert_contains(logs, "HELLO_FROM_PROXY")
    log("✓ docker logs via proxy returned the container's stdout")

    # `docker stats --no-stream` via the proxy returns one metrics snapshot
    # (GET /containers/{id}/stats?stream=0 -> dockerhost pass-through). Assert on
    # the format-invariant header rather than a mapped name/id.
    stats = docker("-H", host, "stats", "--no-stream", "dproxy")
    assert_contains(stats, "CPU")
    log("✓ docker stats --no-stream via proxy returned a metrics row")

    # `docker cp` round-trip via the proxy exercises the container archive
    # endpoint both ways: PUT (host->container) then GET (container->host), with
    # the HEAD stat the CLI issues in between.
    tmp = temp_dir()
    write_file(path = tmp + "/in.txt", content = "CP_PAYLOAD")
    docker("-H", host, "cp", tmp + "/in.txt", "dproxy:/tmp/cptest.txt")
    docker("-H", host, "cp", "dproxy:/tmp/cptest.txt", tmp + "/out.txt")
    back = read_file(path = tmp + "/out.txt")
    assert_eq(back, "CP_PAYLOAD", "docker cp round-trip payload mismatch")
    log("✓ docker cp round-trip (PUT then GET) via proxy preserved the file")

    # `docker exec` (non-TTY) via the proxy: create the exec, hijack-tunnel its
    # stdio through the server to the real container, stream stdout back. Drives
    # the 200 raw-stream hijack path (interactive -it / attach need a PTY the
    # shell-out harness can't provide; they share this same tunnel + bridge code).
    ex = docker("-H", host, "exec", "dproxy", "echo", "EXEC_MARKER")
    assert_contains(ex, "EXEC_MARKER")
    log("✓ docker exec via proxy ran a command and streamed its stdout")

    # Exit-code propagation: exec-inspect must surface the process exit status so
    # the docker CLI exits with it. sh() captures the rc without failing.
    ec = sh(cmd = "docker -H '" + host + "' exec dproxy sh -c 'exit 5'")
    assert_eq(ec["code"], 5, "docker exec exit code not propagated")
    log("✓ docker exec exit code propagated via exec-inspect")

    # `docker stop` through the proxy tears the deployment down on the server.
    docker("-H", host, "stop", "dproxy")
    wait_gone("dproxy")
    log("✓ docker stop via proxy removed the workload from the server")

    # `docker rm` clears the proxy's own record.
    docker("-H", host, "rm", "dproxy")
    after = docker("-H", host, "ps", "-a")
    assert_true("dproxy" not in after, "container record survived docker rm")
    log("✓ docker rm via proxy cleared the record")

    # `docker compose up -d` / `ps` / `down` through the proxy. Compose issues the
    # network + per-service create/start calls the proxy fakes/handles; each service
    # container (dcomp-web-1) becomes a cornus deploy of the same name. The compose
    # plugin ignores the -H flag, so docker_compose() selects the proxy via
    # DOCKER_HOST instead. This closes the last dockerd-proxy E2E gap.
    compose_file = "e2e/scenarios/dockerd-compose.yaml"
    docker_compose("-f", compose_file, "-p", "dcomp", "up", "-d")
    cst = wait(name = "dcomp-web-1", running = 1, timeout = "180s")
    assert_eq(cst["running"], 1, "compose service dcomp-web-1 not running on the server")
    log("✓ docker compose up -d via proxy -> cornus deploy running")

    # `compose ps` lists the project's service by its container name.
    cps = docker_compose("-f", compose_file, "-p", "dcomp", "ps")
    assert_contains(cps, "dcomp-web-1", "compose ps did not list the service")
    log("✓ docker compose ps via proxy listed the service")

    # `compose down` stops + removes the service, tearing the deployment down.
    docker_compose("-f", compose_file, "-p", "dcomp", "down")
    wait_gone("dcomp-web-1")
    log("✓ docker compose down via proxy removed the deployment")

    # `docker exec -i -t` through the proxy under a REAL PTY: the non-TTY exec block
    # above shares the hijack tunnel, but only a PTY-backed session drives the
    # interactive `-it` path and the resize forwarding. `dproxy` was docker-rm'ed
    # earlier, so create a fresh `dproxy2` to exec into. `stty size` prints the remote
    # TTY's "<rows> <cols>"; seeing "30 120" proves the proxy forwarded the PTY window
    # size to POST /exec/{id}/resize on the way to the container. (exec_tty answers the
    # shell's startup ESC[6n cursor-position query the way a real terminal does, so
    # busybox ash under TERM=xterm does not block on it.)
    docker("-H", host, "run", "-d", "--name", "dproxy2", "alpine:3.20", "sleep", "infinity")
    wait(name = "dproxy2", running = 1, timeout = "180s")
    it = exec_tty(
        argv = ["docker", "-H", host, "exec", "-i", "-t", "dproxy2", "sh"],
        input = "stty size; echo DOCKER_TTY_OK; exit\n",
        rows = 30,
        cols = 120,
    )
    assert_contains(it["output"], "DOCKER_TTY_OK", "interactive docker exec via proxy did not run the command")
    assert_contains(it["output"], "30 120", "PTY window size did not reach the container (proxy resize forwarding)")
    log("✓ `docker exec -it` via proxy: interactive TTY session ran and the 30x120 window size was forwarded")

    # Clean up the interactive-exec container.
    docker("-H", host, "stop", "dproxy2")
    docker("-H", host, "rm", "dproxy2")
    log("✓ docker exec interactive cleanup done")

    # `docker compose up -d --scale web=2` through the proxy: compose creates TWO
    # numbered service containers (dscale-web-1 / dscale-web-2), each becoming its
    # own cornus deploy.
    docker_compose("-f", compose_file, "-p", "dscale", "up", "-d", "--scale", "web=2")
    wait(name = "dscale-web-1", running = 1, timeout = "180s")
    wait(name = "dscale-web-2", running = 1, timeout = "180s")
    names = docker("-H", host, "ps", "--filter", "name=dscale-web", "--format", "{{.Names}}")
    assert_contains(names, "dscale-web-1", "docker ps did not list dscale-web-1")
    assert_contains(names, "dscale-web-2", "docker ps did not list dscale-web-2")
    log("✓ docker compose up -d --scale web=2 via proxy -> two instances running")

    # Reconverge with a second `up -d --scale web=1`: the recreate/scale-diff
    # path. This used to crash on two proxy gaps, both fixed in pkg/dockerproxy:
    # (a) the faked networks now store and echo create-time labels, so compose
    # reuses the compose-owned network instead of failing with "incorrect label
    # com.docker.compose.network"; (b) the container LIST JSON now carries
    # NetworkSettings.Networks, which compose v5's convergence
    # (checkExpectedNetworks, convergence.go) dereferences when diffing a
    # running container. Compose keeps the lowest-numbered replica and removes
    # the surplus dscale-web-2.
    docker_compose("-f", compose_file, "-p", "dscale", "up", "-d", "--scale", "web=1")
    wait(name = "dscale-web-1", running = 1, timeout = "180s")
    wait_gone("dscale-web-2")
    names = docker("-H", host, "ps", "--filter", "name=dscale-web", "--format", "{{.Names}}")
    assert_contains(names, "dscale-web-1", "docker ps did not list dscale-web-1 after scale-down")
    assert_true("dscale-web-2" not in names, "dscale-web-2 still listed after scale-down to 1")
    log("✓ docker compose up -d --scale web=1 reconverged (surplus instance removed)")

    # `down` tears the project down: the remaining instance is removed from the
    # server and no dscale record survives in the proxy.
    docker_compose("-f", compose_file, "-p", "dscale", "down")
    wait_gone("dscale-web-1")
    wait_gone("dscale-web-2")
    names = docker("-H", host, "ps", "-a", "--filter", "name=dscale-web", "--format", "{{.Names}}")
    assert_true("dscale-web-1" not in names, "dscale-web-1 record survived compose down")
    assert_true("dscale-web-2" not in names, "dscale-web-2 record survived compose down")
    log("✓ docker compose down tore the scaled project down (no instances left)")
