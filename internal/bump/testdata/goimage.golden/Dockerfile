# The builder must be at least the go line (the golang image sets GOTOOLCHAIN=local).
FROM golang:1.26.3 AS builder
WORKDIR /src
COPY . .
RUN go build -o /agent .

FROM --platform=$BUILDPLATFORM golang:1.26.3-alpine@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb AS tools

FROM gcr.io/distroless/base-debian12 AS final
COPY --from=builder /agent /agent
