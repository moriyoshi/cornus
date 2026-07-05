# Minimal image used by the kube mount scenarios (compose-mounts / deploy-mounts)
# as BOTH the workload container and the privileged 9p mount-agent sidecar. The
# entrypoint stages the runner's own static cornus binary next to this file and
# builds it as `cornus:e2e`, then `kind load`s it into the cluster.
FROM debian:bookworm-slim
COPY cornus /usr/local/bin/cornus
ENTRYPOINT ["cornus"]
