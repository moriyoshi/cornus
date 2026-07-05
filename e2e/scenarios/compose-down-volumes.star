# Drive `compose down --volumes` end to end: a plain `down` leaves the project's
# named volume in place (Docker named-volume persistence), while `down --volumes`
# removes it — matching `docker compose down --volumes`.
#
# docker-only: it asserts on the concrete Docker volume via `docker volume ls`.
# The kubernetes/containerd RemoveVolume paths are unit-tested in their packages.

if TARGET != "docker":
    log("compose-down-volumes: skipped (docker-only; inspects Docker named volumes)")
else:
    compose_file = "e2e/scenarios/compose-volume.yaml"
    VOL = "e2evol_data"  # <project>_<volume>, the Docker volume dockerhost provisions

    addr = serve()
    host = {"CORNUS_HOST": "http://" + addr}

    def vol_exists():
        out = docker("volume", "ls", "--format", "{{.Name}}")
        return VOL in [line.strip() for line in out.split("\n")]

    # `up` creates the named volume.
    cornus("compose", "-f", compose_file, "up", "-d", env = host)
    wait(name = "e2evol-web", running = 1, timeout = "180s")
    assert_true(vol_exists(), "named volume was not created by up")

    # A plain `down` removes the workload but LEAVES the named volume (persistence).
    cornus("compose", "-f", compose_file, "down", env = host)
    assert_eq(status(name = "e2evol-web")["total"], 0, "web still present after down")
    assert_true(vol_exists(), "plain down removed the named volume (it should persist)")
    log("✓ plain down preserves the named volume")

    # `down --volumes` removes the named volume too, after the workload is gone.
    cornus("compose", "-f", compose_file, "up", "-d", env = host)
    wait(name = "e2evol-web", running = 1, timeout = "180s")
    cornus("compose", "-f", compose_file, "down", "-v", env = host)
    assert_true(not vol_exists(), "down --volumes did not remove the named volume")
    log("✓ down --volumes removed the named volume")
