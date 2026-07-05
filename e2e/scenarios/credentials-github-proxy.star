# GitHub credential PROXY delivery, hermetic end-to-end against a MOCK upstream.
#
# The github-proxy provider normally forwards to https://api.github.com, so the
# cluster path was unit-tested rather than run. This scenario closes that gap the
# same way credentials-ai-proxy.star does, using the delivery's `upstream`
# override (a GitHub-Enterprise knob that doubles as a test seam): a stub nginx
# stands in for api.github.com and echoes back the request headers it received,
# so we can prove the sidecar proxy injected the credential — WITHOUT the app
# ever holding the token and without touching the real API.
#
# The SOURCE is `static`, not `github-cli`: the runner must not need a logged-in
# `gh`. github-cli's own coverage is its unit test's shell stub, the same
# trade-off the `anthropic` backend makes. What is under test here is the
# delivery path — relay, caretaker role, header injection.
#
# Path exercised: client `static` source -> held session -> server relay ->
# caretaker credential role (github-proxy endpoint, upstream pinned at the mock)
# -> app `wget $GITHUB_API_URL/user` -> mock upstream. The echoed body must show
# `Authorization: Bearer <token>` and a non-empty User-Agent (httputil blanks a
# missing one and GitHub 403s those).
#
# Kube-only: the delivery uses the kubernetes credential attach + 9P mount sidecars.
#
# Source of truth: pkg/creddelivery/githubproxy + internal/authproxy (inject +
# upstream override + Link/Location rewriting), pkg/caretaker/credential.go
# (endpoint role), pkg/credential (client source).

# nginx that reflects what the proxy forwarded. `$http_*` exposes the request
# headers (lowercased, '-' -> '_'); a single-line body avoids embedding literal
# newlines from Starlark.
NGINX_CONF = """server {
    listen 80 default_server;
    location / {
        default_type text/plain;
        return 200 "authorization=[$http_authorization] user_agent=[$http_user_agent]";
    }
}
"""

TOKEN = "gho_e2e_githubproxytok"

# cornus deploys kube workloads into the target's namespace (KubeTarget sets it to
# "cornus-e2e"), which is NOT the kubeconfig's default namespace, so every kubectl
# below must pass -n NS the way the harness's own pod_exec builtin does.
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
        # Running the field-selector returns zero items and the wildcard yields
        # "" (rather than a negative index like {.items[-1:]}, which errors on an
        # empty list and would abort the scenario instead of retrying).
        ips = kubectl("-n", NS, "get", "pod", "-l", "cornus.app=" + app,
                      "--field-selector=status.phase=Running",
                      "-o", "jsonpath={.items[*].status.podIP}").split()
        if ips:
            return ips[-1]
        sleep(duration = "2s")
    fail(msg = "mock upstream %s never got a pod IP" % app)

if TARGET != "kube":
    log("credentials-github-proxy: skipped (kube-only; credential attach + 9P sidecars)")
else:
    serve()

    # 1. Mock upstream: nginx echoing request headers, config streamed in over 9P.
    conf_dir = temp_dir()
    write_file(path = conf_dir + "/default.conf", content = NGINX_CONF)
    deploy_attach(
        name = "gh-upstream",
        image = "nginx:1.27-alpine",
        local_mount = [conf_dir + ":/etc/nginx/conf.d:ro"],
        timeout = "240s",
    )
    wait(name = "gh-upstream", running = 1, timeout = "240s")
    ip = upstream_ip("gh-upstream")
    upstream = "http://%s:80" % ip
    log("✓ mock upstream is up at %s" % upstream)

    # 2. App: a GitHub token sourced on the client and delivered via the
    #    github-proxy sidecar, whose upstream is pinned at the mock. The app holds
    #    NO token — only GITHUB_API_URL pointing at the loopback sidecar.
    creds = '[{"name":"gh","backend":"static",' + \
            '"config":{"token":"' + TOKEN + '"},' + \
            '"deliveries":[{"kind":"endpoint","provider":"github-proxy",' + \
            '"upstream":"' + upstream + '"}]}]'
    deploy_attach(
        name = "gh-app",
        image = "busybox:1.36",
        command = ["sleep", "3600"],
        credentials_json = creds,
        timeout = "240s",
    )
    wait(name = "gh-app", running = 1, timeout = "240s")
    log("✓ app is up with the github-proxy sidecar (holds no token)")

    # 3. The proxy must advertise itself and NOT hand the app a token: a
    #    placeholder GITHUB_TOKEN would be picked up by gh / git credential
    #    helpers / direct api.github.com calls, so its absence is deliberate.
    env = pod_exec(app = "gh-app", cmd = "printenv")
    assert_contains(env, "GITHUB_API_URL=", "the app must be pointed at the proxy")
    if "GITHUB_TOKEN=" in env:
        fail(msg = "github-proxy must not set GITHUB_TOKEN in the app: %r" % env)

    # 4. Call the API base URL from inside the app. The proxy injects the relayed
    #    credential and forwards to the mock, which echoes what it saw.
    body = read_until("gh-app", 'wget -q -O - "$GITHUB_API_URL/user"',
                      "authorization=[Bearer")
    log("mock upstream saw: %s" % body.strip())
    assert_contains(body, "authorization=[Bearer " + TOKEN + "]",
                    "proxy must inject the client-sourced token as Bearer")
    if "user_agent=[]" in body or "user_agent=[-]" in body:
        fail(msg = "proxy forwarded an empty User-Agent; GitHub 403s those: %r" % body)

    attach_stop(name = "gh-app")
    attach_stop(name = "gh-upstream")
    log("✓ disconnect tore down the app, the sidecar proxy, and the mock upstream")
