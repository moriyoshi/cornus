# A FROM referencing a registry host that does not resolve. The base-image pull
# must fail and surface as a BUILD FAILED trailer rather than a silent success.
FROM nonexistent.invalid/does-not-exist:latest
RUN true
