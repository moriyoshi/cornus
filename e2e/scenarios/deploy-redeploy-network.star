# Regression: redeploy of a networked app on the HOST backends (dockerhost /
# containerdhost). Apply ensures the spec's user networks and then reused the
# full network-reaping Delete to clear existing instances — but Delete
# garbage-collects any managed network whose last member is gone. On a redeploy
# of a SOLE-member user network, that Delete deleted the very network Apply just
# ensured, so recreating the instance failed and the service was left DOWN.
# Every redeploy of a networked app was broken.
#
# docker/containerd only: the reaping Delete lives in the host backends
# (pkg/deploy/dockerhost/dockerhost.go Apply, reapNetwork; and
# pkg/deploy/containerdhost/lifecycle_linux.go Apply, reapNetwork). The kube
# backend uses the netdriver, an entirely different path — deploy-network.star
# covers that — so a kube redeploy does NOT exercise this bug and is skipped.

compose_file = "e2e/scenarios/deploy-redeploy-network.yaml"

if TARGET != "docker" and TARGET != "containerd":
    log("deploy-redeploy-network: skipped (host-backend network-reaping regression; kube uses the netdriver)")
else:
    serve()

    # First deploy: the single service comes up on its own rdnet_default network
    # and is reachable on its published port.
    compose_up(file = compose_file, detach = True)
    st = wait(name = "rdnet-web", running = 1, timeout = "180s")
    assert_eq(st["running"], 1, "networked app should be up after the first deploy")
    r1 = http_get(url = "http://127.0.0.1:18091/", retry = "30s")
    assert_eq(r1["status"], 200, "networked app should be reachable after the first deploy")
    assert_contains(r1["body"], "nginx")
    log("✓ first deploy: sole-member networked app is up and reachable")

    # Redeploy the SAME single-service spec. `compose up` re-applies each spec, so
    # Apply runs its ensure-networks -> Delete -> recreate cycle again. The buggy
    # Delete reaped rdnet_default (now member-less mid-Delete) and recreate then
    # failed on the missing network, tearing the service down. It must survive.
    compose_up(file = compose_file, detach = True)
    st = wait(name = "rdnet-web", running = 1, timeout = "180s")
    assert_eq(st["running"], 1, "a redeploy must not reap the app's own sole-member network and leave it down")
    r2 = http_get(url = "http://127.0.0.1:18091/", retry = "30s")
    assert_eq(r2["status"], 200, "networked app must still be reachable after the redeploy")
    assert_contains(r2["body"], "nginx")
    log("✓ redeploy of a sole-member networked app stayed up and reachable")

    compose_down(file = compose_file)
    log("torn down")
