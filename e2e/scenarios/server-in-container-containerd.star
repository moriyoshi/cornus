# The cornus server running AS A CONTAINER on the containerd host it manages.
#
# The docker counterpart is server-in-container.star. This one exists because the
# containerd topology is configured entirely differently and each of its three
# requirements fails in its own way — two of them silently before the work this
# scenario guards:
#
#   * the SOCKET bind is what makes self-inspection possible at all. Without it
#     cornus cannot ask which container it is, falls back to the identity mapper —
#     whose ToHost never fails — and hands containerd its own container-private
#     paths. containerd then creates each one fresh and empty on the host: volumes
#     with no data, no managed /etc/hosts, and a log shim that is never staged, so
#     `cornus logs` returns nothing for a perfectly healthy container.
#   * the /run/cornus bind is what lets containerd RESOLVE the network-namespace
#     pins cornus creates. /run is a container-private tmpfs; measured, a file
#     created there inside this container is invisible from containerd's mount
#     namespace, so without the bind every deploy fails — late, after the image
#     pull and after the previous healthy deployment has been torn down.
#   * --net-host is what puts the CNI bridge and the portmap DNAT on the host
#     instead of inside this container. Without it a deploy SUCCEEDS and reports
#     its ports published, and the host sees nothing on them.
#
# So the assertions here are on the PREFLIGHT: its verdict and its exit code are
# the operator's only warning for all three, and each is a startup message rather
# than a failed deploy precisely because two of them are otherwise silent.
#
# Deliberately NOT asserted here: that a deploy through a containerized containerd
# server works end to end. That needs iptables inside the server image (the bridge
# conflist sets ipMasq and portmap realizes `ports:`, and both shell out to it) —
# the RELEASED image ships it, but the minimal alpine probe image below does not.
# The released image's own build asserts the plugins and iptables are present.
#
# Needs: TARGET == "containerd", `ctr` on PATH, and a containerd that can start a
# task. Self-skips otherwise, naming which precondition failed.

if TARGET != "containerd":
    log("server-in-container-containerd: skipped (containerd-only)")
