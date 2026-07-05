# `cornus web --publish-in-conduit` JOINS the SOCKS5 conduit the background agent
# is already running, instead of starting a second one from its own settings.
#
# Conduits have no name and no join API: two frontends share one only when their
# whole resolved configuration hashes to the same clientconduit.Identity. A
# `cornus web` resolves that configuration for itself, and its config path has
# never filled in ingress the way `compose up -d` does — so the everyday case was
# a SECOND proxy that then collided with the first on one bind address. The fix is
# agent-side (clientagent/web.go, resolveWebConduitLocked): a publish that pinned
# nothing adopts whichever shared socks5 conduit the connection already has.
#
# The load-bearing assertion is ONE ADDRESS, not "the UI is reachable". A scenario
# that only fetched the UI would pass just as happily against two proxies — which
# is precisely the bug. So both the workload and the UI are fetched through the
# SAME proxy port, the one `compose up -d` chose, and `daemon status` is asked to
# account for exactly one.
#
# Docker-only: it drives `compose up -d` on the background agent, like agent.star
# and compose-conduit-mismatch.star.

compose_file = "e2e/scenarios/compose-app.yaml"
project = "webjoin"

if TARGET != "docker":
    log("web-conduit-join: skipped (docker-only)")
else:
    serve()

    # The compose session picks the address. `.shared` is the spelling that means
    # "the shared proxy, on this port" — the UI must find its way here on its own.
    p = free_port()
    proxy = "127.0.0.1:" + p

    compose_up(file = compose_file, project = project, detach = True,
               conduit = "socks5://.shared:" + p)
    wait(name = project + "-web", running = 1, timeout = "180s")
    r = http_get(url = "http://web:80/", socks5 = proxy, retry = "30s")
    assert_eq(r["status"], 200, "the compose service should answer through the shared proxy")
    log("✓ compose stack up on the shared conduit (%s)" % proxy)

    # NO conduit= here: this is the whole point. The profile default would resolve
    # to some other address entirely, and before the fix that started a second
    # proxy (or failed to bind).
    name = web(publish = True, compose_file = compose_file, project = project)
    log("✓ published as %s" % name)

    # The UI answers on the address COMPOSE chose. Through a second proxy it could
    # not: nothing else is listening there.
    cfg = http_get(url = "http://" + name + "/.cornus/web/config", socks5 = proxy, retry = "30s")
    assert_eq(cfg["status"], 200, "the published UI must answer through the conduit the compose session already runs")
    assert_contains(cfg["body"], "endpoint")

    # ...and the workload still does, through that same one. Both halves matter:
    # the UI alone would be satisfied by a conduit that had displaced the compose
    # session's, which is a different bug with the same symptom.
    r = http_get(url = "http://web:80/", socks5 = proxy, retry = "15s")
    assert_eq(r["status"], 200, "joining must not disturb the workload names already registered in the conduit")
    log("✓ one browser proxy setting (%s) reaches both the workload and the UI" % proxy)

    # The premise, stated directly rather than inferred from reachability: the
    # agent runs ONE proxy. A second one on another port would serve every
    # assertion above just as well if the UI had brought its own.
    inv = cornus("daemon", "status")
    log(inv)
    assert_eq(inv.count("SOCKS5 proxy listening on"), 1,
              "the agent should run exactly ONE SOCKS5 proxy, got:\n%s" % inv)
    assert_contains(inv, proxy, "the one proxy should be the address the compose session chose")

    web_stop(handle = name)
    compose_down(file = compose_file, project = project)
