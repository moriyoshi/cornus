# Multi-replica hub over a REAL Redis: two independent `cornus serve` processes
# agreeing through a store neither owns, with JWT verification on and NO static
# CORNUS_AUTH_TOKEN.
#
# A spoke REGISTERS "greeter" via replica A, so A owns the hosting connection; a
# spoke REACHES it via replica B. Dialing B's reach listener must forward
# B -> A -> the hosting spoke -> the echo and round-trip.
#
# The JWT-only posture is the point. With no shared static token the B -> A hop
# can only authenticate with B's PEER-scoped JWT, verified against the public key
# B published to Redis. That configuration used to fail silently: peers dialled
# each other with no credential at all. Its KubeStore twin is
# hub-multireplica-kube.star -- keep the two in step.

SECRET = "cornus-multireplica-e2e-hs256-secret"

if TARGET != "docker":
    log("skip: the redis store needs docker (target is " + TARGET + ")")
else:
    store = redis()
    echo = tcp_echo()

    # The replicas' own addresses have to exist BEFORE they start:
    # CORNUS_HUB_FORWARD_URL is what peers dial back on, so it cannot be
    # discovered afterwards.
    addr_a = "127.0.0.1:" + free_port()
    addr_b = "127.0.0.1:" + free_port()

    common = {
        "CORNUS_HUB_REDIS": store.url,
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

    got = dial_echo(addr = reach, line = "PING-MULTIREPLICA")
    assert_eq(got, "PING-MULTIREPLICA", "cross-replica reach did not round-trip through Redis")
    log("✓ JWT-only cross-replica delivery through real Redis (B authenticated to A with its peer key)")
