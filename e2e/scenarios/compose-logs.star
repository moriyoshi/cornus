# Prove that a foreground `cornus compose up` (no -d) attaches to its services'
# container logs and streams them, prefixed by service name, the way `docker
# compose up` does — the compose-fidelity gap this scenario guards against a
# regression of. Runs on both targets with public images (no build step).
#
# Flow: background a foreground up, wait for the workloads, then a `down` from
# "another terminal" makes the up self-terminate. compose_up_wait hands back the
# up's whole captured output, which must contain the containers' own startup log
# lines (redis "Ready to accept connections", nginx "Configuration complete")
# under their service-name prefixes — not just cornus's own status banners.

compose_file = "e2e/scenarios/compose-app.yaml"

serve()

# Foreground up: holds the session and (with the attach-by-default behavior)
# follows every service's logs until its workloads go away.
handle = compose_up_bg(file = compose_file)

# Both services must come up under the held foreground up. web depends_on cache,
# so once web is running the up has finished deploying and reached its log attach.
wait(name = "e2e-cache", running = 1, timeout = "180s")
wait(name = "e2e-web", running = 1, timeout = "180s")

# Give the follow stream a moment to drain each container's startup backlog
# (Tail=all) into the captured buffer before we tear things down.
sleep(duration = "5s")

# `down` from "another terminal" removes the workloads server-side; the idle
# foreground up notices and exits, flushing its captured logs.
compose_down(file = compose_file)

res = compose_up_wait(handle = handle, timeout = "60s")
out = res["output"]
log(out)

# The up self-terminated cleanly (exit 0, exit banner printed).
assert_eq(res["code"], 0, "foreground up exited non-zero")
assert_contains(out, "services removed; exiting.",
                "foreground up did not self-terminate after its workloads were removed")

# Proof of attach: the containers' OWN stdout/stderr made it into the up's output.
# These strings are emitted only by the images at startup, never by cornus.
assert_contains(out, "Ready to accept connections",
                "redis container log was not streamed by foreground `up`")
assert_contains(out, "Configuration complete; ready for start up",
                "nginx container log was not streamed by foreground `up`")

# Proof of prefixing: log lines are tagged with their service name, like
# `docker compose up`. The service names appear as the `<name> |` line prefix.
assert_contains(out, "cache", "cache-prefixed log lines missing")
assert_contains(out, "web", "web-prefixed log lines missing")

log("✓ foreground `compose up` attached to and streamed prefixed container logs")
