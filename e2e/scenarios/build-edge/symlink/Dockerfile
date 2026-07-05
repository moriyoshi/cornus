FROM alpine:3.20
COPY . /app
RUN test -L /app/link.txt \
 && test "$(cat /app/link.txt)" = "real-data" \
 && echo SYMLINK_OK
