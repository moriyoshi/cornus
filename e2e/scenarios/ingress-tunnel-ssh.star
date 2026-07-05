# `cornus ingress-tunnel`: a public tunnel in front of the server's INGRESS rather
# than in front of one workload port, so every host and path a project declares is
# reachable through a single URL.
#
# HERMETIC. The tunnel rides the `ssh` backend against the harness's own sshd via
# SSH remote port forwarding, so the whole path — CLI -> server -> relay -> server
# -> ingress routing -> workload — is exercised with no ngrok account and no
# external egress. (The nearby deploy-tunnel.star needs a real NGROK_AUTHTOKEN and
# is opt-in; this one is not, which is the point.)
#
# CORNUS_TUNNEL_SSH_URL_TEMPLATE tells the server what public URL to report for the
# port the relay bound. Here the "relay" is loopback, so the template resolves to a
# local address the harness can fetch — the bytes still travel the real
# tcpip-forward path through sshd.
#
# Source of truth: pkg/server/ingress_tunnel.go (scope, front selection, host
# modes), pkg/tunnel/ssh, pkg/ingressmux, cmd/cornus/ingress_tunnel.go.

compose_file = "e2e/scenarios/ingress-front-door.yaml"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed" % name)

# Runs on docker AND kube. The kube run is not a duplicate: only there is the
# backend a deploy.IngressGateway, so only there does front selection actually
# PROBE for a cluster ingress controller and fall back to the server's own
# routing when none is discoverable — which is the case on the harness's kind
# cluster. On docker that branch is never even reached.
if TARGET != "docker" and TARGET != "kube":
    log("ingress-tunnel-ssh: skipped (needs the docker or kube target, plus a local sshd)")
elif TARGET == "kube" and getenv(name = "E2E_INGRESS_NGINX") == "1":
    # This scenario pins the MUX front: it fetches the tunnel URL with no Host
    # header and relies on the server having aliased the tunnel hostname onto the
    # ingress host. Install a real controller and the tunnel fronts that instead,
    # passing the request through untouched — the controller then sees a Host no
    # Ingress declares and answers 404, which is correct behaviour and simply a
    # different scenario. ingress-tunnel-controller.star covers that one.
    #
    # Only the kube target is affected: E2E_INGRESS_NGINX installs a controller
    # into the kind cluster, so on docker the mux front still applies and this
    # scenario stays valid.
    log("ingress-tunnel-ssh: skipped (kube + E2E_INGRESS_NGINX=1 selects the controller front; see ingress-tunnel-controller.star)")
else:
    ssh = sshd()
    if ssh == None:
        log("ingress-tunnel-ssh: skipped (no sshd binary)")
    else:
        # The relay binds this port on the sshd host (loopback here), and the
        # server reports it as the tunnel's public URL.
        relay_port = free_port()
        serve(env = {
            "CORNUS_TUNNEL_BACKEND": "ssh",
            "CORNUS_TUNNEL_SSH_ADDR": ssh["addr"],
            "CORNUS_TUNNEL_SSH_USER": ssh["user"],
            "CORNUS_TUNNEL_SSH_BIND": "127.0.0.1:" + relay_port,
            "CORNUS_TUNNEL_SSH_KNOWN_HOSTS": ssh["known_hosts"],
            "CORNUS_TUNNEL_SSH_URL_TEMPLATE": "http://127.0.0.1:{port}",
            # The ssh backend authenticates with this key, injected as the tunnel
            # credential by --authtoken-file below.
        })

        compose_up(file = compose_file, detach = True)
        wait(name = "e2e-ing-web", running = 1, timeout = "240s")
        wait(name = "e2e-ing-api", running = 1, timeout = "240s")
        log("✓ project up with two services behind one declared ingress host")

        # Host the ingress tunnel for the whole PROJECT. The private key is the
        # ssh backend's credential; --authtoken-file keeps it out of argv.
        url = ingress_tunnel(
            project = "e2e-ing",
            authtoken_file = ssh["identity"],
        )
        assert_true(url.startswith("http"), "expected a tunnel URL, got %r" % url)
        log("✓ ingress tunnel published at %s" % url)

        # Both services answer through the ONE tunnel URL, split by path — the whole
        # point of tunnelling the ingress rather than a port. No Host header is sent,
        # so the request arrives bearing the tunnel's own hostname; this works ONLY
        # because the server aliased that hostname onto the declared ingress host
        # (host mode "auto" -> "alias"), which is the mux front's behaviour.
        r = http_get(url = url + "/", retry = "90s", retry_5xx = True)
        assert_eq(r["status"], 200, "the root path should reach web through the tunnel")
        assert_contains(r["body"], "nginx", "the root path did not reach nginx")
        log("✓ / reached the web service through the tunnel")

        r = http_get(url = url + "/api", retry = "90s", retry_5xx = True)
        assert_eq(r["status"], 200, "/api should reach the api service through the tunnel")
        assert_contains(r["body"], "api-service", "/api did not reach the api service")
        log("✓ /api reached the api service through the SAME tunnel URL")

        # The tunnel process is held by the harness and torn down with it; removing
        # the project is what proves the routes go away.
        compose_down(file = compose_file)
        wait_gone("e2e-ing-web")
        wait_gone("e2e-ing-api")
        log("✓ tunnel torn down and project removed")
