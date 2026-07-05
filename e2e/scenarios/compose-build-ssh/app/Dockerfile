# A compose-built service whose build forwards the caller's SSH agent. The RUN
# lists the forwarded agent's keys with `ssh-add -l`; if forwarding did not work
# the mount is absent and the command fails (non-zero), failing the whole build.
# A successful build therefore proves `build.ssh` threaded end to end through the
# compose path (compose build -> client -> server tunnel -> caller's agent).
FROM alpine:3.20

RUN --mount=type=ssh \
    apk add --no-cache openssh-client >/dev/null && ssh-add -l && echo SSH_FORWARD_OK
