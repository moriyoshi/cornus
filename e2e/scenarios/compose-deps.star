# `compose up SERVICE` brings up the named service's depends_on GRAPH.
#
# Until 2026-07-28 cornus deployed the named service ALONE, which is a silent
# divergence from `docker compose up web` and from what docs/cli/compose.md
# already claimed ("services are brought up in dependency order, honoring
# depends_on"): a project whose web needs db came up broken with no flag
# involved and no message. Expansion is now the default and announces itself;
# --no-deps opts out.
#
# The expansion itself (cmd/cornus/internal/composecli/flagcompat.go
# expandDependencies) is unit-tested, but it decides the SELECTION that crosses
# the deploy boundary — the set of workloads the server is asked to create. That
# is where a regression bites, and it is only observable by looking at what is
# actually deployed, which is what this scenario does: every assertion is a
# status() count of a real workload, never a line of CLI output. The announcement
# is asserted too, but only as a secondary check on a leg whose deploy result
# already agrees with it.
#
# Three legs, one per rule:
#   1. `up -d web`            -> web + api + cache + db (db is TRANSITIVE, via
#                                api), and `solo` untouched.
#   2. `up -d --no-deps web`  -> web ALONE.
#   3. `up -d solo`           -> a dependency-free service is unaffected: no
#                                expansion, no announcement, nothing else up.
#
# Backend-agnostic (public image, no build, no ports), so it runs anywhere a
# deploy backend exists; only the build-only `local` target skips.

compose_file = "e2e/scenarios/compose-deps.yaml"
PROJ = "e2edeps"
ALL = ["web", "api", "db", "cache", "solo"]
ANNOUNCE = "also starting dependencies"

def wl(svc):
    """The workload name compose derives for a service: <project>-<service>."""
    return PROJ + "-" + svc

def wait_gone(svc, steps = 90):
    for _ in range(steps):
        if status(name = wl(svc))["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s still present" % wl(svc))

def assert_up(svcs, why):
    for s in svcs:
        st = wait(name = wl(s), running = 1, timeout = "300s")
        assert_eq(st["running"], 1, "%s: %s should be running" % (why, s))

def assert_absent(svcs, why):
    for s in svcs:
        assert_eq(status(name = wl(s))["total"], 0,
                  "%s: %s must NOT have been deployed" % (why, s))

def down_all(host):
    cornus("compose", "-f", compose_file, "down", env = host)
    for s in ALL:
        wait_gone(s)

def run():
    addr = serve()
    host = {"CORNUS_HOST": "http://" + addr}

    # Up-front cleanup (Starlark has no defer, so a failed earlier run leaves its
    # workloads behind). Every assertion below is "this service is NOT deployed",
    # which a leftover would turn into a false FAILURE — or, worse, a leftover
    # `api` would make leg 1 pass without expansion having happened at all.
    down_all(host)

    # ---- 1. Naming one service deploys its TRANSITIVE depends_on set ---------
    # db is the load-bearing name here: web does not depend on it, api does. A
    # one-hop implementation would bring up api and cache and stop.
    out = cornus("compose", "-f", compose_file, "up", "-d", "web", env = host)
    log(out)
    assert_up(["web", "api", "cache", "db"], "up web")
    assert_absent(["solo"], "up web")
    # Only now that the deploy agrees, check that the growth was ANNOUNCED — a
    # selection that silently grew is the other half of the docker-parity bug.
    assert_contains(out, ANNOUNCE, "up SERVICE must announce the dependencies it added")
    assert_contains(out, "--no-deps", "the announcement must name the flag that opts out")
    log("✓ up web deployed the transitive depends_on set (api, cache, and db via api)")
    down_all(host)

    # ---- 2. --no-deps deploys ONLY the named service --------------------------
    out = cornus("compose", "-f", compose_file, "up", "-d", "--no-deps", "web", env = host)
    log(out)
    assert_up(["web"], "up --no-deps web")
    assert_absent(["api", "cache", "db", "solo"], "up --no-deps web")
    assert_true(ANNOUNCE not in out,
                "--no-deps must not announce an expansion it did not perform (got %r)" % out)
    log("✓ --no-deps suppressed the expansion: web deployed alone")
    down_all(host)

    # ---- 3. A dependency-free service is unaffected ---------------------------
    # The guard against an expansion that over-reaches (e.g. pulling in reverse
    # edges, or falling back to "the whole project" when a service has no
    # depends_on at all).
    out = cornus("compose", "-f", compose_file, "up", "-d", "solo", env = host)
    log(out)
    assert_up(["solo"], "up solo")
    assert_absent(["web", "api", "cache", "db"], "up solo")
    assert_true(ANNOUNCE not in out,
                "a service with no depends_on must not grow the selection (got %r)" % out)
    log("✓ a dependency-free service deploys alone, with no announcement")
    down_all(host)

if TARGET == "local":
    log("compose-deps: skipped (build-only target; needs a deploy backend)")
else:
    run()
