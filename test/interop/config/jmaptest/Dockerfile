# syntax=docker/dockerfile:1.7
#
# Builds an image that runs the jmapio/jmap-test-suite TypeScript conformance
# suite (~300 tests across RFC 8620 core + RFC 8621 mail).
#
# Pinned upstream commit (master @ 2026-04-28):
#
#   https://github.com/jmapio/jmap-test-suite/commit/0f2c117dc5f3621bfa50cb0b8b4d4ac87a4154be
#
# Re-pin by fetching a new commit SHA from the upstream master branch. The
# repo carries no runtime dependencies (build deps: typescript + @types/node;
# runtime: Node 22 built-in fetch), so reproducibility comes from the commit
# pin alone — no package-lock.json exists upstream.

ARG JMAPTEST_SHA=0f2c117dc5f3621bfa50cb0b8b4d4ac87a4154be

FROM node:22-bookworm-slim AS build

ARG JMAPTEST_SHA

# apt: same future-date workaround as the other interop containers.
RUN apt-get -o Acquire::Check-Valid-Until=false update -qq 2>/dev/null || \
    apt-get -o Acquire::Check-Valid-Until=false update && \
    apt-get install -y --no-install-recommends \
        ca-certificates git && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /opt

# Shallow clone, then check out the pinned SHA. We do this in two steps
# because GitHub does not let you clone an arbitrary SHA shallowly.
RUN git clone --depth 50 https://github.com/jmapio/jmap-test-suite.git && \
    cd jmap-test-suite && \
    git -c advice.detachedHead=false checkout "${JMAPTEST_SHA}" && \
    rm -rf .git

WORKDIR /opt/jmap-test-suite
RUN npm install --no-audit --no-fund && \
    npm run build

# ---- runtime stage ----
FROM node:22-bookworm-slim

RUN apt-get -o Acquire::Check-Valid-Until=false update -qq 2>/dev/null || \
    apt-get -o Acquire::Check-Valid-Until=false update && \
    apt-get install -y --no-install-recommends \
        ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY --from=build /opt/jmap-test-suite /opt/jmap-test-suite

# Mount point for the per-run config (baked in via the compose file's
# volume mount: ./config/jmaptest/config.json -> /etc/jmap-test/config.json).
RUN mkdir -p /etc/jmap-test /usr/local/share/ca-certificates

WORKDIR /opt/jmap-test-suite

ENTRYPOINT ["node", "/opt/jmap-test-suite/dist/cli.js"]
