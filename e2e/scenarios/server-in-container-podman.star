# The cornus server running AS A CONTAINER on the podman host it manages.
#
# The docker counterpart is server-in-container.star and the containerd one is
# server-in-container-containerd.star. This one exists because podman's
# self-identification fails in a way neither of those can catch.
#
# Docker sets a container's HOSTNAME to its own short container id, so a
# containerized cornus can identify itself from $HOSTNAME alone. **Podman
# generates a random hostname unrelated to the id** — measured on 5.8.2, a
# container with id 3cde70d7... had hostname 3958b35611d1. Every hostname-derived
# candidate is therefore rejected by the daemon as "no such container", and the
# only remaining evidence is the per-container bind in /proc/self/mountinfo,
# which podman spells differently again:
#
#	docker:  /var/lib/docker/containers/<id>/hosts
#	podman:  /…/containers/overlay-containers/<id>/userdata/hosts
#
# Until that second spelling was matched, a containerized cornus on podman could
# not identify itself AT ALL. What it lost is not cosmetic: without a confirmed
# id it falls back to assuming its paths match the runtime's, and it will not
# attach itself to a workload's network — so on a rootless daemon it also loses
# the direct route to workloads that this very topology exists to provide.
#
# The assertions are on the PREFLIGHT, as in the containerd scenario and for the
# same reason: its verdict is the operator's only warning, and the failure it
# guards is otherwise silent — a server that starts, reports healthy, and hands
# podman paths that mean something else.
#
# Needs: TARGET == "podman", the podman CLI, and a reachable API socket. Self-
# skips otherwise, naming which precondition failed.

if TARGET != "podman":
    log("server-in-container-podman: skipped (podman-only)")
else:
    probe_image = "docker.io/library/alpine:3.20"
    sock = getenv("CORNUS_PODMAN_SOCKET")

    if sock == "":
        log("server-in-container-podman: skipped (no CORNUS_PODMAN_SOCKET; the backend does not guess one and neither does this scenario)")
    else:
        # Preconditions reported separately: a rate-limited pull and a podman that
        # cannot start a container are different problems, and conflating them has
        # already cost a live re-run in the containerd counterpart.
        pull = sh(cmd = "podman pull -q " + probe_image + " 2>&1 | tail -2")
        have = sh(cmd = "podman images -q " + probe_image + " 2>/dev/null | grep -c . || true")

        if have["output"] == "0":
            log("server-in-container-podman: skipped (no probe image; the pull said: " + pull["output"] + ")")
        else:
            canary = sh(cmd = "podman run --rm " + probe_image + " /bin/true 2>&1")
            if canary["code"] != 0:
                log("server-in-container-podman: skipped (this podman cannot start a container here: " + canary["output"] + ")")
            else:
                data_dir = temp_dir()

                # One podman invocation, assembled per case. Each bind is the subject
                # of a case, so each is named separately and left out to prove what it
                # buys.
                def preflight(name, net_host = True, bind_socket = True, bind_data = True, env = ""):
                    argv = ["podman", "run", "--rm", "--privileged", "--name", name]
                    if net_host:
                        argv.append("--network host")
                    # The binary under test, not whatever the probe image holds.
                    argv.append("-v " + CORNUS_BIN + ":/usr/local/bin/cornus:ro")
                    if bind_socket:
                        argv.append("-v " + sock + ":/run/podman.sock:rw")
                        argv.append("-e CORNUS_PODMAN_SOCKET=/run/podman.sock")
                    if bind_data:
                        argv.append("-v " + data_dir + ":/var/lib/cornus:rw")
                    argv.append("-e CORNUS_DEPLOY_BACKEND=podman")
                    argv.append("-e CORNUS_DATA=/var/lib/cornus")
                    if env != "":
                        argv.append("-e " + env)
                    argv.append(probe_image)
                    argv.append("/usr/local/bin/cornus daemon preflight")
                    return sh(cmd = " ".join(argv) + " 2>&1")

                # --- 1. the supported configuration -------------------------------
                #
                # No CORNUS_HOST_PATH_MAP. The claim is that the server works out its
                # own host paths by asking the podman it is about to deploy through.
                ok = preflight("sipp-ok")
                assert_eq(ok["code"], 0, "the fully-bound configuration must pass the preflight: " + ok["output"])
                assert_contains(
                    ok["output"], "on a podman host",
                    "the preflight must name podman as the runtime it checked, not docker: " + ok["output"],
                )
                log("✓ preflight identifies the podman runtime")

                # --- 2. self-identification, the regression this file is for -------
                #
                # "runs in a container (<id>)" is printed only when a candidate id was
                # CONFIRMED against the daemon. On podman that can only have come from
                # the mountinfo miner, because the hostname is not the id — so this
                # single assertion is the guard for the whole divergence.
                assert_contains(
                    ok["output"], "runs in a container (",
                    "cornus could not identify its own container on podman: " + ok["output"] + "\n" +
                    "podman's hostname is NOT the container id, so this depends entirely on the " +
                    "/…/containers/overlay-containers/<id>/ spelling in /proc/self/mountinfo",
                )
                log("✓ self-identification works despite podman's hostname not being the container id")

                # --- 3. without the socket, it cannot ask ---------------------------
                #
                # The failure is the point: unable to confirm an id, cornus assumes its
                # paths match podman's and every volume, mount and log path silently
                # refers to somewhere else.
                no_sock = preflight("sipp-nosock", bind_socket = False)
                assert_true(
                    "runs in a container (" not in no_sock["output"],
                    "without the API socket cornus must NOT claim a confirmed container id: " + no_sock["output"],
                )
                log("✓ without the socket bind, self-identification correctly reports that it could not confirm")
