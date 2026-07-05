# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/update-detector ./cmd/update-detector

# Ubuntu (not distroless/alpine) on purpose: we need apt, dpkg, and Ubuntu's
# own update-notifier tooling (apt-check) to detect updates faithfully.
FROM ubuntu:22.04
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

USER update-detector
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/update-detector"]
