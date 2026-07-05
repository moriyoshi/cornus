# Compose variable interpolation sources: sibling `.env` discovery, `--env-file`
# (repeatable, later wins, REPLACES the default `.env`), and the process
# environment winning over both — asserted by what the DEPLOYED container
# actually has in its environment, not by rendering the model.
#
# Source of truth: pkg/compose/interpolate.go envMapping + the compose CLI's
# `--env-file` flag (cmd/cornus/internal/composecli/compose.go). Unit tests cover
# the mapping; nothing proved the interpolated value survives the whole
# CLI -> plan -> DeploySpec -> backend path into the running workload.
#
# Backend-agnostic (public image, no build, `cornus exec` to read the env back);
# skipped only on the local target, which has no runtime backend.

compose_file = "e2e/scenarios/compose-env-file.yaml"
APP = "e2eenv-app"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed after compose down" % name)

if TARGET == "local":
    log("compose-env-file: skipped (needs a real backend to read the container env back)")
else:
    addr = serve()
    host = {"CORNUS_HOST": "http://" + addr}

    # Up-front cleanup: a leftover workload from a failed run would answer the
    # env read-backs below with STALE values and pass vacuously.
    cornus("compose", "-f", compose_file, "down", env = host)
    wait_gone(APP)

    def env_of(var):
        # printf, not printenv: an unset variable must come back as an empty
        # string to compare against, not a non-zero exit.
        got = exec_tty(argv = [
            "cornus",
            "exec",
            "--server",
            "http://" + addr,
            APP,
            "sh",
            "-c",
            "printf '[%s]' \"$" + var + "\"",
        ])
        return got["output"]

    def down():
        cornus("compose", "-f", compose_file, "down", env = host)
        wait_gone(APP)

    # Two env files: `base` sets GREETING and WHO, `over` re-sets WHO only.
    d = temp_dir()
    write_file(path = d + "/base.env", content = "GREETING=from-base\nWHO=from-base\n")
    write_file(path = d + "/over.env", content = "WHO=from-override\n")

    cornus(
        "compose",
        "-f",
        compose_file,
        "--env-file",
        d + "/base.env",
        "--env-file",
        d + "/over.env",
        "up",
        "-d",
        env = host,
    )
    wait(name = APP, running = 1, timeout = "240s")
    assert_contains(env_of("GREETING"), "[from-base]", "value from the first --env-file did not reach the container")
    assert_contains(env_of("WHO"), "[from-override]", "the LATER --env-file must win for a key both set")
    assert_contains(env_of("EXTRA"), "[unset]", "a variable no env file sets must fall back to its literal default")
    log("✓ --env-file is repeatable and the later file wins")

    # The process environment overrides an env file (docker compose parity). The
    # env is set on the CLIENT process, which is where interpolation happens.
    down()
    cornus(
        "compose",
        "-f",
        compose_file,
        "--env-file",
        d + "/base.env",
        "up",
        "-d",
        env = {"CORNUS_HOST": "http://" + addr, "WHO": "from-process"},
    )
    wait(name = APP, running = 1, timeout = "240s")
    assert_contains(env_of("WHO"), "[from-process]", "the process environment must override an --env-file value")
    assert_contains(env_of("GREETING"), "[from-base]", "the env file must still supply keys the process env does not set")
    log("✓ the process environment overrides --env-file, per key")

    # Sibling `.env` discovery, and that --env-file REPLACES it rather than
    # merging: the project is copied to a temp dir with a .env beside it.
    down()
    proj = temp_dir()
    sh(cmd = "cp %s %s/compose.yaml" % (compose_file, proj))
    write_file(path = proj + "/.env", content = "GREETING=from-dotenv\nEXTRA=only-in-dotenv\n")

    cornus("compose", "-f", proj + "/compose.yaml", "up", "-d", env = host)
    wait(name = APP, running = 1, timeout = "240s")
    assert_contains(env_of("GREETING"), "[from-dotenv]", "the sibling .env was not discovered")
    assert_contains(env_of("EXTRA"), "[only-in-dotenv]", "the sibling .env was not discovered")
    log("✓ the compose file's sibling .env is discovered by default")

    cornus("compose", "-f", proj + "/compose.yaml", "down", env = host)
    wait_gone(APP)
    cornus("compose", "-f", proj + "/compose.yaml", "--env-file", d + "/base.env", "up", "-d", env = host)
    wait(name = APP, running = 1, timeout = "240s")
    assert_contains(env_of("GREETING"), "[from-base]", "--env-file must replace the sibling .env, not lose to it")
    assert_contains(env_of("EXTRA"), "[unset]", "--env-file must REPLACE the sibling .env, not merge with it")
    log("✓ --env-file replaces sibling-.env discovery entirely")

    # An explicitly named env file that does not exist is an error, not a silent
    # fallback to .env / the defaults.
    err = cornus("compose", "-f", compose_file, "--env-file", d + "/nope.env", "config", expect_fail = True, env = host)
    assert_contains(err, "nope.env", "a missing --env-file must name the file it could not read")
    log("✓ a missing --env-file fails loudly")

    down()
    log("torn down")
