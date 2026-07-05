# Re-running a detached Compose project with CHANGED conduit settings reconciles
# the live project onto the new conduit — it neither refuses nor silently keeps
# the old one.
#
# This is the "configuration, not identity" half of the agent's project-reuse
# rule (cmd/cornus/internal/clientagent/agent.go, ensureProject): the SERVER
# CONNECTION is identity, so changing it is a conflict the CLI refuses by name;
# the whole conduit configuration is client-local (listeners, aliases, the
# ingress emulator), so changing it rebinds in place and the reconcile that
# follows moves the exposures across.
#
# Every assertion here is behavioural rather than textual, because the warning
# alone cannot distinguish "reconciled" from "said it reconciled". The proof is
# that the service answers through the NEW proxy and stops answering through the
# OLD one — which is exactly what the warning claims was withdrawn.
#
# Docker-only: it drives `compose up -d` on the background agent, like agent.star.

compose_file = "e2e/scenarios/compose-app.yaml"

def eventually_unreachable(addr, steps = 20):
    """Poll until `web` stops resolving through the proxy at addr.

    The old conduit's refcount is released AFTER the caller's reconcile, so the
    listener can outlive the `up` by a moment. Polling for the withdrawal keeps
    the assertion honest without racing it: a conduit that never goes away fails
    the scenario, one that goes away late still passes.
    """
    for _ in range(steps):
        r = http_get(url = "http://web:80/", socks5 = addr, retry = "1s", allow_error = True)
        if r.get("error", "") != "":
            return
        sleep(duration = "1s")
    fail(msg = "`web` still answers through the OLD conduit %s; the previous listener/aliases were not withdrawn" % addr)

if TARGET != "docker":
    log("compose-conduit-mismatch: skipped (docker-only)")
else:
    serve()
    p1 = free_port()
    p2 = free_port()
    old = "127.0.0.1:" + p1
    new = "127.0.0.1:" + p2

    compose_up(file = compose_file, project = "cmismatch", detach = True,
               conduit = "socks5://.shared:" + p1)
    wait(name = "cmismatch-web", running = 1, timeout = "180s")
    r = http_get(url = "http://web:80/", socks5 = old, retry = "30s")
    assert_eq(r["status"], 200, "`web` should be reachable through the first conduit")
    log("✓ project up on the first conduit (%s)" % old)

    # Same project, same server, but a different conduit endpoint. The live
    # resources must be retained and rebound, not refused and not left behind.
    warning = compose_up(file = compose_file, project = "cmismatch", detach = True,
                         conduit = "socks5://.shared:" + p2)
    assert_contains(warning, "conduit", "expected a conduit mismatch warning (got %r)" % warning)
    assert_contains(warning, "reconciled it onto the new conduit",
                    "the warning should say the project was reconciled, not that it was refused (got %r)" % warning)

    # The load-bearing assertion: the exposures actually moved. A warning that
    # says "reconciled" while the alias stayed on the old proxy would pass the
    # text assertions above and fail here.
    r = http_get(url = "http://web:80/", socks5 = new, retry = "60s")
    assert_eq(r["status"], 200, "`web` should be reachable through the NEW conduit after the rebind")
    assert_contains(r["body"], "nginx", "the new conduit did not reach the web service")
    log("✓ changed conduit settings reconciled the live project onto %s" % new)

    eventually_unreachable(old)
    log("✓ the previous listener and its aliases were withdrawn")

    compose_down(file = compose_file, project = "cmismatch")
