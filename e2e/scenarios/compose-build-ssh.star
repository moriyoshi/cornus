# `cornus compose build` forwarding the caller's SSH agent to the server's build
# engine via a service `build.ssh` section. The Dockerfile's `RUN
# --mount=type=ssh` lists the forwarded agent's keys, so a build that completes
# proves SSH threaded end to end through the compose path — the fix for
# `build.ssh` being silently dropped. Docker-only + build engine (the `build:`
# section builds through the server) and needs ssh-keygen/ssh-agent/ssh-add.

compose_file = "e2e/scenarios/compose-build-ssh.yaml"

if TARGET != "docker":
    log("compose-build-ssh: skipped (docker-only; needs the build engine + dockerhost)")
else:
    serve()

    # Start an ssh-agent with a fresh key; the compose builtin forwards
    # SSH_AUTH_SOCK so `build.ssh: [default]` resolves to it.
    fingerprint = ssh_agent()
    log("forwarding agent key: " + fingerprint)

    # Build the service: the RUN --mount=type=ssh step fails unless the agent was
    # forwarded, so a successful build is the assertion.
    compose_build(file = compose_file, project = "cbssh")
    log("✓ compose build forwarded the SSH agent (build.ssh path)")
