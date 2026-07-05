# Minimal image used by the kube mount scenarios (compose-mounts / deploy-mounts)
# as BOTH the workload container and the privileged 9p mount-agent sidecar. The
# entrypoint stages the runner's own static cornus binary next to this file and
# builds it as `cornus:e2e`, then `kind load`s it into the cluster.
# Trixie, tracking the runner image's base. The binary staged next to this file is
# the runner's own, and with E2E_BUILD_TAGS="... imbh ..." it is glibc-DYNAMIC — so
# a base older than the builder's would fail to load it at runtime, in the kube
# mount scenarios only, long after the build went green. The default build is
# static and indifferent; this keeps the opt-in one honest.
FROM debian:trixie-slim
COPY cornus /usr/local/bin/cornus
ENTRYPOINT ["cornus"]
