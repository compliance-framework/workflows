# Stage 1: Get the binary from the upstream agent image, pinned to an agent release.
# ccf-bump rewrites this line when compliance-framework/agent releases.
FROM ghcr.io/compliance-framework/agent:0.9.3 AS source

# Stage 2: Final image with shell
FROM debian:bookworm-slim

# Install ca-certificates for SSL connections. The package version follows the base image
# (DL3008 ignored), and it has no recommended packages to skip.
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=source /app/concom /usr/local/bin/concom
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
