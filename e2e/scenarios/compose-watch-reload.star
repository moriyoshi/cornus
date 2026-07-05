# `compose up -d --watch`: the background agent watches the loaded compose file
# and, on a plain file save, re-execs the CLI to RELOAD the configuration and
# RE-RECONCILE the project — bringing up a service that was added to the file with
# no second manual `up`. This is the end-to-end proof of the auto-reload feature
# (agent-side file watch -> reload -> reconcile).
#
# The fixture is written into a temp dir (not the repo tree) so the scenario can
# edit it mid-run. Public images so it runs on both the Docker host and kind
# targets without a build step.

serve()

work = temp_dir()
cf = work + "/compose.yaml"

# Start with a single fire-and-forget service. --watch still arms the agent's
# watcher even though no session is handed off (the agent holds the watch).
one = """name: e2ewatch
services:
  web:
    image: nginx:1.27-alpine
"""
write_file(path = cf, content = one)

compose_up(file = cf, detach = True, watch = True)
st = wait(name = "e2ewatch-web", running = 1, timeout = "180s")
assert_eq(st["running"], 1, "web should be up after the initial watched up")
log("✓ initial watched up: web running; agent is watching %s" % cf)

# Edit the compose file to ADD a service. Saving the file must make the agent
# detect the change, reload the whole configuration, and reconcile the new service
# into existence — without any second `compose up`.
two = one + """  cache:
    image: redis:7-alpine
"""
write_file(path = cf, content = two)

st = wait(name = "e2ewatch-cache", running = 1, timeout = "180s")
assert_eq(st["running"], 1, "editing the compose file (add cache) must auto-reload and bring cache up")
log("✓ auto-reload: added service 'cache' was reconciled into the running project")

# web must still be running (the reload reconciled to the new desired set, it did
# not restart the unchanged service).
assert_eq(status(name = "e2ewatch-web")["running"], 1, "unchanged service web must stay up across a reload")
log("✓ reload left the unchanged service 'web' running")

# A full down stops the project AND its watcher, and removes both workloads.
compose_down(file = cf)

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "expected %s removed after compose down" % name)

wait_gone("e2ewatch-web")
wait_gone("e2ewatch-cache")
log("✓ down: project and its watcher stopped, workloads removed")
