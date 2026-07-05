# Multi-replica hub over the KUBERNETES-NATIVE store: the API server IS the
# registry, no Redis. Two `cornus serve` replicas with CORNUS_HUB_STORE=kube share
# the cluster the kube target already created, with JWT verification on and NO
# static CORNUS_AUTH_TOKEN.
#
# Twin of hub-multireplica-redis.star; keep the two in step. The Redis half and
# this one drifted apart once before, while they were shell scripts outside this
# suite, and nothing caught it.

SECRET = "cornus-multireplica-kube-e2e-hs256-secret"

if TARGET != "kube":
    log("skip: the KubeStore variant needs the kube target (target is " + TARGET + ")")
else:
    echo = tcp_echo()

    addr_a = "127.0.0.1:" + free_port()
    addr_b = "127.0.0.1:" + free_port()

    common = {
        "CORNUS_HUB_STORE": "kube",
        "CORNUS_JWT_HS256_SECRET": SECRET,
    }

    env_a = dict(common)
    env_a["CORNUS_REPLICA_ID"] = "repA"
    env_a["CORNUS_HUB_FORWARD_URL"] = "ws://" + addr_a
    a = serve(name = "repA", addr = addr_a, storage = "mem://", env = env_a)

    env_b = dict(common)
    env_b["CORNUS_REPLICA_ID"] = "repB"
    env_b["CORNUS_HUB_FORWARD_URL"] = "ws://" + addr_b
    b = serve(name = "repB", addr = addr_b, storage = "mem://", env = env_b)

    # Asserted, not merely observed. The reach below would still pass on the
    # legacy static-token path if peer-key publication had silently failed, so the
    # published keys are checked first: one Lease-owned, labelled CR per replica.
    keys = ""
    for _ in range(30):
        # kubectl exits 0 with empty output when nothing matches, so no error
        # tolerance is needed here -- the loop is waiting for content, not success.
        keys = kubectl(
            "get", "hubendpoints", "-A",
            "-l", "cornus.dev/hub-peer-key=true", "--no-headers",
        )
        if keys.count("hk-") >= 2:
            break
        sleep(duration = "1s")
    assert_true(keys.count("hk-") >= 2, "expected one published peer key per replica, got: " + keys)
    log("✓ both replicas published a peer public key to the API server")

    token = cornus(
        "token", "issue", "--sub", "e2e-spoke", "--scope", "api",
        "--ttl", "10m", "--hs256-secret", SECRET,
    ).strip()
    spoke_env = {"CORNUS_TOKEN": token}

    cornus_bg(
        "hub", "--server", "ws://" + a.addr, "--register", "greeter=" + echo.addr,
        env = spoke_env, log = "spoke-register",
    )
    reach = "127.0.0.1:" + free_port()
    cornus_bg(
        "hub", "--server", "ws://" + b.addr, "--reach", "greeter=" + reach,
        env = spoke_env, log = "spoke-reach",
    )

    got = dial_echo(addr = reach, line = "PING-KUBESTORE")
    assert_eq(got, "PING-KUBESTORE", "cross-replica reach did not round-trip through the API server")
    log("✓ JWT-only cross-replica delivery through the Kubernetes API server (B authenticated to A with its peer key)")
