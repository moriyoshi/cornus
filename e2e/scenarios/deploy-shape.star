# Generated-object shape on the kubernetes backend. Rather than only asserting a
# workload becomes Running, this scenario reads the generated Deployment / Service
# back with kubectl and asserts their EXACT shape: the container command, env, and
# ports mapping; the image-pull policy; the Service-only-when-ports-present gating;
# the stop/start scale-to-0-and-restore replica bookkeeping (cornus.dev/replicas);
# the rolling-restart annotation (cornus.dev/restartedAt); and that a plain bind
# mount is rejected outright. kube-only (needs a real cluster); other targets skip.

NS = "cornus-e2e"  # KubeTarget's default namespace

if TARGET != "kube":
    log("deploy-shape: skipped (kube-only; asserts the generated k8s object shape)")
else:
    serve()

    # A workload with a command, one env var, and a published port. The kube
    # backend maps each onto the container; reaching Running proves it applied.
    deploy(
        name = "shape",
        image = "alpine:3.20",
        entrypoint = ["sleep"],
        command = ["3600"],
        env = {"FOO": "bar"},
        ports = ["8080:80"],
    )
    st = wait(name = "shape", running = 1, timeout = "240s")
    assert_eq(st["running"], 1, "shape pod never became ready")

    # Command mapping (Docker create-time semantics, per api.DeploySpec): an
    # explicit spec.Entrypoint overrides the image ENTRYPOINT and lands in the
    # container's `command`, while spec.Command supplies the arguments to it and
    # lands in `args`. ({[*]} space-joins the range.)
    cmd = kubectl("-n", NS, "get", "deployment", "shape", "-o",
                  "jsonpath={.spec.template.spec.containers[0].command[*]}")
    assert_eq(cmd, "sleep", "spec.Entrypoint must map to container.command (got %r)" % cmd)
    cargs = kubectl("-n", NS, "get", "deployment", "shape", "-o",
                    "jsonpath={.spec.template.spec.containers[0].args[*]}")
    assert_eq(cargs, "3600", "spec.Command must map to container.args (got %r)" % cargs)

    # Env mapping: env={"FOO":"bar"} -> a container env var FOO=bar.
    envval = kubectl("-n", NS, "get", "deployment", "shape", "-o",
                     'jsonpath={.spec.template.spec.containers[0].env[?(@.name=="FOO")].value}')
    assert_eq(envval, "bar", "container env FOO not mapped (got %r)" % envval)

    # Port mapping: "8080:80" (host:container) -> containerPort 80.
    cport = kubectl("-n", NS, "get", "deployment", "shape", "-o",
                    "jsonpath={.spec.template.spec.containers[0].ports[0].containerPort}")
    assert_eq(cport, "80", "containerPort not mapped from host:container (got %r)" % cport)

    # Image-pull policy: KubeTarget sets CORNUS_K8S_IMAGE_PULL_POLICY=IfNotPresent,
    # which the backend stamps on every generated container.
    policy = kubectl("-n", NS, "get", "deployment", "shape", "-o",
                     "jsonpath={.spec.template.spec.containers[0].imagePullPolicy}")
    assert_eq(policy, "IfNotPresent", "imagePullPolicy not honored (got %r)" % policy)
    log("✓ container shape: command, env, port, and imagePullPolicy all mapped")

    # Service gating: a workload WITH ports gets a ClusterIP Service.
    svc = kubectl("-n", NS, "get", "service", "shape", "--ignore-not-found", "-o", "name")
    assert_contains(svc, "service/shape", "a workload with ports must get a Service")

    # ...and a workload with NO ports gets NO Service (service() returns nil).
    deploy(
        name = "noports",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
    )
    nosvc = kubectl("-n", NS, "get", "service", "noports", "--ignore-not-found", "-o", "name")
    assert_eq(nosvc, "", "a workload with no ports must NOT get a Service (got %r)" % nosvc)
    log("✓ Service is created only when the workload publishes ports")

    # Scale-to-0 / restore: stop scales the Deployment to 0 and remembers the
    # desired count in the cornus.dev/replicas annotation; start restores it.
    # We read the whole annotations map and substring-match it: kubectl jsonpath's
    # dotted-key bracket form (['cornus.dev/replicas']) is unreliable, and Starlark
    # rejects the `\.`-escaped alternative, so the map dump is the robust path.
    stop(name = "shape")
    stopped = wait(name = "shape", running = 0, timeout = "120s")
    assert_eq(stopped["running"], 0, "shape did not scale to 0 on stop")
    reps = kubectl("-n", NS, "get", "deployment", "shape", "-o", "jsonpath={.spec.replicas}")
    assert_eq(reps, "0", "stop did not scale the Deployment to 0 (got %r)" % reps)
    annos = kubectl("-n", NS, "get", "deployment", "shape", "-o", "jsonpath={.metadata.annotations}")
    assert_contains(annos, '"cornus.dev/replicas":"1"', "stop did not remember the desired replica count (annotations = %r)" % annos)
    log("✓ stop scaled to 0 and remembered replicas=1")

    start(name = "shape")
    restored = wait(name = "shape", running = 1, timeout = "240s")
    assert_eq(restored["running"], 1, "shape did not restore on start")
    reps2 = kubectl("-n", NS, "get", "deployment", "shape", "-o", "jsonpath={.spec.replicas}")
    assert_eq(reps2, "1", "start did not restore the remembered replica count (got %r)" % reps2)
    log("✓ start restored replicas=1 from the annotation")

    # Rolling restart: bumps a pod-template annotation (like kubectl rollout restart),
    # which must now be present. Same annotations-map dump as above (dotted-key jsonpath).
    restart(name = "shape")
    tannos = kubectl("-n", NS, "get", "deployment", "shape", "-o",
                     "jsonpath={.spec.template.metadata.annotations}")
    assert_contains(tannos, '"cornus.dev/restartedAt":', "restart did not stamp cornus.dev/restartedAt (annotations = %r)" % tannos)
    log("✓ restart stamped the pod-template restartedAt annotation")

    # Negative: the stateless kube backend rejects a plain (non-sidecar) bind mount
    # outright — Apply returns an error rather than realizing an unsafe hostPath.
    # expect_fail keeps the scenario going past the expected failure.
    deploy(
        name = "badmount",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        mounts = ["/tmp:/data"],
        expect_fail = True,
    )

    # Clean up (badmount never created anything).
    remove(name = "shape")
    remove(name = "noports")
    log("✓ deploy-shape: generated object shape verified")
