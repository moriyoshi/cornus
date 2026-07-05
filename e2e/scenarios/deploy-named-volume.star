# Named volume -> a SHARED, PERSISTENT PersistentVolumeClaim on kubernetes.
# Unlike an anonymous volume (per-deployment, ephemeral; see deploy-volumes.star),
# a named volume is one backing store that multiple deployments share and that
# SURVIVES `cornus delete` of any one of them (Docker named-volume semantics).
# This proves: (1) two deployments naming the same volume mount ONE claim and see
# each other's writes, and (2) the claim persists after each deployment is deleted.
#
# kube-only (the PVC path needs a real cluster); other targets skip. Validated
# 2026-07-03 inside the dind E2E runner (e2e/container, kind-in-dind): sharing,
# persistence across deletes, and the explicit cleanup all pass on a real kind.
#
# The named volume is expressed via the harness `volumes = ["name@target"]` form;
# both pods land on kind's single node, so they can share the ReadWriteOnce PVC.

NS = "cornus-e2e"  # KubeTarget's default namespace
VOL = "e2e_shared"   # logical name; the PVC carries label cornus.volume=e2e_shared

def pvc_names():
    # Names of the PVCs backing this named volume (by its stable label, so we need
    # not recompute the hashed claim name). Returns a list of "persistentvolumeclaim/..."
    out = kubectl("-n", NS, "get", "pvc", "-l", "cornus.volume=" + VOL, "-o", "name")
    return [line for line in out.split("\n") if line.strip()]

if TARGET != "kube":
    log("deploy-named-volume: skipped (kube-only; named volume -> shared persistent PVC)")
else:
    serve()

    # Deployment A mounts the named volume and writes a marker into it.
    deploy(
        name = "shareda",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        volumes = [VOL + "@/data"],
    )
    sta = wait(name = "shareda", running = 1, timeout = "240s")
    assert_eq(sta["running"], 1, "shareda pod (named-volume PVC) never became ready")

    # Exactly one PVC backs the named volume, un-owned so it will outlive deletes.
    claims = pvc_names()
    assert_eq(len(claims), 1, "named volume must back exactly one shared PVC, got %r" % claims)
    log("✓ named volume provisioned one shared PVC: %s" % claims[0])

    pod_exec(app = "shareda", cmd = "printf %s SHARED-DATA > /data/marker")
    log("✓ shareda wrote the shared marker")

    # Deployment B mounts the SAME named volume and must see A's write — proving one
    # shared backing store, not two independent volumes.
    deploy(
        name = "sharedb",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        volumes = [VOL + "@/mnt/shared"],
    )
    stb = wait(name = "sharedb", running = 1, timeout = "240s")
    assert_eq(stb["running"], 1, "sharedb pod (shared named-volume PVC) never became ready")

    # Still one PVC (shared, not one-per-deployment).
    assert_eq(len(pvc_names()), 1, "a named volume must be shared, not duplicated per deployment")
    got = pod_exec(app = "sharedb", cmd = "cat /mnt/shared/marker")
    assert_eq(got, "SHARED-DATA", "sharedb did NOT see shareda's write; the volume is not shared")
    log("✓ two deployments share one named volume (sharedb read shareda's write)")

    # Deleting one owner must NOT reclaim the shared volume: it is un-owned.
    remove(name = "shareda")
    assert_eq(len(pvc_names()), 1, "the shared PVC was reclaimed when one deployment was deleted")
    still = pod_exec(app = "sharedb", cmd = "cat /mnt/shared/marker")
    assert_eq(still, "SHARED-DATA", "shared data vanished after deleting the other deployment")
    log("✓ named volume survived `cornus delete` of one sharer (data intact)")

    # Even after the last sharer is deleted, the named volume persists (Docker
    # named-volume semantics: it lives until explicitly removed).
    remove(name = "sharedb")
    assert_eq(len(pvc_names()), 1, "named volume must persist after all sharers are deleted")
    log("✓ named volume persisted after all deployments were deleted")

    # Clean up the persistent PVC by hand (cornus never reaps a named volume).
    kubectl("-n", NS, "delete", "pvc", "-l", "cornus.volume=" + VOL)
    log("✓ named-volume PVC removed by explicit request")
