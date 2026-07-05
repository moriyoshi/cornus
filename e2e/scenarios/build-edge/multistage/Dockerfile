FROM alpine:3.20 AS builder
RUN printf 'built-artifact' > /out.txt
FROM alpine:3.20
COPY --from=builder /out.txt /out.txt
RUN test "$(cat /out.txt)" = "built-artifact" && echo MULTISTAGE_OK
