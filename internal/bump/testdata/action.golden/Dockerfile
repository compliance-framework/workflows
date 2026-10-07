# replaced by ghcr.io/compliance-framework/mock-agent:<version> via ccf-bump
FROM ghcr.io/compliance-framework/mock-agent:0.1.0 AS source

FROM alpine:3.20
COPY --from=source /ap[p]/mock-agen[t] /usr/local/bin/
