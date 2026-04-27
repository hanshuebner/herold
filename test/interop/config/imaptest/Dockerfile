# syntax=docker/dockerfile:1.7
#
# Builds an imaptest image from Dovecot's official upstream binary release.
#
# imaptest is distributed by the Dovecot project as pre-built binaries at
# https://github.com/dovecot/imaptest/releases — the same source the official
# Docker Hub image (dovecot/imaptest) uses.  We fetch the binary directly from
# the upstream release, verify its SHA-256 digest, and install it into a
# debian:13-slim runtime that provides the glibc 2.38+ required by the
# debian-13 variants of the binary.
#
# Pinned release tag: latest (2026-04-24 build, commit d413411491c4)
# Upstream SHA-256 from SHA256SUMS.txt at that release:
#
#   x86_64 / amd64 (imaptest-x86_64-debian-13):
#     1d4a9b75f03d67a537163e79a57a2593a8c6725a6a1ebe65d39e60277603c4fb
#   aarch64 / arm64 (imaptest-aarch64-debian-13):
#     69ce54ef72095cbcb5ecc85ffe4be5fc92919dadbd657105955e103a76e2e662
#
# Re-pin when upgrading: fetch SHA256SUMS.txt from the new release tag and
# update the DIGEST_* build args below.

ARG IMAPTEST_TAG=latest
ARG DIGEST_AMD64=1d4a9b75f03d67a537163e79a57a2593a8c6725a6a1ebe65d39e60277603c4fb
ARG DIGEST_ARM64=69ce54ef72095cbcb5ecc85ffe4be5fc92919dadbd657105955e103a76e2e662

# ---- download stage ----
# Uses debian:13-slim for the download stage so curl is available.
FROM debian:13-slim AS download

ARG IMAPTEST_TAG
ARG DIGEST_AMD64
ARG DIGEST_ARM64

# apt-get: no Check-Valid-Until because the development host clock may be
# ahead of the Debian mirror's InRelease freshness window.
RUN apt-get -o Acquire::Check-Valid-Until=false update -qq 2>/dev/null || \
    apt-get -o Acquire::Check-Valid-Until=false update && \
    apt-get install -y --no-install-recommends \
            ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*

# Select the correct binary for the build architecture, download it, and
# verify the SHA-256 digest before allowing the layer to commit.
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
        amd64) \
            variant="x86_64-debian-13"; \
            expected="${DIGEST_AMD64}" ;; \
        arm64) \
            variant="aarch64-debian-13"; \
            expected="${DIGEST_ARM64}" ;; \
        *) echo "Unsupported architecture: $arch"; exit 1 ;; \
    esac; \
    url="https://github.com/dovecot/imaptest/releases/download/${IMAPTEST_TAG}/imaptest-${variant}"; \
    curl -fsSL -o /tmp/imaptest "$url"; \
    echo "${expected}  /tmp/imaptest" | sha256sum -c -; \
    chmod +x /tmp/imaptest

# ---- runtime stage ----
# debian:13-slim provides glibc 2.41, satisfying the GLIBC_2.38 requirement of
# the debian-13 imaptest binaries.  The apt flags are the same future-date
# workaround as the other interop containers.
FROM debian:13-slim

RUN apt-get -o Acquire::Check-Valid-Until=false update -qq 2>/dev/null || \
    apt-get -o Acquire::Check-Valid-Until=false update && \
    apt-get install -y --no-install-recommends \
            ca-certificates libpcre2-32-0 && \
    rm -rf /var/lib/apt/lists/*

COPY --from=download /tmp/imaptest /usr/local/bin/imaptest

# Directory for the interop CA mount; populated at container startup by
# the compose command block before sleep infinity.
RUN mkdir -p /usr/local/share/ca-certificates

ENTRYPOINT ["/usr/local/bin/imaptest"]
