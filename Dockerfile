# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY openapi ./openapi
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/update-detector ./cmd/update-detector

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

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      apt \
      update-notifier-common \
    && rm -rf /var/lib/apt/lists/*

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
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
