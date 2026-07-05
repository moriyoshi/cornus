# A build whose RUN step exits non-zero. BuildKit must fail the solve and the
# server must stream a BUILD FAILED trailer (builds return HTTP 200 then stream).
FROM alpine:3.20
RUN echo "about to fail" && false
