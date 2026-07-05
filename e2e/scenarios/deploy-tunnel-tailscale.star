# Public tunnels through the native `cornus tunnel` CLI on the TAILSCALE backend.
# The server is booted with CORNUS_TUNNEL_BACKEND=tailscale and shells out to the
# `tailscale funnel` CLI, which proxies https://<node>.ts.net/ to a loopback shim
# the server bridges to the deployment's container port. Proves the full
# CLI -> server -> tailscale funnel -> server -> backend -> container path end to
# end, on a completely different backend from deploy-tunnel.star (ngrok).
#
# OPT-IN: hosting a Funnel needs a node already joined to a tailnet
# (`tailscale up`) with the Funnel node-attribute granted in the tailnet ACL
# policy, plus external egress — none of which is available in CI. The scenario is
# skipped unless CORNUS_TUNNEL_TAILSCALE_E2E is set in the harness env. Funnel is
# anonymous from cornus's side (CredentialOptional), so no authtoken is injected.
# The local target has no runtime backend and is skipped too.
#
# Run it live (on a joined + Funnel-enabled node) with:
#   CORNUS_TUNNEL_TAILSCALE_E2E=1 make e2e-docker

gate = getenv(name = "CORNUS_TUNNEL_TAILSCALE_E2E")

if TARGET == "local":
    log("deploy-tunnel-tailscale: skipped (needs a real backend)")
elif gate == "":
    log("deploy-tunnel-tailscale: skipped (set CORNUS_TUNNEL_TAILSCALE_E2E on a Funnel-enabled tailnet node to run it)")
else:
    # Select the tailscale funnel backend on the server. The node join and Funnel
    # ACL are out-of-band, so no credential rides through cornus.
    serve(env = {"CORNUS_TUNNEL_BACKEND": "tailscale"})

    # nginx serving on :80 with NO published ports: unreachable from anywhere
    # until tunneled. Its default command already serves (backend-agnostic).
    deploy(name = "tun", image = "nginx:alpine")
    wait(name = "tun", running = 1, timeout = "240s")
    log("✓ workload Running with an UNPUBLISHED :80")

    # Host a public Funnel to the container's :80 through the server, then fetch
    # the public URL from the harness host — the bytes ride the tailscale relay in
    # and back out to the container.
    url = tunnel(name = "tun", port = 80)
    assert_true(url.startswith("https://"), "expected an https tunnel URL, got %r" % url)
    assert_contains(url, ".ts.net", "expected a *.ts.net Funnel URL, got %r" % url)

    r = http_get(url = url + "/", retry = "60s")
    assert_eq(r["status"], 200, "tunnel GET status (got %r)" % r["status"])
    assert_contains(r["body"], "nginx", "tunnel did not reach the container's :80, got %r" % r["body"])
    log("✓ `cornus tunnel` reached an unpublished container port over the tailscale Funnel relay")

    remove(name = "tun")
