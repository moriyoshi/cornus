# Client-sourced AWS credentials, minted by the `aws-sts` backend, delivered into
# a container via the caretaker sidecar. This is the FULL broker path with the
# real STS source backend — the client mints temporary credentials against an STS
# endpoint (the winterbaume mock: winterbaume-sts is 11/11 operations,
# state-backed), the cornus server relays them, and the pod reads them back — the
# end-to-end complement to the unit-level pkg/credential/awssts/awssts_test.go
# (same opt-in env, same mock), the way registry-s3.star complements s3_test.go.
#
# SELF-SKIPS unless CORNUS_TEST_STS_ENDPOINT names a reachable STS-compatible
# server (same opt-in env as the awssts unit test), so the default full-glob run
# stays green without one. Requires a cornus binary built with `-tags credaws`
# (the `aws-sts` backend); run it via `make e2e-credaws`, which builds that binary,
# starts winterbaume, and sets the env.
#
# Kube-only: delivery uses the caretaker sidecar. The client (the deploy-attach
# agent) runs on the harness host and reaches winterbaume there; the pod never
# needs to reach STS.

STS_ENDPOINT = getenv(name = "CORNUS_TEST_STS_ENDPOINT")

def read_until(app, cmd, want, steps = 30):
    for _ in range(steps):
        out = pod_exec(app = app, cmd = cmd)
        if want in out:
            return out
        sleep(duration = "2s")
    fail(msg = "%r never contained %r (last: %r)" % (cmd, want, out))

if STS_ENDPOINT == "":
    log("credentials-sts: skipped (set CORNUS_TEST_STS_ENDPOINT to a live STS server, e.g. winterbaume)")
elif TARGET != "kube":
    log("credentials-sts: skipped (kube-only; delivery uses the caretaker sidecar)")
else:
    serve()

    # aws-sts mints a short-lived session token against the mock STS endpoint on
    # the client, delivered to the pod as the neutral generic JSON endpoint.
    creds = '[{"name":"aws","backend":"aws-sts",' + \
            '"config":{"mode":"session-token","region":"us-east-1",' + \
            '"endpoint":"' + STS_ENDPOINT + '",' + \
            '"access_key":"test","secret_key":"test","duration":"15m"},' + \
            '"deliver":[{"kind":"endpoint","provider":"generic"}]}]'

    deploy_attach(
        name = "sts-app",
        image = "busybox:1.36",
        command = ["sleep", "3600"],
        credentials_json = creds,
        timeout = "240s",
    )
    wait(name = "sts-app", running = 1, timeout = "240s")
    log("✓ workload up (client minted temp AWS creds via STS and the caretaker delivered them)")

    # Read the minted credential back from inside the pod. A session token proves
    # it is a real STS temporary credential, not an empty/static value.
    body = read_until("sts-app", "wget -qO- $CORNUS_CREDENTIALS_URL", "SessionToken")
    assert_contains(body, "AccessKeyId", "STS-minted access key delivered into the pod")
    assert_contains(body, "SessionToken", "STS-minted session token delivered into the pod")
    log("✓ container retrieved client-minted temporary AWS credentials")

    attach_stop(name = "sts-app")
    log("✓ disconnect tore the workload down")
