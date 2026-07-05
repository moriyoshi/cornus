# Deploy / runtime FAILURE paths on the local Docker host. The happy deploy
# scenarios prove a workload becomes Running; this proves the backend rejects or
# fails the bad cases and that a crash-on-start is observable (host backends do NOT
# synchronously wait for health). Docker-only: it asserts dockerhost-specific error
# text and host-port behavior. Uses deploy(expect_fail=True), which now returns the
# rejection message so we can assert_contains on it.
#
# pkg/deploy/dockerhost/dockerhost.go and pkg/deploy/hostpolicy/policy.go are the
# source of truth for the error strings.

if TARGET != "docker":
    log("deploy-errors: skipped (docker-only; asserts dockerhost error text + host-port conflict)")
else:
    addr = serve()

    # --- 1. Image pull failure -------------------------------------------------
    # A ref that does not exist in cornus's own registry (which the backend pulls
    # from): the pull fails and Apply returns the wrapped "pull <ref>: ..." error.
    err = deploy(name = "pullfail", image = addr + "/nope/does-not-exist:v0", expect_fail = True)
    assert_contains(err, "pull ", "image-pull failure should surface a 'pull <ref>:' error (got %r)" % err)
    log("✓ deploying a missing image fails with a pull error")

    # --- 2. Host-port conflict -------------------------------------------------
    # The first workload binds host port 19099; a second one claiming the same host
    # port must fail, and the Docker daemon's "port is already allocated" surfaces
    # verbatim through the start error.
    deploy(name = "pc1", image = "alpine:3.20", command = ["sleep", "3600"], ports = ["19099:80"])
    wait(name = "pc1", running = 1, timeout = "120s")
    err2 = deploy(name = "pc2", image = "alpine:3.20", command = ["sleep", "3600"], ports = ["19099:80"], expect_fail = True)
    assert_contains(err2, "already allocated", "a second deploy on the same host port should fail with 'port is already allocated' (got %r)" % err2)
    remove(name = "pc1")
    log("✓ a host-port conflict fails the second deploy (port is already allocated)")

    # --- 3. Privileged rejected by host policy ---------------------------------
    # The test server does NOT set CORNUS_ALLOW_PRIVILEGED, so the default-deny host
    # policy rejects a privileged container before it is created.
    err3 = deploy(name = "priv", image = "alpine:3.20", command = ["sleep", "3600"], privileged = True, expect_fail = True)
    assert_contains(err3, "privileged containers are disabled by policy", "privileged should be rejected by the default-deny host policy (got %r)" % err3)
    log("✓ a privileged container is rejected by the default-deny host policy")

    # --- 4. Crash-on-start: no synchronous health wait -------------------------
    # A container whose PID 1 exits immediately. Apply returns SUCCESS (host
    # backends only wait for the container to *start*, not to stay healthy), so the
    # deploy call itself does not error — but the workload is not actually up, which
    # Status reflects as running==0 once the process exits. restart="no" keeps it
    # from being resurrected.
    deploy(name = "crash", image = "alpine:3.20", command = ["sh", "-c", "exit 1"], restart = "no")
    dead = False
    for _ in range(30):
        st = status(name = "crash")
        if st["running"] == 0:
            dead = True
            break
        sleep(duration = "1s")
    assert_true(dead, "a crash-on-start workload should end up with running==0 (host backends do not health-gate the deploy)")
    remove(name = "crash")
    log("✓ crash-on-start deploy 'succeeds' but Status shows running==0 (no synchronous health wait)")

    # --- 5. Failed pull on redeploy must NOT tear down the running instance -----
    # POST /images/create returns HTTP 200 even when the pull fails (the error is
    # inside the streamed JSON body). imagePull used to check only the HTTP status,
    # so a failed pull looked like success: Apply then ran Delete (removing the
    # healthy running instance) and only afterwards failed on the missing image.
    # A redeploy with a bad tag must FAIL up front, BEFORE the old instance is
    # touched. Deploy a good instance, redeploy the SAME name with a nonexistent
    # tag, and assert the redeploy fails AND the original is still Running.
    deploy(name = "keep", image = "alpine:3.20", command = ["sleep", "3600"])
    wait(name = "keep", running = 1, timeout = "120s")
    err5 = deploy(name = "keep", image = addr + "/nope/still-missing:v9", expect_fail = True)
    assert_contains(err5, "pull ", "a redeploy with a bad tag should fail with a 'pull <ref>:' error (got %r)" % err5)
    st5 = status(name = "keep")
    assert_eq(st5["running"], 1, "a failed pull on redeploy must leave the previously-running instance up, not tear it down")
    remove(name = "keep")
    log("✓ a failed pull on redeploy is caught before teardown; the running instance survives")
