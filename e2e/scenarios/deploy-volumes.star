# Anonymous volume -> a dynamically-provisioned PersistentVolumeClaim on kubernetes.
# A `volumes: ["/data"]`-style anonymous volume becomes a PVC the kube backend
# provisions; the pod only reaches Running once that PVC is provisioned, bound,
# and mounted, so wait(running=1) alone proves the PVC path. We then prove the
# mount is writable, that data survives a pod restart (the ReadWriteOnce PVC
# reattaches), and that `cornus delete` reclaims the PVC (anonymous = ephemeral).
# kube-only (the PVC path needs a real cluster); other targets skip.

NS = "cornus-e2e"  # KubeTarget's default namespace

def pvc_gone(name, steps = 30):
    for _ in range(steps):
        out = kubectl("-n", NS, "get", "pvc", name, "--ignore-not-found", "-o", "name")
        if name not in out:
            return True
        sleep(duration = "2s")
    return False

if TARGET != "kube":
    log("deploy-volumes: skipped (kube-only; anonymous volume -> dynamic PVC)")
else:
    serve()

    # alpine kept alive; the anonymous volume at /data provisions PVC "vol-vol-0".
    deploy(
        name = "vol",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        volumes = ["/data"],
    )
    # A pod backed by an unbound PVC stays Pending; reaching Running proves the PVC
    # dynamically provisioned against kind's default StorageClass, bound, and mounted.
    st = wait(name = "vol", running = 1, timeout = "240s")
    assert_eq(st["running"], 1, "pod with an anonymous-volume PVC never became ready")
    log("✓ anonymous volume: PVC provisioned + bound + mounted (pod Running)")

    # The PVC is Bound, and the mount is writable.
    phase = kubectl("-n", NS, "get", "pvc", "vol-vol-0", "-o", "jsonpath={.status.phase}")
    assert_eq(phase, "Bound", "PVC vol-vol-0 is not Bound (got %r)" % phase)
    pod_exec(app = "vol", cmd = "printf %s VOLUME-DATA > /data/marker")
    got = pod_exec(app = "vol", cmd = "cat /data/marker")
    assert_eq(got, "VOLUME-DATA", "read back the file just written on the PVC")
    log("✓ PVC vol-vol-0 Bound and writable")

    # Persistence across a pod restart: delete the pod; the Deployment recreates it
    # and the RWO PVC reattaches, so the marker file must survive.
    kubectl("-n", NS, "delete", "pod", "-l", "cornus.app=vol")
    st2 = wait(name = "vol", running = 1, timeout = "240s")
    assert_eq(st2["running"], 1, "pod did not recover after delete")
    persisted = pod_exec(app = "vol", cmd = "cat /data/marker")
    assert_eq(persisted, "VOLUME-DATA", "data did NOT persist across the pod restart")
    log("✓ data persisted across a pod restart (PVC reattached)")

    # Populate-on-first-start: a freshly provisioned PVC mounts EMPTY, unlike a
    # Docker anonymous volume the daemon seeds from the image. The kube backend adds
    # a populate initContainer to match. Mount the volume OVER alpine's /etc (which
    # the image ships with files) and confirm the image's alpine-release is visible
    # through the volume — proving the initContainer copied image content into the
    # otherwise-empty PVC before the app started.
    deploy(
        name = "volseed",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        volumes = ["/etc"],
    )
    sst = wait(name = "volseed", running = 1, timeout = "240s")
    assert_eq(sst["running"], 1, "volseed pod never became ready")
    rel = pod_exec(app = "volseed", cmd = "cat /etc/alpine-release")
    assert_true(rel.startswith("3."), "image content was not seeded into the fresh PVC (/etc/alpine-release = %r)" % rel)
    log("✓ fresh PVC seeded from image content at the mount target (populate initContainer)")
    remove(name = "volseed")

    # cornus delete removes the deployment AND the ephemeral PVC.
    remove(name = "vol")
    assert_true(pvc_gone("vol-vol-0"), "PVC vol-vol-0 survived cornus delete")
    log("✓ cornus delete reclaimed the PVC")
