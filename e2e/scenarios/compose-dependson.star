# Drive depends_on condition gating: `web` declares depends_on db with
# condition: service_healthy, so `up` blocks starting web until db's healthcheck
# passes, and reports the wait ("<service>  waiting  for db (service_healthy)").
#
# Uses public images so it runs on both the Docker host and kind targets without
# a build step.

compose_file = "e2e/scenarios/compose-dependson.yaml"

addr = serve()
host = {"CORNUS_HOST": "http://" + addr}

# `up` gates web on db reaching service_healthy; the wait is always reported
# (the "waiting" event is emitted before polling), so it appears regardless of
# how fast db becomes healthy.
out = cornus("compose", "-f", compose_file, "up", "-d", env = host)
log(out)
assert_contains(out, "waiting", "up did not report waiting for the healthy dependency")

wait(name = "e2edep-db", running = 1, timeout = "180s")
wait(name = "e2edep-web", running = 1, timeout = "180s")
log("✓ up gated web on db reaching service_healthy")

cornus("compose", "-f", compose_file, "down", env = host)
assert_eq(status(name = "e2edep-web")["total"], 0, "web still present after down")
assert_eq(status(name = "e2edep-db")["total"], 0, "db still present after down")
