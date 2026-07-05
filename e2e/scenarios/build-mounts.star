# Build mounts + SSH agent forwarding, locally and over the remote 9P/WebSocket
# path. The Dockerfile's RUN steps assert each mount with `test`, so a build that
# completes proves bind / secret / cache / ssh all worked. Requires the build
# engine (root or a rootless stack) and ssh-keygen/ssh-agent/ssh-add on the host.

ctx = "e2e/scenarios/build-mounts/context"
mounts = {
    "secret": {"tok": "e2e/scenarios/build-mounts/token.txt"},
    "build_context": {"data": "e2e/scenarios/build-mounts/data"},
}

serve()

fingerprint = ssh_agent()
log("forwarding agent key: " + fingerprint)

# Local build: the in-process engine with the caller's mounts + forwarded agent.
build(
    name = "mounts-local",
    context = ctx,
    secret = mounts["secret"],
    build_context = mounts["build_context"],
    ssh = "default",
)
log("local build: bind + secret + cache + ssh all OK")

# Remote build: the same inputs streamed to the server over 9P/WebSocket.
# no_cache forces every RUN (and thus every mount) to actually execute on the
# server rather than hit a shared cache, so this genuinely tests the wire path.
build(
    name = "mounts-remote",
    context = ctx,
    secret = mounts["secret"],
    build_context = mounts["build_context"],
    ssh = "default",
    builder = True,
    no_cache = True,
)
log("remote build (9P/WebSocket): bind + secret + cache + ssh all OK")
