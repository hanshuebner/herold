# syntax=docker/dockerfile:1.7

# Multi-stage build. Default build has no CGO (modernc.org/sqlite, pgx, bleve,
# nats are all pure Go) so the runtime image can be distroless/static.

ARG GO_VERSION=1.23

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -buildvcs=true \
      -ldflags "-s -w" \
      -o /out/herold ./cmd/herold

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
LABEL org.opencontainers.image.title="herold"
LABEL org.opencontainers.image.source="https://github.com/hanshuebner/herold"
LABEL org.opencontainers.image.licenses="MIT"

USER nonroot:nonroot
WORKDIR /var/lib/herold
COPY --from=build /out/herold /usr/local/bin/herold

EXPOSE 25 465 587 143 993 4190 8080

# Default command runs the server with config at /etc/herold/system.toml;
# operators override with a mounted config and data directory.
ENTRYPOINT ["/usr/local/bin/herold"]
CMD ["server", "start", "--system-config", "/etc/herold/system.toml"]
