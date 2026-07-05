# LIVE cross-replica CREDENTIAL forwarding: a caretaker credential fetch that
# lands on the WRONG replica is forwarded to the replica that owns the
# deploy-attach session, and is authorized THERE.
#
# The third leg of the multi-replica hub set, beside hub-multireplica-redis.star
# (cross-replica DELIVERY over Redis) and hub-multireplica-kube.star (the same
# over the API server). Those two prove a hub SERVICE reaches its hosting spoke
# across replicas; this one proves the credential relay does the same for a
# deploy-attach session, which until now had only unit coverage against miniredis
# (pkg/server/credential_multireplica_test.go).
#
# Why kube-only, and why its own file: a credential session can only exist where
# client-sourced credentials are actually realized, and that is the kubernetes
# backend alone -- dockerhost / bare / containerdhost all answer
# "client-sourced credentials are not yet supported by the <name> backend" and the
# server then drops the session (pkg/deploy/*/attachments*.go). So there is no
# docker-target twin to keep in step with, and unlike the other two this scenario
# needs a PRIMARY serve() (only that one binds every interface and advertises a
# cluster-reachable URL, which the pod's own caretaker sidecar has to dial).
#
# Shape:
#   - replica A = the primary serve(), running the KubeStore. `cornus deploy`
#     attaches to A declaring one client-sourced credential ("db", a static
#     source held by the CLI). A therefore owns the session.
#   - replica B = a second `cornus serve` PROCESS on the same store. It has never
#     terminated a deploy-attach, so its in-process session registry is empty --
#     a session lives only in the process whose WebSocket the CLI is attached to.
#   - a caretaker (the real `cornus daemon caretaker`, the same binary and roles
#     the pod sidecar runs) is pointed at B and asked for that session's "db".
#     B must resolve the session's routing record, forward the stream to A's
#     /.cornus/v1/cred/forward, and A must bridge it to the CLI's source.
#
# Negative control, in the SAME caretaker process and over the SAME connection to
# B: a second role asks B for "nope", a name the session never declared. That is
# the security property -- authorization happens at the OWNER (A re-checks
# AllowsCredential against the session it holds), not at the forwarder. A replica
# that blindly proxied whatever a peer asked for would serve it and fail here.
#
# Source of truth: pkg/server/credential_relay.go (relayCredentialRemote /
# handleCredentialForward), pkg/caretaker/credential.go (the fetch), pkg/kubehub
# (the routing record).

CRED = '[{"name":"db","backend":"static",' + \
       '"config":{"username":"cornus-user","password":"forwarded-across-replicas"},' + \
       '"deliver":[{"kind":"endpoint","provider":"generic"}]}]'

APP = "credfwd-app"
NS = "cornus-e2e"  # KubeTarget's default namespace

if TARGET != "kube":
    log("skip: client-sourced credentials are only realized by the kubernetes deploy backend (target is " + TARGET + ")")
