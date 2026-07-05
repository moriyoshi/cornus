# Local-AI-credential PROXY delivery, hermetic end-to-end against a MOCK upstream.
#
# The auth-injecting proxy providers (anthropic-proxy / openai-proxy) normally
# forward to the real vendor API, so they were unit-tested rather than run in a
# cluster. This scenario closes that gap using the delivery's `upstream` override
# (an Anthropic-compatible-gateway knob that doubles as a test seam): a stub nginx
# upstream stands in for api.anthropic.com and echoes back the request headers it
# received, so we can prove the sidecar proxy injected the developer's local
# credential — WITHOUT the app ever holding the secret and without touching the
# real API.
#
# Path exercised: client `static` source (an OAuth-shaped Claude token) -> held
# session -> server relay -> caretaker credential role (anthropic-proxy endpoint,
# upstream pinned at the mock) -> app `wget $ANTHROPIC_BASE_URL` -> mock upstream.
# The mock's echoed body must show `Authorization: Bearer <token>` and the required
# `anthropic-beta: oauth-2025-04-20`, injected by the proxy.
#
# Kube-only: the delivery uses the kubernetes credential attach + 9P mount sidecars.
#
# Source of truth: pkg/creddelivery/anthropicproxy + internal/authproxy (inject +
# upstream override), pkg/caretaker/credential.go (endpoint role), pkg/credential
# (client source). Unit E2E of the same path: pkg/server TestCredentialProxyRelayToMockUpstream.

# nginx that reflects the auth headers the proxy forwarded. `$http_*` exposes the
# request headers (lowercased, '-' -> '_'); a single-line body avoids embedding
# literal newlines from Starlark.
NGINX_CONF = """server {
    listen 80 default_server;
    location / {
        default_type text/plain;
        return 200 "authorization=[$http_authorization] x_api_key=[$http_x_api_key] anthropic_beta=[$http_anthropic_beta]";
    }
}
"""

TOKEN = "sk-ant-oat-e2e-proxytok"

# cornus deploys kube workloads into the target's namespace (KubeTarget sets it to
# "cornus-e2e"), which is NOT the kubeconfig's default namespace. A bare `kubectl
# get pod` therefore queries "default" and finds nothing even though `wait(running=
# 1)` (server-side) succeeds — so every kubectl below must pass `-n NS`, the same
# way the harness's own pod_exec builtin does.
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
        # {.items[*].status.podIP} tolerates an empty list: before a pod is
        # Running the field-selector returns zero items, and the wildcard then
        # yields "" (rather than a negative index like {.items[-1:]}, which
        # errors "array index out of bounds" on an empty list and would make
        # kubectl exit non-zero, aborting the scenario instead of retrying). The
        # wildcard also yields every running pod's IP; we take the last (newest).
        ips = kubectl("-n", NS, "get", "pod", "-l", "cornus.app=" + app,
                      "--field-selector=status.phase=Running",
                      "-o", "jsonpath={.items[*].status.podIP}").split()
        if ips:
            return ips[-1]
        sleep(duration = "2s")
    fail(msg = "mock upstream %s never got a pod IP" % app)

if TARGET != "kube":
    log("credentials-ai-proxy: skipped (kube-only; credential attach + 9P sidecars)")
else:
    serve()

    # 1. Mock upstream: nginx echoing request headers, config streamed in over 9P.
    conf_dir = temp_dir()
    write_file(path = conf_dir + "/default.conf", content = NGINX_CONF)
    deploy_attach(
        name = "ai-upstream",
        image = "nginx:1.27-alpine",
        local_mount = [conf_dir + ":/etc/nginx/conf.d:ro"],
        timeout = "240s",
    )
    wait(name = "ai-upstream", running = 1, timeout = "240s")
    ip = upstream_ip("ai-upstream")
    upstream = "http://%s:80" % ip
    log("✓ mock upstream is up at %s" % upstream)

    # 2. App: an OAuth-shaped Claude token sourced on the client and delivered via
    #    the anthropic-proxy sidecar, whose upstream is pinned at the mock. The app
    #    holds NO key — only ANTHROPIC_BASE_URL pointing at the loopback sidecar.
    creds = '[{"name":"claude","backend":"static",' + \
            '"config":{"oauth_token":"' + TOKEN + '"},' + \
            '"deliver":[{"kind":"endpoint","provider":"anthropic-proxy",' + \
            '"upstream":"' + upstream + '"}]}]'
    deploy_attach(
        name = "ai-app",
        image = "busybox:1.36",
        command = ["sleep", "3600"],
        credentials_json = creds,
        timeout = "240s",
    )
    wait(name = "ai-app", running = 1, timeout = "240s")
    log("✓ app is up with the anthropic-proxy sidecar (holds no key)")

    # 3. Call the vendor API base URL from inside the app. The proxy injects the
    #    relayed credential and forwards to the mock, which echoes what it saw.
    body = read_until("ai-app", 'wget -q -O - "$ANTHROPIC_BASE_URL/v1/messages"',
                      "authorization=[Bearer")
    log("mock upstream saw: %s" % body.strip())
    assert_contains(body, "authorization=[Bearer " + TOKEN + "]",
                    "proxy must inject the developer's OAuth token as Bearer")
    assert_contains(body, "anthropic_beta=[oauth-2025-04-20]",
                    "proxy must add the required anthropic-beta header for OAuth")

    attach_stop(name = "ai-app")
    attach_stop(name = "ai-upstream")
    log("✓ disconnect tore down the app, the sidecar proxy, and the mock upstream")
