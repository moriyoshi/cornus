# Regression: re-applying a RUNNING one-shot (restart: no) service. On the kube
# backend a one-shot deploys as a Job, and a Job's pod template is immutable, so a
# re-apply deletes the old Job (foreground, taking its pod with it) and recreates
# it. The wait-for-the-old-Job-to-clear step (waitJobGone) used a fixed ~0.3s retry
# budget — far shorter than a pod's termination grace period — so the recreate
# raced a still-terminating Job and `cornus compose up` aborted with
# `job "<name>" still terminating`, leaving the Job DELETED-but-never-recreated.
# Every second `compose up` of a project with a running one-shot service broke,
# and (because the aborting `up` tore down the client conduit) it also killed the
# streamed mounts of the OTHER services in the same project.
#
# The fixture's worker ignores SIGTERM and sets stop_grace_period, so on the
# re-apply the old Job genuinely lingers for the grace period before its pod is
# killed — the exact window the fix must wait out rather than give up on.
#
# The worker also carries an anonymous managed volume (a PVC OWNED by the Job), so
# the foreground Job delete cascades a GC of that PVC and the recreate's PVC ensure
# races the still-terminating claim. This makes the re-apply exercise BOTH fixes
# together — waitJobGone (Job clear) AND ensurePVC (wait out the terminating claim,
# recreate fresh) — the exact combination that made a real one-shot re-apply wedge
# Unschedulable ("persistentvolumeclaim ... not found").
#
# kube only: the Job replace + waitJobGone + PVC-ensure paths live in the kubernetes
# backend (pkg/deploy/kubernetes/{job,kubernetes}.go). Host backends map restart:no
# differently.

compose_file = "e2e/scenarios/compose-redeploy-oneshot.yaml"

if TARGET != "kube":
    log("compose-redeploy-oneshot: skipped (kube-only; the Job replace race is a kube backend concern)")
else:
    serve()

    # First up: the one-shot deploys as a Job whose pod runs (it sleeps, so it
    # stays Running rather than Completing).
    compose_up(file = compose_file, detach = True)
    st = wait(name = "rdoneshot-worker", running = 1, timeout = "180s")
    assert_eq(st["running"], 1, "one-shot worker should be running after the first up")
    job = kubectl("-n", "cornus-e2e", "get", "job", "rdoneshot-worker", "-o", "jsonpath={.metadata.name}")
    assert_eq(job.strip(), "rdoneshot-worker", "restart:no service must deploy as a Job, got %r" % job)
    log("✓ first up: one-shot service is a running Job")

    # Re-apply. `compose up` re-applies each spec; the running Job must be replaced
    # (delete foreground -> wait out the pod's grace period -> recreate). The buggy
    # ~0.3s wait aborted here with `job "rdoneshot-worker" still terminating`. The
    # fix must wait the lingering Job out and bring the service back up.
    compose_up(file = compose_file, detach = True)
    st = wait(name = "rdoneshot-worker", running = 1, timeout = "180s")
    assert_eq(st["running"], 1, "re-applying a running one-shot must replace its Job and come back up")
    job = kubectl("-n", "cornus-e2e", "get", "job", "rdoneshot-worker", "-o", "jsonpath={.metadata.name}")
    assert_eq(job.strip(), "rdoneshot-worker", "the Job must exist (recreated), got %r" % job)
    log("✓ redeploy: running one-shot Job replaced without a still-terminating abort")

    # The pod actually SCHEDULED and ran (running==1 above), which it could not do if
    # its owned PVC had been left terminating — the Unschedulable
    # ("persistentvolumeclaim ... not found") wedge. Assert the claim is Bound to make
    # the PVC-ensure coverage explicit, not incidental.
    # Poll: the re-apply's PVC-ensure recreates the claim asynchronously, so the
    # object can lag a moment behind the pod being Running (a bare `get` would 404
    # and fail the scenario). retry= waits that window out; a Running pod means the
    # claim is already Bound once it is visible.
    pvc = kubectl("-n", "cornus-e2e", "get", "pvc", "rdoneshot-worker-vol-0", "-o", "jsonpath={.status.phase}", retry = "30s")
    assert_eq(pvc.strip(), "Bound", "the Job-owned PVC must be Bound after re-apply, got %r" % pvc)
    log("✓ redeploy: the Job-owned PVC was recreated and Bound (no terminating-claim adoption)")

    compose_down(file = compose_file)
    log("torn down")
