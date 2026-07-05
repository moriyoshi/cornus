# The cornus server running AS A CONTAINER on the docker host it manages —
# the host-backend counterpart of the in-cluster install, and the one topology
# the rest of the suite cannot reach.
#
# Every other scenario runs the server as a host process, where it shares the
# daemon's filesystem: the paths it hands over mean the same thing on both sides
# and no translation is ever exercised. Containerize it and they diverge. The
# failure that matters is SILENT — given a path it cannot find, the daemon
# creates it and starts the workload, so the mount becomes an empty directory
# with no error anywhere. Asserting that a deploy "succeeds" would therefore
# prove nothing; this scenario asserts on the CONTENT that crosses.
#
# Needs: TARGET == "docker", a prebuilt cornus-embedding image
# (CORNUS_AGENT_IMAGE — e.g. "cornus:e2e", the same image the sidecar-mount
# scenarios use), and privileged Docker. Self-skips otherwise, matching
# deploy-mounts-sidecar-docker.star.
#
# Docker-only because the assertions below are docker-shaped (the Engine API's
# self-inspection, and the self-attach onto a user-defined network that has no
# analogue elsewhere), not because the other backends are unfinished. Their
# in-container topologies have their own scenarios now:
# server-in-container-containerd.star and server-in-container-incus.star.
#
# CAUTION when running this by hand: the SERVER under test is the one inside
# CORNUS_AGENT_IMAGE, not the `bin/cornus` that `make` rebuilds. A stale image
# silently exercises old server code while every other part of the run is
# current — which cost a mutation check that "passed" against a server that
# never had the mutation. Rebuild the image from the same tree (the containerized
# runner's prepare_docker_agent_image does this from $CORNUS_BIN):
#   go build -o /tmp/c/cornus ./cmd/cornus && \
#     cp e2e/container/appimage.Dockerfile /tmp/c/Dockerfile && \
#     docker build -t cornus:e2e /tmp/c

server_image = getenv("CORNUS_AGENT_IMAGE", "")

if TARGET != "docker":
    log("server-in-container: skipped (docker-only; exercises the server running as a container on its own docker host)")
elif server_image == "":
    log("server-in-container: skipped (set CORNUS_AGENT_IMAGE to a prebuilt cornus-embedding image, e.g. cornus:e2e)")
