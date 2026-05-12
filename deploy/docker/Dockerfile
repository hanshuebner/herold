# syntax=docker/dockerfile:1.7

# Multi-stage build:
#   1. web-build - pnpm + Node, builds the Suite + admin SPAs.
#   2. build     - Go toolchain, builds the herold binary with the SPA
#                  artefacts copied into internal/webspa/dist/ so the
#                  //go:embed directive in
#                  internal/webspa/embed_default.go bakes them in.
#   3. runtime   - distroless/static. CGO is off in the default build
#                  (modernc.org/sqlite, pgx, bleve, nats are all pure
#                  Go) so we can ship a static binary on a minimal
#                  runtime base.

ARG GO_VERSION=1.25
ARG NODE_VERSION=20
ARG PNPM_VERSION=10

FROM node:${NODE_VERSION}-bookworm-slim AS web-build
WORKDIR /src
RUN --mount=type=cache,target=/root/.npm \
    npm install -g pnpm@${PNPM_VERSION:-10}
# Use scripts/build-web.sh as the single source of truth for the web
# build. The script handles three concerns the previous inline build
# missed: (1) bundling docs/manual/ into JSON consumed by the SPAs at
# runtime, (2) running the Markdoc SSR pass to produce the standalone
# /manual/ chapter tree, and (3) copying all three dists into
# internal/webspa/dist/{suite,admin,manual}/ so the //go:embed
# directive in the build stage picks them up. The prior inline
# pnpm-build sequence skipped the manual steps, which made the
# resulting image crash at startup with "embedded manual dist missing
# index.html" (see scripts/build-web.sh for the full eight-step
# pipeline).
COPY web ./web
COPY docs/manual ./docs/manual
COPY scripts/build-web.sh ./scripts/build-web.sh
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    chmod +x ./scripts/build-web.sh && ./scripts/build-web.sh

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

# scripts/build-web.sh in the web-build stage has already written
# the three dist trees (suite, admin, manual) into
# internal/webspa/dist/. Copy the whole subtree so the //go:embed
# directive resolves with real Vite + Markdoc output, not the
# placeholder shells.
COPY --from=web-build /src/internal/webspa/dist ./internal/webspa/dist

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -buildvcs=true \
      -ldflags "-s -w" \
      -o /out/herold ./cmd/herold

# admin_tls.source = "file" requires a cert+key pair to load at boot;
# the SMTP submission, IMAP, and IMAPS listeners in
# deploy/docker/system.toml also reference these files for their
# STARTTLS / implicit-TLS upgrade. Bake a throwaway self-signed cert
# into the image so the container boots end-to-end without any
# external file mounts. Operators with a real domain replace this by
# bind-mounting their own cert + key over /var/lib/herold/admin.{crt,key}.
RUN openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
      -subj "/CN=mail.example.local" \
      -addext "subjectAltName=DNS:mail.example.local,DNS:localhost,IP:127.0.0.1,IP:::1" \
      -keyout /out/admin.key -out /out/admin.crt \
 && chmod 0644 /out/admin.crt /out/admin.key

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
LABEL org.opencontainers.image.title="herold"
LABEL org.opencontainers.image.source="https://github.com/hanshuebner/herold"
LABEL org.opencontainers.image.licenses="MIT"

WORKDIR /var/lib/herold
COPY --from=build /out/herold /usr/local/bin/herold
COPY deploy/docker/system.toml /etc/herold/system.toml
COPY --from=build --chown=nonroot:nonroot /out/admin.crt /var/lib/herold/admin.crt
COPY --from=build --chown=nonroot:nonroot /out/admin.key /var/lib/herold/admin.key
USER nonroot:nonroot

# Volume layout:
#   /var/lib/herold  - SQLite DB, blob tree, FTS index, queue state,
#                      throwaway self-signed admin.crt / admin.key.
#                      Mount a named volume or host path here for
#                      persistent state; leave unmounted for an
#                      ephemeral, container-private testing run.
#   /etc/herold      - System configuration. The image ships a default
#                      system.toml at /etc/herold/system.toml that
#                      enables STARTTLS on the SMTP submission and IMAP
#                      listeners (using the throwaway cert) and leaves
#                      the admin / public HTTP listeners on plain TCP
#                      for loopback testing. Bind-mount over it for a
#                      real deployment.
VOLUME ["/var/lib/herold"]

# Quickstart listener ports (matches the baked-in system.toml). For a
# real deployment with the standard SMTP / IMAP / HTTPS ports, override
# system.toml and publish 25 / 465 / 587 / 143 / 993 / 4190 / 443 / 80
# instead.
EXPOSE 1025 1587 1143 1993 8080 9443 9090

ENV HEROLD_SYSTEM_CONFIG=/etc/herold/system.toml
# Default PID file lives at /var/run/herold.pid which the nonroot user
# cannot write. Steer it to the data directory instead so `herold
# server reload` (sent via `docker exec`) finds it.
ENV HEROLD_PID_FILE=/var/lib/herold/herold.pid

ENTRYPOINT ["/usr/local/bin/herold"]
CMD ["server", "start"]
