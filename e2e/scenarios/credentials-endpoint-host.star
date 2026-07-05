# An `endpoint` credential delivery served by the SERVER ITSELF on a host
# backend — no caretaker companion anywhere.
#
# An endpoint is a listener on 127.0.0.1 inside the workload's network namespace.
# On kubernetes a caretaker sidecar binds it, because a sidecar is the only thing
# inside the pod. On a host backend the server can enter that namespace directly,
# so it binds the listener itself and serves it for the session's lifetime. The
# authorization is identical either way and is the namespace boundary: the
# workload reaches 127.0.0.1 because it shares the namespace, and nothing else on
# the host can reach it at all.
#
# That absence is the regression this scenario exists for. `endpoint` was the one
# kind treated as unconditionally caretaker-bound, so declaring it on a host
# backend was refused with
#   "client-sourced credentials (endpoint delivery) ... require CORNUS_ADVERTISE_URL"
# whatever the backend could actually do. serve() below deliberately sets NEITHER
# that variable NOR CORNUS_AGENT_IMAGE, so a regression fails at deploy rather
# than in an assertion.
#
# Host counterpart of credentials.star (kube, caretaker-served). Sibling of
# credentials-env-host.star, which covers the env and file kinds.
#
# Source of truth: pkg/netnsbind (the bind), pkg/server/credential_endpoints.go
# (assign/serve/rebind), pkg/deploy/credential_endpoint.go (the backend seam).

creds = '[{"name":"db","backend":"static",' + \
        '"config":{"value":"s3cr3t-endpoint-value"},' + \
        '"deliveries":[{"kind":"endpoint","provider":"generic"}]}]'

# incus is included, and it is the interesting one. Its companion is a SIBLING
# INSTANCE — Incus exposes no way for one instance to join another's network
# namespace — so it carries neither mounts nor egress, and this backend is not a
# deploy.AttachingBackend at all. None of that stops the SERVER entering the
# instance's namespace from the host through its init pid, which is why endpoint
# deliveries work here when nothing else that needs a companion does.

# Binding inside a workload's network namespace needs CAP_SYS_ADMIN, and a
# root-owned container's /proc/<pid>/ns/net is not even readable without it. An
# unprivileged `cornus serve` on the host therefore REFUSES an endpoint delivery
# at deploy time rather than starting a workload whose credential never arrives —
# so this scenario is about the capable case, and skips otherwise. The
# containerized runner is root, which is where it normally runs.
uid = sh(cmd = "id -u")["output"].strip()

if TARGET not in ["docker", "podman", "podman-rootless", "bare", "containerd", "incus"]:
    log("credentials-endpoint-host: skipped (host-backend scenario; kube delivery is covered by credentials.star)")
elif uid != "0":
    log("credentials-endpoint-host: skipped (needs root or CAP_SYS_ADMIN to enter the workload's network namespace; an unprivileged server refuses the delivery by design)")
