# Tiny build-from-source service for the compose `build:` E2E. Stays alive so the
# deployment reports a ready replica.
FROM alpine:3.20
RUN echo built-by-compose > /built
CMD ["sh", "-c", "echo compose-build up && sleep infinity"]
