# Caretaker `docker` role: a Docker Engine API endpoint exposed to the workload on
# a pod-loopback address, so a container can drive the same cornus server that
# manages its own stack — loopback access to the managed stack, with no real Docker
# daemon in the pod.
#
# The caretaker (cornus:e2e) runs the pkg/dockerproxy Docker Engine API proxy on
# 127.0.0.1:2375 in the shared pod netns, and the kubernetes backend injects
# DOCKER_HOST into the app container. We read both back from inside a plain busybox
# app to prove the endpoint is live and reachable over loopback, and that the Docker
# API responds identifying itself as the cornus proxy (not a stray daemon).
#
# Kube-only: the docker role rides the caretaker sidecar (like the mount/credential
# scenarios). The app image is plain busybox; the caretaker runs the cornus sidecar
# image. Source of truth: pkg/caretaker (docker role + runDocker), pkg/dockerproxy
# (the API surface), pkg/deploy/kubernetes (addDockerRole / injectDocker).

def read_until(app, cmd, want, steps = 30):
    # The caretaker startup probe (caretaker-check -> dockerReady) gates the app
    # until the endpoint is bound, so this should pass on the first try; retry a
    # little to absorb pod-churn races.
    for _ in range(steps):
        out = pod_exec(app = app, cmd = cmd)
        if want in out:
            return out
        sleep(duration = "2s")
    fail(msg = "%r never contained %r (last: %r)" % (cmd, want, out))

if TARGET != "kube":
    log("caretaker docker endpoint: skipped (kube-only; runs on the caretaker sidecar)")
else:
    serve()

    # busybox app kept alive with sleep; the caretaker runs the Docker-API proxy on
    # loopback and the backend injects DOCKER_HOST pointing at it. No client token is
    # needed here because the harness server is unauthenticated.
    deploy(
        name = "docker-endpoint-app",
        image = "busybox:1.36",
        command = ["sleep", "3600"],
        docker = "tcp",
    )
    wait(name = "docker-endpoint-app", running = 1, timeout = "240s")
    log("✓ workload is up (caretaker docker endpoint passed its startup probe)")

    # 1) DOCKER_HOST advertised to the app container.
    dh = read_until("docker-endpoint-app", "printenv DOCKER_HOST", "tcp://127.0.0.1:2375")
    assert_contains(dh, "tcp://127.0.0.1:2375", "DOCKER_HOST injected into the app container")
    log("✓ DOCKER_HOST reaches the app container")

    # 2) The endpoint answers on loopback: /_ping is the Docker daemon liveness check.
    ping = read_until("docker-endpoint-app", "wget -qO- http://127.0.0.1:2375/_ping", "OK")
    assert_contains(ping, "OK", "docker /_ping over the pod loopback")
    log("✓ docker /_ping answered over the pod loopback")

    # 3) The Docker Engine API responds and identifies itself as the cornus proxy —
    #    proving it is our caretaker-hosted endpoint, not a stray daemon.
    ver = read_until("docker-endpoint-app", "wget -qO- http://127.0.0.1:2375/version", "cornus-docker-proxy")
    assert_contains(ver, "ApiVersion", "docker /version served the Docker API shape")
    log("✓ the Docker API endpoint is the cornus proxy (docker /version)")

    remove(name = "docker-endpoint-app")
    log("✓ removed the docker-endpoint workload")