else:
    # --- what the preflight reports, before anything is deployed -------------
    #
    # The preflight is the operator's only warning that a set of binds is wrong,
    # so its two verdicts are worth pinning directly.

    data_dir = temp_dir()

    # (1) With the data dir bound, the server must work out that it is
    # containerized AND what its own paths look like on the host — by asking the
    # daemon about its own container, with no configuration at all.
    # CORNUS_DATA is stated explicitly because the bind destination and the
    # server's own data dir must be the same path, and only the image knows its
    # default — the minimal e2e image sets none and would fall back to an XDG
    # path under $HOME, which is precisely the kind of mismatch this whole
    # feature exists to surface.
    ok = docker(
        "run", "--rm",
        "-e", "CORNUS_DATA=/var/lib/cornus",
        "-v", "/var/run/docker.sock:/var/run/docker.sock",
        "-v", data_dir + ":/var/lib/cornus",
        server_image, "daemon", "preflight",
    )
    assert_contains(ok, "translating its paths for the runtime", "preflight must detect that this server's paths diverge from the runtime's")
    assert_contains(ok, data_dir, "preflight must resolve the data dir to its real path on the host")
    log("✓ preflight self-inspects: container detected, host path resolved with no configuration")

    # (2) Without it, the same image must say the data dir is invisible and name
    # the remedy — a warning on dockerhost, since only client-local mounts
    # depend on it and plain deploys keep working.
    warned = docker(
        "run", "--rm",
        "-e", "CORNUS_DATA=/var/lib/cornus",
        "-v", "/var/run/docker.sock:/var/run/docker.sock",
        server_image, "daemon", "preflight",
    )
    assert_contains(warned, "cannot see cornus's data dir", "preflight must report an unreachable data dir")
    assert_contains(warned, "CORNUS_HOST_PATH_MAP", "the warning must name the knob that fixes it")
    log("✓ preflight reports an unreachable data dir instead of leaving it to fail silently later")

    # --- the payload: does a client-local mount actually cross? --------------

    addr = serve_container(image = server_image)

    local = temp_dir()
    write_file(path = local + "/marker", content = "LIVE-9P-THROUGH-A-CONTAINERIZED-SERVER")

    deploy_attach(
        name = "inctr-mnt",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        local_mount = [local + ":/data:ro"],
        timeout = "240s",
    )

    # The assertion this scenario exists for. The bytes originate on the client,
    # travel over 9P to a server inside a container, get mounted there, and reach
    # a workload the HOST's daemon started — which only works if the server
    # translated the mountpoint into the host's spelling before handing it over.
    got = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "inctr-mnt", "cat", "/data/marker"])
    assert_contains(got["output"], "LIVE-9P-THROUGH-A-CONTAINERIZED-SERVER", "client-local mount content read from inside a workload deployed by a containerized server")
    log("✓ client-local mount crossed from the client, through a containerized server, into a host-daemon workload")

    # The bind the daemon was actually given must be the HOST path, not the
    # server's own. Checking the content above proves it works; checking the
    # bind proves it works for the RIGHT reason, and would catch a future change
    # that made it work by accident (e.g. by falling back to a host bind of the
    # pre-rewrite client source).
    binds = docker("inspect", "cornus-inctr-mnt-0", "--format", "{{range .Mounts}}{{.Source}}=>{{.Destination}} {{end}}")
    assert_contains(binds, "=>/data", "the workload must carry a bind at /data")
    assert_true("/var/lib/cornus/mounts" not in binds, "the daemon must be given the HOST path, not the server container's own /var/lib/cornus/... spelling")
    assert_contains(binds, "/mounts/", "the bind source must still be the server's mount session directory, translated")
    log("✓ the daemon was handed the translated host path: " + binds.strip())

    attach_stop(name = "inctr-mnt")
    remaining = docker("ps", "-a", "--filter", "label=cornus.app=inctr-mnt", "--format", "{{.ID}}")
    assert_eq(remaining.strip(), "", "workload must be gone after attach_stop")
    log("✓ torn down")

    # --- routing to a workload on a USER-DEFINED network ---------------------
    #
    # The deploy above landed on the default bridge, which the server container
    # is also on — so it proves nothing about routing. The moment a workload
    # declares `networks:` the two are on different bridge networks, and docker's
    # isolation chains drop traffic between them in BOTH directions. Measured on
    # docker 29.2.1: container-on-bridge -> container-on-user-network is
    # unreachable while host -> the same address is not, which is exactly why
    # every host-process target passes this and only a containerized server
    # fails it.
    #
    # No unit test can reach this: the isolation lives in the host's iptables,
    # not in the Engine API. What the fake daemon can prove is that cornus ASKS
    # to join the network (selfnet_test.go); only this can prove that joining is
    # what makes the bytes arrive.
    net_compose = "e2e/scenarios/server-in-container-net.yaml"
    compose_up(file = net_compose, detach = True)

    # serve_container() names its container after the port it published
    # (pkg/e2e/harness.go). Derived rather than returned, so nothing new enters
    # the builtin surface for one scenario; if that naming ever changes, the
    # `docker inspect` below fails loudly rather than quietly asserting nothing.
    server_ctr = "cornus-e2e-server-" + addr.split(":")[-1]

    # The mechanism, before the payload: the server's own container must now be a
    # member of the workload's network. Asserting only the GET below would keep
    # passing if some future change made it work by publishing a host port
    # instead, which is a different (and worse) thing.
    joined = docker("inspect", "--format", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", server_ctr)
    assert_contains(joined, "inctrnet_appnet", "the containerized server must attach itself to the workload's user-defined network")
    log("✓ the server joined the workload's network: " + joined.strip())

    # The payload: a port the workload never published, dialed by the server on
    # a network it had no route to until it joined.
    local = port_forward(name = "inctrnet-web", port = 80)
    r = http_get(url = "http://" + local + "/", retry = "30s")
    assert_eq(r["status"], 200, "port-forward to a user-defined-network workload through a containerized server (got %r)" % r["status"])
    assert_contains(r["body"], "nginx", "port-forward reached something other than the workload's :80")
    log("✓ port-forward crossed from the harness, through a containerized server, onto a user-defined network")

    # And the network must not leak. cornus's own endpoint keeps the network
    # non-empty, so `down` has to detach before it reaps — otherwise every
    # deploy/delete cycle in this mode strands a network and an endpoint.
    compose_down(file = net_compose)
    left = docker("network", "ls", "--filter", "name=inctrnet_appnet", "--format", "{{.Name}}")
    assert_eq(left.strip(), "", "the network the server joined must be reaped on down, not left behind by cornus's own endpoint")
    still = docker("inspect", "--format", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", server_ctr)
    assert_true("inctrnet_appnet" not in still, "the server must leave the workload's network when the deployment goes")
    log("✓ the server left the network and it was reaped: " + still.strip())
