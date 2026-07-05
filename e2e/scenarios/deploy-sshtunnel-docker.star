# SSH-tunnel connection profile: reach the served cornus through a real sshd,
# with NO -H/--host on the command line — the endpoint is an ssh-tunnel context
# that dials the server over SSH (pure-Go dialer, no local port). This proves the
# whole path: config file -> ssh-tunnel context -> sshclient dialer -> SSH -> server.
#
# docker-only: it needs a runtime backend to deploy the workload AND a local sshd
# whose direct-tcpip forward reaches the same-host server. It self-skips when no
# sshd binary is available (see the sshd() builtin).

if TARGET != "docker":
    log("deploy-sshtunnel: skipped (docker-only: needs a runtime backend + local sshd)")
else:
    addr = serve()  # 127.0.0.1:PORT the tunnel forwards to (same host as sshd)

    ssh = sshd()
    if ssh == None:
        log("deploy-sshtunnel: skipped (no sshd binary)")
    else:
        # Hermetic client config under a throwaway dir, so the test never touches
        # the developer's real ~/.config/cornus/config.yaml.
        workdir = temp_dir()
        cfg = workdir + "/config.yaml"
        env = {"CORNUS_CONFIG": cfg}

        # Create an ssh-tunnel profile: SSH to the local sshd, forward to the
        # served cornus. --ssh-no-config keeps it independent of any host ssh_config;
        # --ssh-no-agent makes auth deterministic (identity file only).
        cornus(
            "config", "set-context", "devbox",
            "--ssh-host", ssh["addr"],
            "--ssh-user", ssh["user"],
            "--ssh-identity-file", ssh["identity"],
            "--ssh-known-hosts", ssh["known_hosts"],
            "--ssh-remote-addr", addr,
            "--ssh-no-config",
            "--ssh-no-agent",
            env = env,
        )
        cornus("config", "use-context", "devbox", env = env)

        # get-contexts renders the ssh-tunnel profile (no plain server endpoint).
        ctxs = cornus("config", "get-contexts", env = env)
        assert_contains(ctxs, "devbox", "get-contexts should list the profile")
        assert_contains(ctxs, "ssh-tunnel", "get-contexts should show the ssh-tunnel rendering")
        assert_contains(ctxs, "*", "get-contexts should mark the current context")
        log("✓ ssh-tunnel profile stored and selected")

        # Deploy a compose project through the tunnel alone (argv carries no -H).
        compose_file = "e2e/scenarios/deploy-sshtunnel-app.yaml"
        cornus("compose", "-f", compose_file, "up", "-d", env = env)

        ps = cornus("compose", "-f", compose_file, "ps", env = env)
        assert_contains(ps, "web", "compose ps (via ssh tunnel) should list the service")
        log("✓ compose reached the served cornus through the SSH tunnel")

        # Cross-check on the server itself (harness client -> same server) that the
        # tunnel-driven `compose up` actually deployed the workload.
        st = wait(name = "sshtun-web", running = 1, timeout = "180s")
        assert_eq(st["running"], 1, "workload deployed via the ssh tunnel should be running")
        log("✓ workload confirmed running on the server")

        cornus("compose", "-f", compose_file, "down", env = env)
        log("✓ ssh-tunnel deploy/ps/down all succeeded")
