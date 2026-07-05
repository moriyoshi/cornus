# Sample build context exercising cache + secret mounts (buildx parity).
FROM alpine:3.20

# Cache mount: the apk cache persists across builds, so a second build is fast.
RUN --mount=type=cache,target=/var/cache/apk \
    apk add --no-cache curl

# Secret mount: the secret is available only during this RUN and never lands in
# a layer.
RUN --mount=type=secret,id=token \
    test -f /run/secrets/token && echo "secret present (not stored in image)"

ARG VERSION=dev
LABEL org.opencontainers.image.version=$VERSION

CMD ["sh", "-c", "echo cornus demo $VERSION && sleep infinity"]
