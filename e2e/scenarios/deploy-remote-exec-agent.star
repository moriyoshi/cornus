# `cornus exec --forward-agent`, across every backend that supports agent
# forwarding into an exec session: remote-mode dockerhost/containerdhost
# (CORNUS_DOCKER_REMOTE / CORNUS_CONTAINERD_REMOTE — a backend-wide mode that
# runs an always-on per-instance companion) and kubernetes (an explicit
# per-deployment opt-in, DeploySpec.AgentForward / deploy()'s
# agent_forward=True — kubernetes has no backend-wide remote mode; see
# pkg/deploy/kubernetes/kubernetes.go's addAgentForwardRole). A real local
# ssh-agent (started by ssh_agent()) is forwarded into the exec'd process's
# environment via the caretaker's AgentRelayRole — `ssh-add -l` run INSIDE the
# workload must see the SAME key fingerprint the harness's local agent holds,
# proving the fixed-socket relay (SSH_AUTH_SOCK -> caretaker's agent.sock ->
# server -> exec-agent-channel -> the harness's real agent) works end to end on
# every backend, from one shared scenario body.
#
# docker/containerd need a prebuilt cornus-embedding agent image
# (CORNUS_AGENT_IMAGE) and privileged Docker/containerd for the always-on
# companion — self-skip otherwise, mirroring deploy-mounts-sidecar-docker.star.
# kube needs no such gating: the kube target already loads a cornus:e2e
# sidecar image, and agent_forward is a plain per-deployment spec field, not an
# env-gated backend mode — so the kube branch (and its negative case below)
# runs unconditionally and is kept in the default SCENARIOS list, unlike the
# docker/containerd branches (image-gated, EXTRA_CHECK_SCENARIOS-only; see
# Makefile). Plain alpine has no ssh client by default, so the command
# installs openssh-client (needs registry/package-mirror egress, same as any
# image pull this harness already relies on) before idling.
#
# Also proves the negative on kube: a deployment applied WITHOUT agent_forward
# rejects --forward-agent with a clear error rather than silently no-oping.
# docker/containerd have no per-deployment equivalent to test from this same
# already-running server (their negative case is a DIFFERENT server process —
# a co-located, non-remote backend — out of scope here).

agent_image = getenv("CORNUS_AGENT_IMAGE", "")

if TARGET == "local":
    log("deploy-remote-exec-agent: skipped (local target has no real backend)")
elif TARGET in ("docker", "containerd") and agent_image == "":
    log("deploy-remote-exec-agent: skipped (set CORNUS_AGENT_IMAGE to a prebuilt cornus-embedding image, e.g. cornus:e2e)")
else:
    if TARGET == "docker":
        addr = serve(env = {"CORNUS_DOCKER_REMOTE": "1", "CORNUS_AGENT_IMAGE": agent_image})
    elif TARGET == "containerd":
        addr = serve(env = {"CORNUS_CONTAINERD_REMOTE": "1", "CORNUS_AGENT_IMAGE": agent_image})
    else:  # kube
        addr = serve()

    fingerprint = ssh_agent()
    log("local test agent holds: " + fingerprint)

    # No --mount at all on docker/containerd: the agent-relay socket rides the
    # SAME always-on remote-companion scratch volume every remote-mode
    # instance now gets, independent of client-local mounts. On kube,
    # agent_forward=True is the per-deployment equivalent opt-in.
    deploy(
        name = "agentfwd",
        image = "alpine:3.20",
        command = ["sh", "-c", "apk add --no-cache openssh-client >/dev/null && sleep 3600"],
        agent_forward = (TARGET == "kube"),
    )
    wait(name = "agentfwd", running = 1, timeout = "240s")
    log("✓ workload Running")

    # apk add races the container becoming "running"; give it a moment before
    # relying on ssh-add being installed.
    sleep(duration = "5s")

    got = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "--forward-agent", "agentfwd", "ssh-add", "-l"])
    assert_contains(got["output"], fingerprint, "ssh-add -l inside the workload should see the forwarded agent's key")
    log("✓ forwarded local ssh-agent visible inside the workload via cornus exec --forward-agent")

    remove(name = "agentfwd")
    wait(name = "agentfwd", running = 0, timeout = "120s")

    if TARGET == "kube":
        # Negative: a deployment applied WITHOUT agent_forward rejects
        # --forward-agent with a clear error (AgentForwardEnabled reports
        # false). exec_tty never raises on a non-zero exit (cornus exec
        # propagates the server's rejection as its own exit code via kong's
        # FatalIfErrorf) — check the exit code and the error text explicitly.
        deploy(name = "noagentfwd", image = "alpine:3.20", command = ["sleep", "3600"])
        wait(name = "noagentfwd", running = 1, timeout = "240s")
        got = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "--forward-agent", "noagentfwd", "true"])
        if got["code"] == 0:
            fail(msg = "exec --forward-agent against a deployment without agent_forward should fail, got exit 0: %r" % got)
        assert_contains(got["output"], "AgentForward", "exec --forward-agent against a deployment without agent_forward should reject with a clear error, got %r" % got)
        log("✓ --forward-agent rejected against a deployment applied without agent_forward")
        remove(name = "noagentfwd")
