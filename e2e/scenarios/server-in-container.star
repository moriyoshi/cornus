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
# Needs: TARGET == "docker" (dockerhost is where in-container mode is finished;
# containerd still needs host networking and CNI plugins in the image), a
# prebuilt cornus-embedding image (CORNUS_AGENT_IMAGE — e.g. "cornus:e2e", the
# same image the sidecar-mount scenarios use), and privileged Docker. Self-skips
# otherwise, matching deploy-mounts-sidecar-docker.star.
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
