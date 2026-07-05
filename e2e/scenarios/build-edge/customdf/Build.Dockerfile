FROM alpine:3.20
COPY . /app
RUN test -f /app/wanted.txt && test ! -e /app/skipme.txt && echo CUSTOMDF_OK
