FROM alpine:3.20
RUN --mount=type=bind,from=data,target=/d \
    test -f /d/keep.bin && test ! -e /d/drop.skip && echo NAMEDIGNORE_OK
