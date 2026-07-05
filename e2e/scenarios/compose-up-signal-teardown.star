# Terminating a foreground `cornus compose up` (Ctrl-C / SIGINT) must REMOVE the
# mount-free deployments it created — the way `docker compose up` stops its
# containers on exit. Before this fix a foreground up left its mount-free
# workloads running (only `down` removed them), so Ctrl-C never terminated them.
#
# The up is held on EVERY target via a session-local SOCKS5 conduit: a mount-free
# `up` on docker would otherwise return at once (the host publish makes the
# port-forward skip) and take the early non-blocking return that INTENTIONALLY
# leaves the deployments running for `down`. Only a genuine foreground exit
# removes them, which is exactly what a held up + SIGINT exercises.
#
# compose_up_stop sends SIGINT (the interactive Ctrl-C) and waits for the exit,
# unlike compose_up_wait which waits for a self-terminate after an external down.
#
# Source of truth: composecli.runForeground / removeDeployments and the
# `mountFree` slice in cmd/cornus/internal/composecli/commands.go.

compose_file = "e2e/scenarios/compose-app.yaml"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed after Ctrl-C of foreground `compose up`" % name)

serve()

port = free_port()
proxy = "127.0.0.1:" + port

# Foreground `compose up`, backgrounded, holding a session-local SOCKS5 proxy so it
# blocks on every target while its (mount-free) services run.
handle = compose_up_bg(file = compose_file, conduit = "socks5://" + proxy)
wait(name = "e2e-cache", running = 1, timeout = "180s")
wait(name = "e2e-web", running = 1, timeout = "180s")
log("✓ foreground `compose up` is holding its mount-free services")

# Ctrl-C the held up. It must exit cleanly and, on the way out, remove the
# deployments it created — no `down` involved.
res = compose_up_stop(handle = handle, timeout = "60s")
log(res["output"])
assert_eq(res["code"], 0, "foreground `up` should exit 0 on Ctrl-C")
assert_contains(res["output"], "stopping...", "Ctrl-C did not print the stopping banner")
assert_contains(res["output"], "web  removed", "Ctrl-C did not remove the web deployment")
assert_contains(res["output"], "cache  removed", "Ctrl-C did not remove the cache deployment")

# The regression this guards: the workloads are actually gone server-side, with no
# `down` ever issued.
wait_gone("e2e-web")
wait_gone("e2e-cache")
log("✓ terminating a foreground `compose up` removed its mount-free workloads")
