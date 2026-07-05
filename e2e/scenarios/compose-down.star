# Drive `cornus compose down`'s docker-compose-like teardown end to end:
#
#  (1) A foreground `compose up` (no -d) that has gone idle self-terminates when
#      its workloads are removed by a `down` from "another terminal" — instead of
#      sitting forever holding now-defunct forwards. (kube only: on kube the
#      published-port auto-forward is held by the foreground up; on docker the
#      host publish makes the forward skip, so a mount-free up returns at once and
#      there is no idle session to terminate.)
#  (2) The default `down` waits for the workloads to fully terminate before
#      returning (synchronous, like `docker compose down`) ...
#  (3) ... and reports the teardown ("<service>  removed") rather than being silent.
#      `--no-wait` restores the fire-and-forget behavior.
#
# Uses public images so it runs on both targets without a build.

compose_file = "e2e/scenarios/compose-app.yaml"

addr = serve()

# ---- (2)+(3): default down is synchronous and reports the teardown ----
compose_up(file = compose_file, detach = True)
wait(name = "e2e-web", running = 1, timeout = "180s")
wait(name = "e2e-cache", running = 1, timeout = "180s")

out = compose_down(file = compose_file)
log(out)
assert_contains(out, "web  removed", "down did not report web removed")
assert_contains(out, "cache  removed", "down did not report cache removed")

# Synchronous: the workloads are already gone the moment down returned — asserted
# with NO polling. (A pre-change fire-and-forget down would still show instances.)
assert_eq(status(name = "e2e-web")["total"], 0, "web still present after a synchronous down")
assert_eq(status(name = "e2e-cache")["total"], 0, "cache still present after a synchronous down")
log("✓ default `compose down` waited for teardown and reported it")

# ---- (3b): --no-wait is accepted and still tears everything down ----
compose_up(file = compose_file, detach = True)
wait(name = "e2e-web", running = 1, timeout = "180s")

nowait = cornus("compose", "-f", compose_file, "down", "--no-wait", env = {"CORNUS_HOST": "http://" + addr})
log(nowait)
assert_contains(nowait, "web  removed", "--no-wait down did not report web removed")

def wait_gone(name, steps = 90):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "expected %s removed after `compose down --no-wait`" % name)

wait_gone("e2e-web")
wait_gone("e2e-cache")
log("✓ `compose down --no-wait` returned immediately and still removed the workloads")

# ---- (1): a foreground `up` self-terminates when `down` removes its workloads ----
# kube only: see the header note. Elsewhere the foreground up holds nothing for
# this app and would exit on its own, so there is no idle session to prove.
if TARGET == "kube":
    handle = compose_up_bg(file = compose_file)
    # Wait for the workload to come up under the foreground up (it is now holding
    # the published-port auto-forward and sitting idle).
    wait(name = "e2e-web", running = 1, timeout = "180s")

    # `down` from "another terminal" removes the workloads server-side.
    compose_down(file = compose_file)

    # The idle foreground up must notice and exit on its own (no signal sent to it).
    # This is deterministic regardless of whether the up had finished bringing every
    # service up: reportReconcile stops waiting the instant a workload it is watching
    # is removed (seen -> zero instances), so `down` deleting the workloads makes the
    # up reach its watchGone self-exit within a poll interval — no dependence on the
    # reconcile timeout. (Previously a service still reconciling when `down` landed
    # kept the up blocked for the full per-service cap, flaking this 60s wait.)
    res = compose_up_wait(handle = handle, timeout = "60s")
    log(res["output"])
    assert_contains(res["output"], "services removed; exiting.",
                    "foreground up did not self-terminate after its workloads were removed")
    log("✓ idle foreground `compose up` self-terminated after `down` removed its workloads")
