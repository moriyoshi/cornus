FROM alpine:3.20
COPY . /app
RUN test -f /app/keep.txt && test -f /app/sub/keep2.txt \
 && test ! -e /app/secret.env && test ! -e /app/debug.log \
 && test ! -e /app/node_modules \
 && test -f /app/.dockerignore \
 && echo DOCKERIGNORE_OK
