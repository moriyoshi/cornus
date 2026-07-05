# Tiny long-lived workload used by the deploy E2E scenario. It stays running so
# the deployment reports ready replicas on both the Docker and Kubernetes targets.
FROM alpine:3.20
ARG GREETING=hello
ENV GREETING=${GREETING}
CMD ["sh", "-c", "echo cornus-e2e $GREETING && sleep infinity"]
