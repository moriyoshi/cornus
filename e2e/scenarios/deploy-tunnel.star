# Public tunnels through the native `cornus tunnel` CLI. The server hosts an
# ngrok tunnel in-process and bridges it to a deployment's container port, so an
# unpublished :80 becomes reachable from the public internet. Proves the full
# CLI -> server -> ngrok relay -> server -> backend -> container path end to end.
#
# OPT-IN: hosting a real tunnel needs a real ngrok account + external egress, so
# this scenario is skipped unless NGROK_AUTHTOKEN is set in the harness env
# (mirroring the opt-in Go test TestNgrokLive). The local target has no runtime
# backend and is skipped too.

token = getenv(name = "NGROK_AUTHTOKEN")

if TARGET == "local":
    log("deploy-tunnel: skipped (needs a real backend)")
elif token == "":
    log("deploy-tunnel: skipped (set NGROK_AUTHTOKEN to run the real tunnel path)")
else:
    serve()

    # nginx serving on :80 with NO published ports: unreachable from anywhere
    # until tunneled. Its default command already serves (backend-agnostic).
    deploy(name = "tun", image = "nginx:alpine")
    wait(name = "tun", running = 1, timeout = "240s")
    log("✓ workload Running with an UNPUBLISHED :80")

    # Host a public tunnel to the container's :80 through the server, then fetch
    # the public URL from the harness host — the bytes ride the ngrok relay in
    # and back out to the container.
    url = tunnel(name = "tun", port = 80)
    assert_true(url.startswith("https://"), "expected an https tunnel URL, got %r" % url)

    r = http_get(url = url + "/", retry = "60s")
    assert_eq(r["status"], 200, "tunnel GET status (got %r)" % r["status"])
    assert_contains(r["body"], "nginx", "tunnel did not reach the container's :80, got %r" % r["body"])
    log("✓ `cornus tunnel` reached an unpublished container port over the public relay")

    remove(name = "tun")