else:
    ctr = "ctr --address /run/containerd/containerd.sock"
    # The nested-overlay guard entrypoint.sh applies to the backend applies to ctr
    # too: /var/lib sits on the runner's overlayfs and the kernel rejects
    # overlay-upon-overlay, so the copy-based native snapshotter is the only one
    # that can unpack here.
    snap = "--snapshotter native"
    probe_image = "docker.io/library/alpine:3.20"

    # Two preconditions, reported SEPARATELY, because conflating them has already
    # misled once: this scenario blamed "cannot start a task" for what was actually a
    # Docker Hub 429 on the pull, and the real diagnosis took a live re-run to find.
    #
    # (1) the probe image. Pulled twice on purpose: containerd 2.x refuses
    # `pull --snapshotter native` for an image it has never fetched ("no unpack
    # platforms defined"), so the default pull populates the content store and the
    # second unpacks it for native. Errors are SHOWN, not swallowed.
    pull = sh(cmd = ctr + " images pull " + probe_image + " 2>&1 | tail -2")
    pull2 = sh(cmd = ctr + " images pull " + snap + " " + probe_image + " 2>&1 | tail -2")
    have = sh(cmd = ctr + " images ls -q 2>/dev/null | grep -c . || true")

    if have["output"] == "0":
        # An offline or rate-limited runner lands here. Anonymous Docker Hub pulls are
        # throttled, and `ctr images import --snapshotter native` cannot substitute a
        # build-time-staged archive: it fails with "no unpack platforms defined"
        # whatever --platform/--all-platforms it is given, and the native snapshotter
        # is not optional here (/var/lib sits on the runner's overlayfs, and the
        # kernel rejects overlay-upon-overlay). See TODO.md.
        log("server-in-container-containerd: skipped (no probe image available; the pull said: " + pull["output"] + " / " + pull2["output"] + ")")
    else:
        # (2) can this containerd start a task at all? A nested runner whose cgroup
        # root still holds processes cannot delegate controllers, and runc then fails
        # with "cannot enter cgroupv2 ... with domain controllers" or a missing
        # cpu.weight. entrypoint.sh's prepare_cgroup_nesting fixes that; this is the
        # guard for a runner that did not run it.
        canary = sh(cmd = ctr + " run --rm " + snap + " --net-host " + probe_image + " sicanary /bin/true 2>&1")
        if canary["code"] != 0:
            log("server-in-container-containerd: skipped (this containerd cannot start a task here: " + canary["output"] + ")")
        else:
            data_dir = temp_dir()
            netns_dir = temp_dir()

            # One ctr invocation, assembled per case. The binds are the subject of the
            # test, so each is named separately and left out to prove what it buys.
            def preflight(name, net_host = True, bind_socket = True, bind_data = True, bind_netns = True, env = ""):
                argv = [ctr, "run", "--rm", snap, "--privileged"]
                if net_host:
                    argv.append("--net-host")
                # The binary under test, not whatever the probe image happens to hold —
                # see the CORNUS_BIN note in pkg/e2e/harness.go.
                argv.append("--mount type=bind,src=" + CORNUS_BIN + ",dst=/usr/local/bin/cornus,options=rbind:ro")
                if bind_socket:
                    argv.append("--mount type=bind,src=/run/containerd/containerd.sock,dst=/run/containerd/containerd.sock,options=rbind:rw")
                if bind_data:
                    argv.append("--mount type=bind,src=" + data_dir + ",dst=/var/lib/cornus,options=rbind:rw")
                if bind_netns:
                    argv.append("--mount type=bind,src=" + netns_dir + ",dst=/run/cornus/netns,options=rbind:rw")
                argv.append("--env CORNUS_DEPLOY_BACKEND=containerd")
                argv.append("--env CORNUS_DATA=/var/lib/cornus")
                if env != "":
                    argv.append("--env " + env)
                argv.append(probe_image)
                argv.append(name)
                argv.append("/usr/local/bin/cornus daemon preflight")
                return sh(cmd = " ".join(argv) + " 2>&1")

            # --- 1. the supported configuration: everything bound, host networking ---
            #
            # No CORNUS_HOST_PATH_MAP anywhere. That is the claim: the server works out
            # its own host paths by asking the containerd it is about to deploy through,
            # exactly as the docker one asks the Engine API.
            ok = preflight("sipf-ok")
            assert_eq(ok["code"], 0, "the fully-bound configuration must pass the preflight: " + ok["output"])
            assert_contains(ok["output"], "translating its paths for the runtime", "the server must detect that its paths diverge from containerd's, with no CORNUS_HOST_PATH_MAP set")
            assert_contains(ok["output"], data_dir, "the data dir must be resolved to its real path on the host")
            assert_contains(ok["output"], "sharing the host's network namespace", "with --net-host the netns check must reach its OK branch")
            log("✓ self-inspection: container identified, host data dir resolved, host networking confirmed — no configuration at all")

            # --- 2. no --net-host: PROVEN isolated netns, so the server must refuse ---
            #
            # A published port would be DNAT'd inside this container and be invisible on
            # the host while the deploy reported success. Before containerd
            # self-inspection this branch was unreachable outside unit tests, because
            # only Docker could answer the question.
            own_netns = preflight("sipf-ownnet", net_host = False)
            assert_true(own_netns["code"] != 0, "a proven isolated netns must fail the preflight, not warn: " + own_netns["output"])
            assert_contains(own_netns["output"], "own network namespace", "the failure must name the network namespace as the cause")
            assert_contains(own_netns["output"], "--network host", "the failure must name the remedy")
            log("✓ an isolated network namespace is refused up front, not discovered as an unreachable published port")

            # --- 3. ...unless the operator says they want it anyway ---
            acked = preflight("sipf-acked", net_host = False, env = "CORNUS_HOST_NETWORK=0")
            assert_eq(acked["code"], 0, "a DECLARED isolated netns is acknowledged, not refused: " + acked["output"])
            assert_contains(acked["output"], "CORNUS_HOST_NETWORK", "the warning should name the declaration that downgraded it")
            log("✓ CORNUS_HOST_NETWORK=0 turns the refusal into an acknowledged warning")

            # --- 4. no /run/cornus bind: containerd cannot resolve the netns pins ---
            no_netns = preflight("sipf-nonetns", bind_netns = False)
            assert_true(no_netns["code"] != 0, "an unresolvable netns pin directory must fail the preflight: " + no_netns["output"])
            assert_contains(no_netns["output"], "netns", "the failure must name the netns directory")
            log("✓ a netns pin directory containerd cannot see is refused before the first deploy destroys the previous one")

            # --- 5. no data-dir bind: the silent-empty-volume case, still fatal ---
            no_data = preflight("sipf-nodata", bind_data = False)
            assert_true(no_data["code"] != 0, "an invisible data dir must stay fatal on containerd: " + no_data["output"])
            log("✓ an invisible data dir is still refused rather than producing empty volumes")

            # --- 6. no socket bind: self-inspection cannot run, and says why ---
            #
            # This is the degraded path, and it must NOT claim to have translated
            # anything: the identity mapper is what silently hands containerd the
            # server's own spelling.
            no_sock = preflight("sipf-nosock", bind_socket = False)
            assert_contains(no_sock["output"], "containerd", "without the socket the server must report the real cause, naming containerd")
            assert_true("translating its paths for the runtime" not in no_sock["output"], "with no socket to ask, the server must not claim to be translating")
            log("✓ without the socket bind the server reports why it cannot self-inspect instead of guessing")
