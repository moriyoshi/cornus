# Brokered credentials declared in a COMPOSE file (`x-cornus-credentials:`),
# end-to-end against a mock upstream.
#
# credentials-ai-proxy.star proves the same delivery path from a deploy spec. What
# is unproven there, and is the whole point here, is that the COMPOSE client
# realizes it: a compose service declaring credentials must be classified as
# needing a HELD deploy-attach session, not deployed with a stateless POST. If it
# takes the stateless path the workload comes up green and its credential fetches
# have nobody to answer them — so this scenario fails by TIMING OUT on a body that
# never carries the token, which is exactly the regression to catch.
#
# Path exercised: compose file -> x-cornus-credentials -> api.DeploySpec.Credentials
# -> held foreground `compose up` -> server relay -> caretaker anthropic-proxy
# endpoint (upstream pinned at the mock) -> app `wget $ANTHROPIC_BASE_URL` -> mock.
#
# Kube-only: the delivery uses the kubernetes credential attach + 9P mount sidecars.
#
# Source of truth: pkg/compose (translateCredentials, x-cornus-credentials),
# cmd/cornus/internal/composecli needsHeldSession. Unit coverage:
# pkg/compose/credentials_test.go, composecli TestNeedsHeldSession.

NGINX_CONF = """server {
    listen 80 default_server;
    location / {
        default_type text/plain;
        return 200 "authorization=[$http_authorization] anthropic_beta=[$http_anthropic_beta]";
    }
}
"""

TOKEN = "sk-ant-oat-e2e-composetok"

# cornus deploys kube workloads into the target's namespace, not the kubeconfig
# default, so every kubectl below passes -n NS (see credentials-ai-proxy.star).
NS = "cornus-e2e"

def read_until(app, cmd, want, steps = 30):
    for _ in range(steps):
        out = pod_exec(app = app, cmd = cmd)
        if want in out:
            return out
        sleep(duration = "2s")
    fail(msg = "%r never contained %r (last: %r)" % (cmd, want, out))

def upstream_ip(app, steps = 30):
    for _ in range(steps):
        ips = kubectl("-n", NS, "get", "pod", "-l", "cornus.app=" + app,
                      "--field-selector=status.phase=Running",
                      "-o", "jsonpath={.items[*].status.podIP}").split()
        if ips:
            return ips[-1]
        sleep(duration = "2s")
    fail(msg = "mock upstream %s never got a pod IP" % app)

if TARGET != "kube":
    log("compose-credentials-proxy: skipped (kube-only; credential attach + 9P sidecars)")
else:
    serve()

    # 1. Mock upstream: nginx echoing the auth headers the proxy forwarded. Deployed
    #    with deploy_attach (not compose) so this scenario's compose file stays
    #    exactly the thing under test.
    conf_dir = temp_dir()
    write_file(path = conf_dir + "/default.conf", content = NGINX_CONF)
    deploy_attach(
        name = "ai-upstream-compose",
        image = "nginx:1.27-alpine",
        local_mount = [conf_dir + ":/etc/nginx/conf.d:ro"],
        timeout = "240s",
    )
    wait(name = "ai-upstream-compose", running = 1, timeout = "240s")
    upstream = "http://%s:80" % upstream_ip("ai-upstream-compose")
    log("✓ mock upstream is up at %s" % upstream)

    # 2. The app comes up from the compose file. compose_up_bg backgrounds a
    #    FOREGROUND `up`, which is what holds the session an interactive terminal
    #    would — the client that mints the token for the workload's lifetime.
    handle = compose_up_bg(
        file = "e2e/scenarios/compose-credentials-proxy.yaml",
        project = "ccp",
        env = {"AI_UPSTREAM": upstream, "AI_TOKEN": TOKEN},
    )
    wait(name = "ccp-app", running = 1, timeout = "240s")
    log("✓ compose service is up with the anthropic-proxy sidecar")

    # 3. The app holds no key: only ANTHROPIC_BASE_URL, pointing at the loopback
    #    sidecar. Assert that BEFORE the call, so a pass cannot come from a token
    #    that leaked into the container's environment instead of being injected.
    envout = pod_exec(app = "ccp-app", cmd = "printenv ANTHROPIC_BASE_URL || echo MISSING")
    assert_contains(envout, "http://127.0.0.1",
                    "compose service should get ANTHROPIC_BASE_URL pointed at the sidecar, got %r" % envout)
    leak = pod_exec(app = "ccp-app", cmd = "printenv | grep -c '%s' || true" % TOKEN)
    assert_contains(leak.strip(), "0", "the token must NOT be in the app's environment, got %r" % leak)

    # 4. Call the vendor base URL from inside the app; the sidecar injects the
    #    relayed credential and forwards to the mock, which echoes what it saw.
    body = read_until("ccp-app", 'wget -q -O - "$ANTHROPIC_BASE_URL/v1/messages"',
                      "authorization=[Bearer")
    log("mock upstream saw: %s" % body.strip())
    assert_contains(body, "authorization=[Bearer " + TOKEN + "]",
                    "the compose-declared credential must be injected as Bearer by the sidecar")
    assert_contains(body, "anthropic_beta=[oauth-2025-04-20]",
                    "proxy must add the required anthropic-beta header for OAuth")

    compose_up_stop(handle = handle)
    attach_stop(name = "ai-upstream-compose")
    log("✓ x-cornus-credentials brokered end to end through a held `compose up`")
