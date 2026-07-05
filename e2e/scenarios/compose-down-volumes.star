# Drive `compose down --volumes` end to end: a plain `down` leaves the project's
# named volume in place (Docker named-volume persistence), while `down --volumes`
# removes it — matching `docker compose down --volumes`.
#
# Backend-parametric: the CLI path (composecli.removeProjectVolumes -> DELETE
# /.cornus/v1/volume/{name} -> deploy.VolumeRemover) is one code path, but each
# backend realizes and reaps the volume differently, so each needs its own live
# probe of the concrete artifact:
#
#   docker      a Docker named volume       `docker volume ls`
#               (pkg/deploy/dockerhost/dockerhost.go RemoveVolume)
#   kube        a shared, un-owned PVC      `kubectl get pvc -l cornus.volume=...`
#               (pkg/deploy/kubernetes/kubernetes.go RemoveVolume)
#   containerd  a host directory under      test -d <DataDir>/containerd/volumes/named/...
#               <DataDir>                   (pkg/deploy/internal/hostrun RemoveVolume)
#
# The volume RESOURCE name is the same on every backend — `<project>_<volume>`,
# compose.VolumeResourceName — which is exactly what the CLI passes to the API;
# only the artifact it names differs. The bare backend shares containerd's
# hostrun VolumeStore, so it takes the same branch.

compose_file = "e2e/scenarios/compose-volume.yaml"
VOL = "e2evol_data"  # <project>_<volume>, the resource name the CLI asks to delete
APP = "e2evol-web"
NS = "cornus-e2e"  # KubeTarget's default namespace

# containerd/bare only: the host dir backing a named volume is <CORNUS_DATA>/
# <backend>/volumes/named/<name>, so the scenario pins the server's data dir
# itself (serve(env=...) wins over the target's) to know where to look.
data_dir = ""
SUBDIR = {"containerd": "containerd", "bare": "bare"}.get(TARGET, "")

def vol_exists():
    if TARGET == "docker":
        out = docker("volume", "ls", "--format", "{{.Name}}")
        return VOL in [line.strip() for line in out.split("\n")]
    if TARGET == "kube":
        # A named volume's PVC carries the stable cornus.volume label, so we need
        # not recompute the hashed claim name (namedPVCName).
        out = kubectl("-n", NS, "get", "pvc", "-l", "cornus.volume=" + VOL, "-o", "name")
        return len([line for line in out.split("\n") if line.strip()]) > 0
    return sh(cmd = "test -d %s/%s/volumes/named/%s" % (data_dir, SUBDIR, VOL))["code"] == 0

def wait_vol_gone(steps = 45):
    # Removal is not synchronous everywhere: on kubernetes the PVC keeps its
    # pvc-protection finalizer until the last mounting pod is really gone, so it
    # lingers in Terminating for a beat after the delete is accepted.
    for _ in range(steps):
        if not vol_exists():
            return True
        sleep(duration = "2s")
    return False

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed after compose down" % name)

if TARGET not in ["docker", "kube", "containerd", "bare"]:
    log("compose-down-volumes: skipped (needs a backend that provisions named volumes)")
else:
    # Up-front cleanup: a named volume deliberately OUTLIVES its project, so one
    # left by a failed run would make the "up created it" assertion below pass
    # vacuously. (Starlark has no defer — end-of-scenario cleanup does not run on
    # failure.) The server is not up yet, so this goes straight at the artifact.
    if TARGET == "docker":
        sh(cmd = "docker volume rm -f %s 2>/dev/null; true" % VOL)
    elif TARGET == "kube":
        kubectl("-n", NS, "delete", "pvc", "-l", "cornus.volume=" + VOL, "--ignore-not-found")

    serve_env = {}
    if SUBDIR != "":
        data_dir = temp_dir()
        serve_env = {"CORNUS_DATA": data_dir}
        # remove_all() rather than rm -rf: data_dir is this scenario's own
        # temp_dir(), so the builtin's sandbox covers it and a malformed path
        # becomes a refusal instead of a deletion outside the scenario.
        remove_all(path = "%s/%s/volumes/named/%s" % (data_dir, SUBDIR, VOL))

    addr = serve(env = serve_env)
    host = {"CORNUS_HOST": "http://" + addr}

    # `up` creates the named volume.
    cornus("compose", "-f", compose_file, "up", "-d", env = host)
    wait(name = APP, running = 1, timeout = "240s")
    assert_true(vol_exists(), "named volume was not created by up")

    # A plain `down` removes the workload but LEAVES the named volume (persistence).
    cornus("compose", "-f", compose_file, "down", env = host)
    wait_gone(APP)
    assert_true(vol_exists(), "plain down removed the named volume (it should persist)")
    log("✓ plain down preserves the named volume")

    # `down --volumes` removes the named volume too, after the workload is gone.
    cornus("compose", "-f", compose_file, "up", "-d", env = host)
    wait(name = APP, running = 1, timeout = "240s")
    out = cornus("compose", "-f", compose_file, "down", "-v", env = host)

    # A backend without deploy.VolumeRemover answers 501 and the CLI soft-skips
    # with a warning instead of failing — that would make the removal assertion
    # below vacuous, so fail loudly on it here.
    assert_true(
        "does not support removing volumes" not in out,
        "the %s backend reported no volume-removal support; --volumes was silently skipped" % TARGET,
    )
    assert_true(wait_vol_gone(), "down --volumes did not remove the named volume")
    log("✓ down --volumes removed the named volume (%s)" % TARGET)
