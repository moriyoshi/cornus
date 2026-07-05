# The build touches only small.txt from the named "data" context; the multi-MiB
# big.bin sitting next to it is never read. With --lazy, the 9P export should
# transfer only the touched bytes, so served-bytes stays far below the context
# size.
FROM alpine:3.20
COPY --from=data small.txt /small.txt
RUN test -f /small.txt && echo LAZY-COPY-OK
CMD ["true"]
