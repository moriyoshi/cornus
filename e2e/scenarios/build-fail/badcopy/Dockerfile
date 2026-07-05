# A COPY whose source does not exist in the build context. The solve must fail
# (COPY cannot find the file) and surface as a BUILD FAILED trailer.
FROM alpine:3.20
COPY this-file-does-not-exist.txt /app/
