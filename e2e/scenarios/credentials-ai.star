# Local-AI-credential env delivery: a developer's local credential is sourced on
# the client and injected into the container's ENVIRONMENT without ever appearing
# in the pod spec as a literal. The server fetches the value once at deploy time
# over the held session, materializes it into a Kubernetes Secret, and the app
# container references it via secretKeyRef. We read it back with `printenv` to
# prove the whole client -> server deploy-time-fetch -> Secret -> container-env
# path landed the value.
#
# This is the hermetic end-to-end for the env delivery (a `static` source stands
# in for a local ANTHROPIC_API_KEY / OPENAI_API_KEY). The auth-injecting proxy
# providers (anthropic-proxy / openai-proxy) forward to the real vendor API, so
# they are covered by unit tests rather than a hermetic cluster run.
#
# Kube-only: the delivery uses the kubernetes credential attach path.
#
# Source of truth: pkg/credential (client source), pkg/server/deploy_attach.go
# (deploy-time fetch), pkg/deploy/kubernetes (Secret + secretKeyRef).

creds = '[{"name":"aikey","backend":"static",' + \
        '"config":{"value":"sk-injected-env-value"},' + \
        '"deliver":[{"kind":"env","envVar":"MY_AI_API_KEY"}]}]'

def read_until(app, cmd, want, steps = 30):
    for _ in range(steps):
        out = pod_exec(app = app, cmd = cmd)
        if want in out:
            return out
        sleep(duration = "2s")
    fail(msg = "%r never contained %r (last: %r)" % (cmd, want, out))

if TARGET != "kube":
    log("credentials-ai: skipped (kube-only; the credential attach path)")
else:
    serve()

    # busybox app kept alive with sleep; the credential value is injected as an env
    # var sourced from a Kubernetes Secret the server created at deploy time.
    deploy_attach(
        name = "ai-env",
        image = "busybox:1.36",
        command = ["sleep", "3600"],
        credentials_json = creds,
        timeout = "240s",
    )
    wait(name = "ai-env", running = 1, timeout = "240s")
    log("✓ workload is up with the injected credential env var")

    body = read_until("ai-env", "printenv MY_AI_API_KEY", "sk-injected-env-value")
    assert_contains(body, "sk-injected-env-value", "credential env var injected from the Secret")
    log("✓ container read the client-sourced credential from $MY_AI_API_KEY")

    attach_stop(name = "ai-env")
    log("✓ disconnect tore the workload down")
