# Test image for the Agent Sandbox adapter. The static binary is built on the
# release workstation because the test node intentionally has no Go proxy
# egress during Docker builds.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 1000 aisphere \
    && adduser -S -D -u 1000 -G aisphere aisphere
COPY sandbox-manager /usr/local/bin/sandbox-manager
RUN chmod +x /usr/local/bin/sandbox-manager
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/sandbox-manager"]
