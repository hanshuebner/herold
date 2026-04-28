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
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml web/tsconfig.base.json ./web/
COPY web/apps ./web/apps
COPY web/packages ./web/packages
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm --dir web install --frozen-lockfile
RUN pnpm --dir web --filter @herold/suite build \
 && pnpm --dir web --filter @herold/admin build

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

# Mirror what scripts/build-web.sh does: drop the SPA dist artefacts
# into internal/webspa/dist/ so the //go:embed directive resolves with
# real Vite output, not the placeholder shells.
COPY --from=web-build /src/web/apps/suite/dist ./internal/webspa/dist/suite
COPY --from=web-build /src/web/apps/admin/dist ./internal/webspa/dist/admin

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
