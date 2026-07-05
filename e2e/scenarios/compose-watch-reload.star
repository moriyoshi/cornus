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

NS = "cornus-e2e"  # KubeTarget's default namespace

# The pods behind the web deployment, by name. On the kube target this — not the
# status instance id — is the identity a recreate changes; see the note at the
# assertion below for why the id cannot answer the question there. A pod name
# carries its ReplicaSet's template hash and a random suffix, so a rolled
# template and a plain delete/recreate both mint a new one.
def web_pods():
    out = kubectl("-n", NS, "get", "pods", "-l", "cornus.app=e2ewatch-web",
                  "-o", "jsonpath={.items[*].metadata.name}", retry = "30s")
    return sorted([p for p in out.split(" ") if p != ""])

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
# The instance ID BEFORE the edit. This is what makes the "unchanged service was
# not restarted" claim testable: a restart necessarily mints a new container, so
# comparing identity answers the question directly.
web_id = st["instances"][0]["id"]
assert_true(web_id != "", "no instance id for web to compare across the reload")
# ... and the pods behind it, for the kube target, where the id names the
# Deployment rather than anything that a recreate replaces.
pods_before = []
if TARGET == "kube":
    pods_before = web_pods()
    assert_eq(len(pods_before), 1, "expected exactly one pod behind web before the edit")
log("✓ initial watched up: web running as %s; agent is watching %s" % (web_id[:12], cf))

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

# What happens to the UNCHANGED service across the reload, asserted by WORKLOAD
# IDENTITY — the only form of this claim that is stable whenever it is sampled.
#
# This assertion previously read `status(...)["running"] == 1`, sampled the
# instant after the reload reported the NEW service up. That was a coin flip: it
# happened to observe the replacement already running, and it failed on the CI
# docker leg (run 30359492738) when it landed inside the down-window instead.
#
# "Identity" is NOT the same observable on every backend, and reading it off
# `status()` uniformly is what broke the kube leg:
#
#   docker  the status instance id IS the container id, so a recreate necessarily
#           changes it and comparing ids answers the question directly.
#   kube    the status instance id is SYNTHESIZED. statusOf() in
#           pkg/deploy/kubernetes/kubernetes.go builds "<deployment>-<i>" out of
#           the Deployment's replica counters — it never reads a pod — so it is
#           the byte-identical string whether or not the pod was replaced, and an
#           `id != web_id` assertion there cannot pass however the backend
#           behaves. It never did: CI runs 30421398539, 30678513400, 30687211223
#           all failed on exactly that line. The kube leg reads the POD names out
#           of the cluster instead.
web_after = wait(name = "e2ewatch-web", running = 1, timeout = "60s")
if TARGET == "docker":
    # The dockerhost backend matches `docker compose up`: a container is recreated
    # only when its configuration or its image content changed (a spec+image-content
    # fingerprint stamped as the cornus.spec-hash label, pkg/deploy/dockerhost/reuse.go),
    # so editing ONE service no longer bounces every other service in the project.
    assert_eq(web_after["instances"][0]["id"], web_id,
              "web was replaced across the reload even though nothing about it changed — dockerhost's Apply must keep a container whose spec and image content are unchanged")
    log("✓ reload left the unchanged service 'web' alone (same instance %s)" % web_id[:12])
elif TARGET == "kube":
    # The kubernetes backend gets the same property from being declarative rather
    # than from a fingerprint: applyDeployment UPDATES the Deployment in place, and
    # an update whose pod template is unchanged does not roll the ReplicaSet, so the
    # pod is never replaced. A recreate would show up as a new pod name the moment
    # the reload's Apply lands, so the pod set is re-sampled over a settle window —
    # "nothing happened" is only falsifiable if we look for long enough to have seen
    # it happen.
    for _ in range(6):
        assert_eq(web_pods(), pods_before,
                  "the reload replaced the pod of the unchanged service 'web' — a kubernetes Apply whose pod template did not change must not roll the Deployment")
        sleep(duration = "2s")
    log("✓ reload left the unchanged service 'web' alone (same pod %s)" % pods_before[0])
else:
    # Backends whose Apply still recreates unconditionally on every Apply — that is
    # per-backend work tracked in .agents/docs/TODO.md. This branch only means
    # anything on a backend whose status id names the container itself (as
    # dockerhost's does); a backend that synthesizes the id like kubernetes needs
    # its own concrete probe, as the kube branch above has.
    assert_true(web_after["instances"][0]["id"] != web_id,
                "web kept its instance across the reload on the %s target — if that backend's Apply has become idempotent too, move it to an equality branch above" % TARGET)
    log("✓ reload brought 'web' back (as a new instance — this backend still recreates unconditionally)")

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
