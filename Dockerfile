# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY openapi ./openapi
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-X update-detector/internal/version.Version=${VERSION}" -o /out/update-detector ./cmd/update-detector

# Ubuntu (not distroless/alpine) on purpose: we need apt, dpkg, and Ubuntu's
# own update-notifier tooling (apt-check) to detect updates faithfully.
#
# Pin to the newest LTS, not the oldest host release you expect to support:
# apt-check's security-pocket classification is version-sensitive, and newer
# apt/python-apt reliably reads older releases' package metadata, but the
# reverse isn't guaranteed. Confirmed empirically — apt-check from a 22.04
# image misclassified noble-security packages as regular updates on a 24.04
# host; identical apt-check from a 24.04 image classified them correctly
# against the exact same host state.
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive

RUN for i in 1 2 3; do \
      apt-get update -o Acquire::Retries=5 -o Acquire::http::Timeout=30 && \
      apt-get install -y --no-install-recommends \
        ca-certificates apt curl debsecan update-notifier-common && \
      { rm -rf /var/lib/apt/lists/*; exit 0; }; \
      echo "apt attempt $i failed, retrying in 15s..." >&2; \
      sleep 15; \
    done; \
    exit 1

# Note: trivy is NOT baked into the image. It is downloaded on first use
# by internal/cve/debian (ensureTrivy) and cached in /var/lib/update-detector/
# tools (the bind-mounted state dir), so the image stays small and the
# binary survives container restarts. debsecan stays apt-installed because
# it is tiny and available in the Ubuntu repos.

RUN useradd --system --uid 10001 --shell /usr/sbin/nologin update-detector \
    && mkdir -p /var/lib/update-detector \
    && chown -R update-detector:update-detector /var/lib/update-detector /var/cache/apt/archives

COPY --from=builder /out/update-detector /usr/local/bin/update-detector
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Starts as root (needed to chown a bind-mounted state dir -- see
# docker-entrypoint.sh) and drops to the unprivileged update-detector user
# before exec'ing the actual binary, which is what actually runs for the
# life of the container.
EXPOSE 8080

# Liveness probe for Docker (and Homepage's Docker integration, which
# surfaces this as the container's health state): /healthz reports process
# liveness only, so a green check means "the agent is up", not "the host
# is fully patched" -- that's /status's `ok` field instead.
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
  CMD curl -fsS http://localhost:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
