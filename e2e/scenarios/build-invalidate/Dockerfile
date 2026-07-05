# The RUN consumes dep.txt from the "data" named context so that changing dep.txt
# changes the COPY layer's content hash, which invalidates the downstream RUN.
#
# The observable marker is kept in marker.txt (a committed file this RUN cats to
# stdout), NOT in the RUN command text. This matters: drainProgress prints each
# vertex's NAME (the full RUN command) on DONE, and a LOCAL cache hit still emits
# that vertex — so a marker embedded in the command (as build-cache.star does,
# which is fine there because its registry cache-import path never streams the
# cached vertex) would false-match on a same-engine hit. By emitting the marker
# only as RUN stdout, it appears on a MISS (step executes) and is absent on a HIT
# (step served from cache: no stdout, and the command text carries no marker).
FROM alpine:3.20
COPY --from=data dep.txt /dep.txt
COPY marker.txt /marker.txt
RUN cat /dep.txt > /baked && cat /marker.txt
CMD ["true"]
