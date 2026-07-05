# A SHARED proxy and a SESSION-LOCAL proxy coexisting in ONE background agent.
#
# `up -d` runs inside the client agent, which hosts one proxy per tunnel config. A
# session can instead ask for its OWN private proxy (its own listener + alias table)
# via a socks5://host:port conduit. This drives two projects through one agent:
#   - project A joins a SHARED proxy   (--conduit socks5://.shared:PORT1)
#   - project B gets a SESSION-LOCAL   (--conduit socks5://127.0.0.1:PORT2)
# and proves they coexist and that each proxy's bare-name aliases are private to it.
# Docker-only (drives `compose up -d` on the background agent, like agent.star).
#
# Source of truth: cmd/cornus/internal/clientagent (conduitKeyOf session isolation),
# pkg/socks5 (alias table), cmd/cornus/internal/composecli (up -d alias registration).

file_a = "e2e/scenarios/socks5-coexist-a.yaml"  # project coexa, service weba
file_b = "e2e/scenarios/socks5-coexist-b.yaml"  # project coexb, service webb

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed" % name)

if TARGET != "docker":
    log("socks5-coexist: skipped (docker-only; drives two `compose up -d` on one agent)")
else:
    serve()

    p1 = free_port()
    p2 = free_port()
    shared = "127.0.0.1:" + p1
    local = "127.0.0.1:" + p2

    # Project A -> the SHARED proxy on p1; project B -> its own SESSION-LOCAL proxy on
    # p2. Both `up -d` register with the same background agent, so it now hosts two
    # coexisting proxies.
    compose_up(file = file_a, detach = True, conduit = "socks5://.shared:" + p1)
    wait(name = "coexa-weba", running = 1, timeout = "180s")
    compose_up(file = file_b, detach = True, conduit = "socks5://" + local)
    wait(name = "coexb-webb", running = 1, timeout = "180s")
    log("✓ one agent hosts a shared proxy (%s) and a session-local proxy (%s)" % (shared, local))

    # Coexistence: each proxy reaches ITS OWN project's service by bare name, at the
    # same time. Two live proxies, two private alias tables.
    ra = http_get(url = "http://weba:80/", socks5 = shared, retry = "30s")
    assert_eq(ra["status"], 200, "project A's `weba` not reachable through the shared proxy")
    assert_contains(ra["body"], "nginx", "shared proxy did not reach the web service")
    rb = http_get(url = "http://webb:80/", socks5 = local, retry = "30s")
    assert_eq(rb["status"], 200, "project B's `webb` not reachable through the session-local proxy")
    assert_contains(rb["body"], "nginx", "session-local proxy did not reach the web service")
    log("✓ both proxies serve simultaneously (coexistence)")

    # Isolation: a bare alias lives only in its own proxy's table. Project B's `webb`
    # is unknown to A's proxy (and vice versa), so it egresses directly and fails.
    m1 = http_get(url = "http://webb:80/", socks5 = shared, retry = "3s", allow_error = True)
    assert_true(m1.get("error", "") != "", "`webb` must NOT resolve through project A's proxy (got %r)" % m1)
    m2 = http_get(url = "http://weba:80/", socks5 = local, retry = "3s", allow_error = True)
    assert_true(m2.get("error", "") != "", "`weba` must NOT resolve through project B's proxy (got %r)" % m2)
    log("✓ bare aliases are private per proxy (session isolation)")

    # But the fully-qualified deployment name crosses any proxy — the suffix rule
    # reaches every deployment by name, so only the bare alias is session-scoped.
    cross = http_get(url = "http://coexa-weba.cornus.internal:80/", socks5 = local, retry = "10s")
    assert_eq(cross["status"], 200, "the fully-qualified name should reach A's deployment even through B's proxy")
    log("✓ the fully-qualified name still crosses; only the bare alias is session-scoped")

    # Teardown: down both projects, then a single daemon stop tears the agent down.
    compose_down(file = file_a)
    compose_down(file = file_b)
    cornus("daemon", "stop")
    wait_gone("coexa-weba")
    wait_gone("coexb-webb")
    log("✓ down + daemon stop tore down both proxies with their sessions")
