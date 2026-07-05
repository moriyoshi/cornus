# Exercises every build mount the caller can supply over the wire. Each RUN
# asserts its mount with `test`, so a successful build proves the mount worked
# (no separate marker assertion needed).
FROM alpine:3.20

# Bind mount of a caller directory (named build context "data").
RUN --mount=type=bind,from=data,target=/data \
    test "$(cat /data/payload.txt)" = "bind-payload" && echo BIND_OK

# Secret mount (ephemeral; not in the image layers).
RUN --mount=type=secret,id=tok \
    test "$(cat /run/secrets/tok)" = "s3cr3t" && echo SECRET_OK

# Cache mount (server-managed). Write then read back within the build.
RUN --mount=type=cache,target=/cache \
    sh -c 'echo hot > /cache/marker && test -f /cache/marker' && echo CACHE_OK

# SSH agent forwarding: the forwarded agent must list the caller's key.
RUN --mount=type=ssh \
    apk add --no-cache openssh-client >/dev/null && ssh-add -l && echo SSH_FORWARD_OK
