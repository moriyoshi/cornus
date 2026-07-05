# Minimal context for the POST /.cornus/v1/build tar-upload E2E — a distinct, thinner
# surface than `cornus build` (no secrets / ssh / named-contexts / cache / lazy).
# COPY the uploaded context file so the build both consumes the tar AND produces a
# pushable image in cornus's own registry; the RUN cats it to stdout so a no-cache
# build streams the marker back through the endpoint, proving the tar was delivered.
FROM alpine:3.20
COPY hello.txt /hello.txt
RUN cat /hello.txt
CMD ["true"]
