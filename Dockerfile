FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod ./
COPY go.sum* ./
COPY main.go ./
COPY internal ./internal
RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/controller ./

# Pull a matching netbird release. Override NETBIRD_VERSION at build time if you
# want to track a different management version (e.g. --build-arg NETBIRD_VERSION=0.73.0).
FROM alpine:3.20 AS netbird
ARG NETBIRD_VERSION=0.72.2
ARG TARGETARCH
RUN apk add --no-cache wget tar ca-certificates && \
    case "${TARGETARCH}" in \
      amd64)  NB_ARCH=x86_64 ;; \
      arm64)  NB_ARCH=arm64 ;; \
      arm)    NB_ARCH=armv6 ;; \
      *)      echo "unsupported arch ${TARGETARCH}" && exit 1 ;; \
    esac && \
    wget -qO /tmp/netbird.tar.gz \
      "https://github.com/netbirdio/netbird/releases/download/v${NETBIRD_VERSION}/netbird_${NETBIRD_VERSION}_linux_${NB_ARCH}.tar.gz" && \
    tar -xzf /tmp/netbird.tar.gz -C /usr/local/bin netbird && \
    chmod +x /usr/local/bin/netbird

FROM alpine:3.20
LABEL org.opencontainers.image.title="netbird-aviary"
LABEL org.opencontainers.image.description="Traefik-style controller for NetBird Services driven by Docker labels"
LABEL org.opencontainers.image.source="https://github.com/ankycooper/netbird-aviary"
LABEL org.opencontainers.image.licenses="MIT"
RUN apk add --no-cache ca-certificates iptables ip6tables openresolv && \
    mkdir -p /var/lib/netbird
COPY --from=netbird /usr/local/bin/netbird /usr/local/bin/netbird
COPY --from=build /out/controller /controller
# Note: when NETBIRD_TARGET_MODE=network this image must run with --cap-add=NET_ADMIN
# and --device=/dev/net/tun. /var/lib/netbird should be a persistent volume so the
# WireGuard private key survives container restarts (default.json + state files live there).
ENTRYPOINT ["/controller"]
