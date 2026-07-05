# The RUN cats marker.txt to stdout so cache behaviour is observable: on a cache
# MISS the step executes and CACHE-RUN-EXECUTED appears in the build log; on a HIT
# it is served from cache (no stdout) and the marker never appears.
#
# The marker lives in a file, NOT in the RUN command text, because drainProgress
# prints each vertex's NAME (the full RUN command) on DONE even for a served-from-
# cache vertex — so a marker embedded in the command would false-match on a hit.
# (Same rationale as build-invalidate/Dockerfile.)
FROM alpine:3.20
COPY marker.txt /marker.txt
RUN cat /marker.txt
CMD ["true"]
