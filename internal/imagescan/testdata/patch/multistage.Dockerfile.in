ARG BASE=alpine:3.24
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
RUN <<EOF
FROM is not an instruction here
EOF

FROM \
    ${BASE} \
    AS runtime
USER app
COPY --from=builder /out/app /app
ENTRYPOINT ["/app"]
