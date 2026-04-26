# syntax=docker/dockerfile:1.7
#
# Test runner image for the interop suite.
# Based on python:3.12-slim (Debian bookworm underpins).
# Includes: pytest, standard library SMTP/IMAP clients, requests.

FROM python:3.12-slim
# TODO verify tag: python:3.12-slim digest pinned 2026-04-26. Verify with:
# docker pull python:3.12-slim && docker inspect --format '{{index .RepoDigests 0}}' python:3.12-slim

RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        netcat-openbsd \
        docker-cli \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /runner

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY . .

# The CA root is mounted from the tls-data volume at runtime via
# docker-compose. Its path is in TLS_CA_BUNDLE env var.
ENV PYTHONUNBUFFERED=1
ENV PYTHONPATH=/runner

# Default: run standard suite and write junit.xml to /artifacts/junit.xml.
# run.sh overrides the junitxml path to /artifacts/${RUN_ID}/junit.xml.
CMD ["pytest", "-v", "--tb=short", \
     "--junitxml=/artifacts/junit.xml", \
     "-m", "not bulk", \
     "scenarios/"]
