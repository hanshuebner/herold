# syntax=docker/dockerfile:1.7
#
# Interop-suite Dockerfile for herold.
# Uses debian:bookworm-slim as the runtime so we can run shell scripts for
# bootstrap (domain/principal creation via herold CLI) and install ca-certificates
# for trusting the interop CA root.
#
# The binary is built from the repo root (context: ../..) via the Go toolchain
# image, then installed into the debian runtime.

ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false \
      -ldflags "-s -w" \
      -o /out/herold ./cmd/herold

# Runtime: debian:bookworm-slim so we have apt/ca-certificates/curl available
# for bootstrap and TLS trust setup.
FROM debian:bookworm-slim

RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/herold /usr/local/bin/herold

# Bootstrap script creates the domain + principal before starting the server.
COPY test/interop/config/herold/bootstrap.sh  /usr/local/bin/herold-bootstrap
COPY test/interop/config/herold/entrypoint.sh /usr/local/bin/herold-entrypoint

RUN chmod +x /usr/local/bin/herold-bootstrap /usr/local/bin/herold-entrypoint

EXPOSE 25 143 587 993 8080 9090

ENTRYPOINT ["/usr/local/bin/herold-entrypoint"]
