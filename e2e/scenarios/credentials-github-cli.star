# The REAL `github-cli` credential source, end to end, against a stub `gh`.
#
# credentials-github-proxy.star covers the DELIVERY half with a `static` source.
# This covers the SOURCE half: `backend: github-cli` actually shelling out on the
# client, its token crossing the relay, and — the part no unit test can reach —
# the caretaker RE-RUNNING it when the TTL lapses, so a rotated token propagates
# into a long-lived workload.
#
# The runner needs no GitHub login: `CORNUS_GH_BIN` points the source at a stub,
# passed through `deploy_attach(client_env=...)` because a credential source runs
# in the CLIENT (`cornus deploy`) process, not the server or the pod.
#
# The stub is what makes the rotation assertion trustworthy. It bumps a counter
# file and prints `gho_e2e_stub_<n>`, so two fetches are DISTINGUISHABLE — with a
# fixed token, a re-run and a cache hit look identical and the test would pass
# whether or not refresh worked at all. It also asserts its own argv is exactly
# `auth token`, so a change to the backend's command line fails here rather than
# silently exercising something else.
#
# Kube-only: credential delivery is realized on the kubernetes backend.
#
# Source of truth: pkg/credential/githubcli (source + CORNUS_GH_BIN precedence),
# pkg/caretaker/credential.go (credFetcher TTL cache + re-fetch),
# pkg/creddelivery/githubproxy (injection).

NGINX_CONF = """server {
    listen 80 default_server;
    location / {
        default_type text/plain;
        return 200 "authorization=[$http_authorization]";
    }
}
"""

NS = "cornus-e2e"

# Short enough that the refresh in step 5 is observable in scenario time; the
# caretaker's default is 5 minutes. Named once so the spec and the failure
# diagnostic cannot drift apart — proving that matters, the neutralization run
# (ttl raised to 1h, refresh correctly not observed) still reported "3s".
TTL = "3s"

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
    log("credentials-github-cli: skipped (kube-only; credential attach + 9P sidecars)")
else:
    serve()

    # 1. A stub `gh` on the CLIENT. Each run bumps a counter and prints a distinct
    #    token, which is what lets step 5 tell a re-mint from a cache hit. It fails
    #    loudly on unexpected argv so a backend command-line change surfaces here.
    work = temp_dir()
    stub = work + "/gh"
    write_file(path = stub, content = """#!/bin/sh
[ "$1" = auth ] && [ "$2" = token ] || { echo "unexpected gh args: $*" >&2; exit 2; }
n=0
[ -f "$0.count" ] && n=$(cat "$0.count")
n=$((n + 1))
echo "$n" > "$0.count"
printf 'gho_e2e_stub_%s\\n' "$n"
""")
    sh(cmd = "chmod +x '%s'" % stub)
    log("✓ stub gh at %s (prints gho_e2e_stub_<n>, n increments per run)" % stub)

    # 2. Mock upstream: nginx echoing the Authorization the proxy forwarded.
    conf_dir = temp_dir()
    write_file(path = conf_dir + "/default.conf", content = NGINX_CONF)
    deploy_attach(
        name = "ghcli-upstream",
        image = "nginx:1.27-alpine",
        local_mount = [conf_dir + ":/etc/nginx/conf.d:ro"],
        timeout = "240s",
    )
    wait(name = "ghcli-upstream", running = 1, timeout = "240s")
    upstream = "http://%s:80" % upstream_ip("ghcli-upstream")
    log("✓ mock upstream is up at %s" % upstream)

    # 3. App: source is `github-cli` for real, with the short TTL above.
    creds = '[{"name":"gh","backend":"github-cli","ttl":"' + TTL + '",' + \
            '"deliveries":[' + \
            '{"kind":"endpoint","provider":"github-proxy","upstream":"' + upstream + '"},' + \
            '{"kind":"file","path":"/creds/gh-token","format":"raw"}' + \
            ']}]'
    deploy_attach(
        name = "ghcli-app",
        image = "busybox:1.36",
        command = ["sleep", "3600"],
        credentials_json = creds,
        client_env = {"CORNUS_GH_BIN": stub},
        timeout = "240s",
    )
    wait(name = "ghcli-app", running = 1, timeout = "240s")
    log("✓ app is up; its credential is minted by the stub gh on the client")

    # 4. The token the stub produced reached the upstream through the proxy — so
    #    the source ran on the client, crossed the relay, and was injected.
    body = read_until("ghcli-app", 'wget -q -O - "$GITHUB_API_URL/user"',
                      "authorization=[Bearer gho_e2e_stub_")
    log("mock upstream saw: %s" % body.strip())
    assert_contains(body, "authorization=[Bearer gho_e2e_stub_",
                    "github-cli's token must reach the upstream via the proxy")

    # The file delivery renders the same credential as a bare token, which is what
    # pins that the source's default value key ("token") interoperates with
    # format:raw — the two defaults are set in different packages.
    filed = read_until("ghcli-app", "cat /creds/gh-token", "gho_e2e_stub_")
    assert_contains(filed, "gho_e2e_stub_",
                    "format:raw must render github-cli's token, not a JSON object")
    log("✓ file delivery rendered the raw token")

    # 5. Refresh: past the TTL the caretaker re-runs the source, so the NEXT
    #    call carries a DIFFERENT token. This is the claim the docs make about
    #    long-running sessions ("gh refreshes in place; re-running keeps it
    #    alive") and the reason the stub's output varies per run.
    first = body.split("authorization=[Bearer ")[1].split("]")[0]
    sleep(duration = "5s")
    second_body = ""
    for _ in range(30):
        out = pod_exec(app = "ghcli-app", cmd = 'wget -q -O - "$GITHUB_API_URL/user"')
        tok = out.split("authorization=[Bearer ")[1].split("]")[0] if "authorization=[Bearer " in out else ""
        if tok and tok != first:
            second_body = tok
            break
        sleep(duration = "2s")
    if second_body == "":
        fail(msg = "token never changed after the %s TTL lapsed (still %r) — the caretaker is not re-running the source" % (TTL, first))
    log("✓ TTL lapse re-ran the stub: %s -> %s" % (first, second_body))

    # 6. The container holds no token of its own.
    env = pod_exec(app = "ghcli-app", cmd = "printenv")
    assert_contains(env, "GITHUB_API_URL=", "the app must be pointed at the proxy")
    if "GITHUB_TOKEN=" in env:
        fail(msg = "github-proxy must not set GITHUB_TOKEN in the app: %r" % env)
    if "gho_e2e_stub_" in env:
        fail(msg = "the token leaked into the app environment: %r" % env)

    attach_stop(name = "ghcli-app")
    attach_stop(name = "ghcli-upstream")
    log("✓ disconnect tore down the app, the sidecar proxy, and the mock upstream")
