# Drive Compose profiles end to end: a service gated behind `profiles:` is only
# created when its profile is activated with --profile (compose parity). Also
# exercises `down --volumes` on the teardown.
#
# Uses public images so it runs on both the Docker host and kind targets without
# a build step.

compose_file = "e2e/scenarios/compose-profiles.yaml"

addr = serve()
host = {"CORNUS_HOST": "http://" + addr}

# ---- Without the profile, `tools` is gated out; only `web` is created. ----
cornus("compose", "-f", compose_file, "up", "-d", env = host)
wait(name = "e2eprof-web", running = 1, timeout = "180s")
assert_eq(status(name = "e2eprof-tools")["total"], 0, "profiled `tools` was created without its profile active")
log("✓ profile-gated service is not created without --profile")
cornus("compose", "-f", compose_file, "down", env = host)

# ---- With --profile debug, `tools` comes up alongside `web`. ----
cornus("compose", "-f", compose_file, "--profile", "debug", "up", "-d", env = host)
wait(name = "e2eprof-web", running = 1, timeout = "180s")
wait(name = "e2eprof-tools", running = 1, timeout = "180s")
log("✓ --profile debug activates the gated service")

# `down --volumes` tears the whole (profile-activated) project down. It also
# removes named volumes; there are none here, so this just exercises the flag path.
cornus("compose", "-f", compose_file, "--profile", "debug", "down", "-v", env = host)
assert_eq(status(name = "e2eprof-web")["total"], 0, "web still present after down")
assert_eq(status(name = "e2eprof-tools")["total"], 0, "tools still present after down -v")
log("✓ compose profiles gate and activate services")