else:
    addr_b = "127.0.0.1:" + free_port()
    ep_ok = "127.0.0.1:" + free_port()
    ep_bad = "127.0.0.1:" + free_port()

    # Replica A: the primary server, so deploy_attach()/wait() target it and the
    # pod's caretaker sidecar gets a cluster-reachable advertise URL. Its listen
    # port is picked by the harness, so CORNUS_HUB_FORWARD_URL cannot be written
    # ahead of time the way the other two scenarios do it -- POD_IP drives the
    # documented fallback instead (ws://$POD_IP:<listen port>, hubForwardAddr in
    # pkg/server/server.go), which resolves to A's own loopback address here.
    addr_a = serve(env = {
        "CORNUS_HUB_STORE": "kube",
        "CORNUS_REPLICA_ID": "credA",
        "POD_IP": "127.0.0.1",
    })

    # Replica B: a separate process, its own data dir, sharing only the store.
    #
    # CORNUS_HUB_STORE=kube is load-bearing and easy to leave off, because B never
    # PUBLISHES anything — it only ever reads. Without it B has no store to resolve
    # the session's routing record in, so it cannot know a peer owns the session
    # and takes the single-replica path instead: errCredUnknownSession, stream
    # reset, and the caretaker's delivery endpoint answers 502. The server log says
    # so in as many words ("unknown session on single-replica server"), which is
    # the line to look for if this scenario ever 502s again.
    b = serve(
        name = "credB",
        addr = addr_b,
        storage = "mem://",
        env = {
            "CORNUS_HUB_STORE": "kube",
            "CORNUS_REPLICA_ID": "credB",
            "CORNUS_HUB_FORWARD_URL": "ws://" + addr_b,
        },
    )

    # The session: `cornus deploy` stays attached to A for the workload's life,
    # holding the client-side `static` source that mints "db".
    deploy_attach(
        name = APP,
        image = "busybox:1.36",
        command = ["sleep", "3600"],
        credentials_json = CRED,
        timeout = "300s",
    )
    wait(name = APP, running = 1, timeout = "300s")
    log("✓ credential session established through replica A")

    # The session id is the unguessable capability the relay routes on. In
    # production the server injects it into the pod's caretaker config; read it
    # back from there rather than inventing a way to print it.
    raw = kubectl(
        "-n", NS, "get", "deploy", APP, "-o",
        'jsonpath={.spec.template.spec.initContainers[?(@.name=="cornus-caretaker")]' +
        '.env[?(@.name=="CORNUS_CARETAKER_CONFIG")].value}',
        retry = "60s",
    )
    injected = json.decode(raw)
    session = injected["credentials"][0]["session"]
    assert_true(session != "", "no deploy-attach session id in the injected caretaker config")

    # Ownership, asserted rather than assumed -- without this the run could pass
    # while never crossing a replica boundary at all. Two independent checks:
    #
    # 1) The relay URL the server injected for the POD is A's, not B's, so the
    #    caretaker we start below (pointed at B) is genuinely on the wrong replica.
    owner_url = injected["credentials"][0]["server"]
    assert_true(
        owner_url != "ws://" + addr_b,
        "the injected relay URL is B (" + owner_url + "); this run would not cross a replica boundary",
    )

    # 2) The session's routing record in the shared store names credA as owner and
    #    A's forward address as the hop -- i.e. the only way B can answer is by
    #    dialing A. (One record per live session; the peer-key records are "hk-"
    #    named and carry no service, so they cannot match this filter.)
    forward_a = "ws://" + addr_a
    record = ""
    for _ in range(30):
        recs = kubectl(
            "get", "hubendpoints", "-A", "-o",
            'jsonpath={range .items[*]}{.spec.service}={.spec.owner}={.spec.forwardAddr}{"\\n"}{end}',
        )
        for line in recs.split("\n"):
            if line.startswith("cornus.mount/") and line.endswith("=credA=" + forward_a):
                record = line
        if record != "":
            break
        sleep(duration = "1s")
    assert_true(
        record != "",
        "no routing record owned by credA for this session; the forward hop would be untestable",
    )
    log("✓ the session's routing record names replica A as owner (" + record + ")")

    # The caretaker: the real one, pointed at B. Both roles ride the SAME
    # connection to B (roles are grouped by server URL), so the declared and the
    # undeclared name differ in nothing but the name.
    cfg = json.encode({
        "credentials": [
            {
                "server": "ws://" + b.addr,
                "session": session,
                "name": "db",
                "deliver": [{"kind": "endpoint", "provider": "generic", "addr": ep_ok}],
            },
            {
                "server": "ws://" + b.addr,
                "session": session,
                "name": "nope",
                "deliver": [{"kind": "endpoint", "provider": "generic", "addr": ep_bad}],
            },
        ],
    })
    cornus_bg("daemon", "caretaker", "--config", cfg, log = "caretaker-on-B")

    # The positive path, through the actual data path: caretaker -> B -> A ->
    # the CLI's static source -> back. Nothing in this value exists on B.
    got = http_get(url = "http://" + ep_ok + "/credentials/db", retry = "90s")
    assert_eq(got["status"], 200, "the forwarded credential fetch did not succeed: " + got["body"])
    assert_contains(got["body"], "forwarded-across-replicas", "the forwarded fetch returned no credential value")
    assert_contains(got["body"], "cornus-user", "the forwarded fetch returned an incomplete credential")
    log("✓ a caretaker on replica B fetched a credential owned by replica A")

    # The negative control: same process, same connection to B, same (valid)
    # session -- only the name was never declared. The owner refuses it, so the
    # relay closes the stream and the delivery endpoint answers 502.
    #
    # Asserted as a REAL 502, not merely "not 200": a dial error would also be
    # "not 200", so a caretaker that never bound this endpoint (or never ran at
    # all) would satisfy a weaker check without the refusal ever happening.
    bad = http_get(url = "http://" + ep_bad + "/credentials/nope", retry = "60s", allow_error = True)
    assert_eq(
        bad.get("status", 0), 502,
        "an undeclared credential name was not refused through the forward hop",
    )
    assert_true(
        "forwarded-across-replicas" not in bad.get("body", ""),
        "the undeclared name leaked the declared credential's value",
    )
    log("✓ an undeclared credential name is refused at the owner, even over the forward hop")

    attach_stop(name = APP)
    log("✓ cross-replica credential forwarding is live: authorized at the owner, not at the forwarder")
