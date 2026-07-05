# One-shot (restart: no / on-failure) workloads deploy as a Kubernetes Job, not a
# Deployment. A Deployment always restarts its pods, so a finished init whose
# client-local mount session had already ended got restarted forever, and each new
# pod's caretaker reset every mount against the now-gone session -> Init:
# CrashLoopBackOff. A Job runs the pod once and does not restart a completed one.
#
# This proves, on a real cluster: (1) a restart:no workload carrying a client-local
# mount becomes a Job whose pod restartPolicy is Never (not a Deployment); (2) no
# Deployment of that name is created; (3) the 9P mount is live INSIDE the Job pod,
# so the privileged caretaker sidecar path works under a Job exactly as under a
# Deployment.
#
# kube target only: the Deployment-vs-Job mapping is a kubernetes backend concern.

if TARGET != "kube":
    log("deploy-oneshot: skipped (kube-only; the Job mapping is a kube backend concern)")
else:
    serve()
    local = temp_dir()
    write_file(path = local + "/marker", content = "ONESHOT-9P")

    # restart:no + a client-local mount. Keep the main container alive with sleep so
    # the mount is observable inside the running Job pod before it completes;
    # attach_stop tears it down. entrypoint overrides cornus:e2e's `cornus` ENTRYPOINT.
    deploy_attach(
        name = "oneshot",
        image = "cornus:e2e",
        entrypoint = ["sleep"],
        command = ["300"],
        local_mount = [local + ":/data:ro"],
        restart = "no",
        timeout = "240s",
    )

    # It is a Job with pod restartPolicy Never, and there is NO Deployment.
    job = kubectl("-n", "cornus-e2e", "get", "job", "oneshot", "-o", "jsonpath={.metadata.name}")
    assert_eq(job.strip(), "oneshot", "restart:no workload must be a Job, got %r" % job)
    rp = kubectl("-n", "cornus-e2e", "get", "job", "oneshot", "-o", "jsonpath={.spec.template.spec.restartPolicy}")
    assert_eq(rp.strip(), "Never", "one-shot Job pod restartPolicy must be Never, got %r" % rp)
    dep = kubectl("-n", "cornus-e2e", "get", "deployment", "oneshot", "--ignore-not-found", "-o", "jsonpath={.metadata.name}")
    assert_eq(dep.strip(), "", "a one-shot must NOT create a Deployment, got %r" % dep)
    log("✓ restart:no deploys as a Job (restartPolicy Never), not a Deployment")

    # The client-local 9P mount is live inside the Job pod (caretaker sidecar works
    # under a Job). This is the exact path that crashlooped as a Deployment.
    got = pod_exec(app = "oneshot", cmd = "cat /data/marker")
    assert_eq(got, "ONESHOT-9P", "mounted file must be readable inside the one-shot Job pod")
    log("✓ client-local 9P mount is live inside the one-shot Job pod")

    attach_stop(name = "oneshot")
    log("✓ one-shot Job torn down")