else:
    # No CORNUS_ADVERTISE_URL, no CORNUS_AGENT_IMAGE.
    addr = serve()

    # The host counterpart of credentials.star's helper: `cornus exec` rather than
    # pod_exec, which is kube-only. Retrying is not incidental here — the listener
    # is bound after the container starts, so an early read is legitimately
    # refused and a single-shot read would be flaky by construction.
    def read_until(app, cmd, want, steps = 30):
        out = ""
        for _ in range(steps):
            out = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, app, "sh", "-c", cmd])["output"]
            if want in out:
                return out
            sleep(duration = "2s")
        fail(msg = "%r never contained %r (last: %r)" % (cmd, want, out))

    deploy_attach(
        name = "creds-ep",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        credentials_json = creds,
        timeout = "240s",
    )
    wait(name = "creds-ep", running = 1, timeout = "240s")
    log("✓ endpoint-credential workload is up with no advertise URL and no agent image configured")

    # 1) The endpoint is advertised through the container's environment. Checked
    #    before reading it, so a failure separates "not advertised" from
    #    "advertised but not answering" — two different bugs.
    env = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "creds-ep", "sh", "-c", "printenv CORNUS_CREDENTIALS_URL"])
    assert_contains(env["output"], "127.0.0.1:", "the endpoint must be advertised on loopback inside the workload")
    log("✓ $CORNUS_CREDENTIALS_URL points at a loopback address")

    # 2) It answers, with the value the CLIENT minted. The listener was bound by
    #    the server from outside; this is the workload's own view of it.
    #
    #    read_until retries: the endpoint is bound after the container starts, so
    #    a first read can legitimately be refused. That window is the accepted
    #    trade for this kind (a connection refused is retryable; a missing file
    #    would not be).
    body = read_until("creds-ep", "wget -qO- $CORNUS_CREDENTIALS_URL", "s3cr3t-endpoint-value")
    assert_contains(body, "s3cr3t-endpoint-value", "the endpoint must serve the client-minted value")
    log("✓ read the credential over the endpoint the server bound inside the workload's netns")

    # 3) The point of the whole path: NOT ONE companion was created.
    if TARGET == "docker":
        # podman is deliberately not asserted here: the companion check shells out
        # to `docker ps`, and podman's own listing is a different command. The
        # capability it exercises comes from the same dockerhost backend.
        lst = sh(cmd = "docker ps --format '{{.Names}}' --filter label=cornus.app=creds-ep")
        assert_contains(lst["output"], "cornus-creds-ep-0", "the app instance must be running")
        if "caretaker" in lst["output"] or "mount-0" in lst["output"] or "egress-0" in lst["output"]:
            fail(msg = "a caretaker companion was created for an endpoint delivery: %r" % lst["output"])
        log("✓ no caretaker companion beside the app")
    elif TARGET == "bare":
        lst = sh(cmd = "runc --root /run/cornus/bare-runc list -q 2>/dev/null")
        assert_contains(lst["output"], "cornus-creds-ep-0", "the app instance must be running")
        if "caretaker" in lst["output"] or "creds-ep-mount" in lst["output"] or "creds-ep-egress" in lst["output"]:
            fail(msg = "a caretaker companion was created for an endpoint delivery: %r" % lst["output"])
        log("✓ no caretaker companion beside the app")

    # 4) The endpoint is NOT reachable from the host. This is the half that makes
    #    the claim about authorization mean anything: asserting only that the
    #    workload can reach it would also pass for a listener bound on the host,
    #    which would publish a live credential endpoint to every process on the
    #    machine. The URL carries the port the workload sees.
    port = env["output"].split("127.0.0.1:")[1].split("/")[0].strip()
    probe = sh(cmd = "wget -qO- --timeout=3 http://127.0.0.1:%s/ 2>&1 || echo REFUSED" % port)
    if "s3cr3t-endpoint-value" in probe["output"]:
        fail(msg = "the credential endpoint answered on the HOST at port %s; it is not confined to the workload's network namespace" % port)
    log("✓ the endpoint is unreachable from the host — the netns boundary is the authorization")

    st = status(name = "creds-ep")
    assert_eq(st["running"], 1, "Status must report exactly the app instance")

    # 5) The credential is scoped to the session that declared it: the spec's
    #    credential block is cleared once realized, so the backend must not warn
    #    that it ignored a credential it in fact delivered.
    logs = server_log()
    if "sees none of the declared credentials" in logs:
        fail(msg = "the backend warned it ignored a credential that was delivered")
    log("✓ no false 'credentials ignored' warning in the server log")

    # 6) A restarted workload gets its endpoint back. On docker the restart gives
    #    the container a NEW pid and therefore a new network namespace, so the
    #    old listener is dead and the server must re-resolve and rebind; nothing
    #    about the workload's configuration changed, so it must simply keep
    #    working. This is the assertion that a one-shot bind would fail.
    restart(name = "creds-ep")
    wait(name = "creds-ep", running = 1, timeout = "240s")
    again = read_until("creds-ep", "wget -qO- $CORNUS_CREDENTIALS_URL", "s3cr3t-endpoint-value")
    assert_contains(again, "s3cr3t-endpoint-value", "the endpoint must be rebound after the workload restarts")
    log("✓ the endpoint was rebound after a restart")
