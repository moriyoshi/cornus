# Lifecycle: deploy, then exercise stop / start / restart and assert the observed
# running count changes. Uses a public image so no build is required.

def wait_running(name, want, timeout_steps = 60):
    for _ in range(timeout_steps):
        if status(name = name)["running"] == want:
            return
        sleep(duration = "2s")
    fail(msg = "timed out waiting for %s to reach running=%d" % (name, want))

serve()

deploy(name = "svc", image = "nginx:1.27-alpine", replicas = 1)
wait(name = "svc", running = 1, timeout = "180s")

stop(name = "svc")
wait_running("svc", 0)
log("stopped -> 0 running")

start(name = "svc")
wait_running("svc", 1)
log("started -> 1 running")

restart(name = "svc")
wait_running("svc", 1)
log("restarted -> 1 running")

remove(name = "svc")
