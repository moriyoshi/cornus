# Client-side egress on the bare backend via the EgressBackend companion
# (ApplyWithEgress): a caretaker container joins the app instance's pinned netns
# and runs the forward proxy the app is pointed at (HTTP_PROXY). This proves the
# bare backend's egress-companion wiring end to end — crucially, the app only
# becomes Ready once the caretaker's proxy binds, and the proxy binds only after
# its relay session to the cornus server comes up, so a passing deploy is itself
# evidence the companion connected back through the (routable) advertised URL.
#
# bare-only, and needs a cornus agent image in a registry the bare backend can
# pull from (setup_bare's prepare_bare_agent_image sets CORNUS_AGENT_IMAGE);
# self-skips otherwise, the same idiom deploy-mounts-sidecar-docker.star uses.

agent_image = getenv("CORNUS_AGENT_IMAGE", "")
RUNC = "runc --root /run/cornus/bare-runc"

if TARGET != "bare":
    log("deploy-egress-bare: skipped (bare-only; exercises the bare EgressBackend companion)")
elif agent_image == "":
    log("deploy-egress-bare: skipped (no CORNUS_AGENT_IMAGE; prepare_bare_agent_image did not run)")
else:
    addr = serve(env = {"CORNUS_AGENT_IMAGE": agent_image})

    # Proxy-mode egress: the caretaker companion runs the forward proxy on loopback
    # and the app is pointed at it via HTTP_PROXY. deploy_attach blocks until the
    # app is Running — which, per the caretaker's readiness gate, means the proxy
    # bound and thus the companion's relay to the server came up.
    deploy_attach(
        name = "eg",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        egress = "proxy",
        timeout = "240s",
    )
    log("✓ egress deploy Ready (the caretaker proxy bound => its relay connected)")

    # 1) The forward-proxy env is injected into the app container.
    envout = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "eg", "printenv", "HTTP_PROXY"])
    assert_contains(envout["output"], "127.0.0.1:15002", "app HTTP_PROXY should point at the caretaker proxy, got %r" % envout["output"])
    log("✓ HTTP_PROXY points the app at the caretaker forward proxy")

    # 2) The egress companion is a real runc container beside the app instance.
    lst = sh(cmd = RUNC + " list -q 2>/dev/null")
    assert_contains(lst["output"], "cornus-eg-egress-0", "expected an egress-caretaker companion container, got %r" % lst["output"])
    assert_contains(lst["output"], "cornus-eg-0", "expected the app instance container")
    log("✓ egress-caretaker companion present as a runc container")

    # 3) The companion shares the app instance's network namespace (it joins it so
    # the loopback proxy is reachable by the app).
    ns = sh(cmd = """
RUNC="%s"
apppid=$($RUNC state cornus-eg-0 2>/dev/null | grep -o '"pid": [0-9]*' | grep -o '[0-9]*')
comppid=$($RUNC state cornus-eg-egress-0 2>/dev/null | grep -o '"pid": [0-9]*' | grep -o '[0-9]*')
a=$(readlink /proc/$apppid/ns/net 2>/dev/null); c=$(readlink /proc/$comppid/ns/net 2>/dev/null)
[ -n "$a" ] && [ "$a" = "$c" ] && echo "SAME_NETNS $a" || echo "DIFFERENT app=$a comp=$c"
""" % RUNC)
    assert_contains(ns["output"], "SAME_NETNS", "egress companion must share the app instance netns, got %r" % ns["output"])
    log("✓ egress companion shares the app instance's netns")

    # 4) Status reports only the app instance, never the companion.
    st = status(name = "eg")
    assert_eq(st["running"], 1, "Status must report exactly the app instance, not the egress companion")
    log("✓ egress companion filtered out of Status")

    # 5) Graceful disconnect tears down the app AND its companion.
    attach_stop(name = "eg")
    for _ in range(20):
        left = sh(cmd = RUNC + " list -q 2>/dev/null | grep cornus-eg || true")
        if left["output"] == "":
            break
        sleep(duration = "1s")
    left = sh(cmd = RUNC + " list -q 2>/dev/null | grep cornus-eg || true")
    assert_eq(left["output"], "", "app + egress companion must both be gone after attach_stop, still present: %r" % left["output"])
    log("✓ torn down: app and egress companion both reaped")
