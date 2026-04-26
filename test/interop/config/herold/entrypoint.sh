#!/bin/bash
# entrypoint.sh - start the herold server for the interop test suite.
# Runs as the container ENTRYPOINT.
#
# Steps:
#   1. Install the interop CA root into the system trust store so herold
#      can verify peer TLS certs when doing outbound SMTP.
#   2. Run bootstrap (create domain + first principal).
#   3. Start herold server.

set -euo pipefail

log() { echo "[entrypoint:herold] $*"; }

# -----------------------------------------------------------------------
# 1. Trust the interop CA root.
#    The tls-data volume is mounted at /etc/herold/tls (read-only in this
#    container). ca.crt is written by the certgen init container.
# -----------------------------------------------------------------------
TLS_DIR=/etc/herold/tls

if [ -f "${TLS_DIR}/ca.crt" ]; then
    log "installing interop CA root into system trust store"
    cp "${TLS_DIR}/ca.crt" /usr/local/share/ca-certificates/herold-interop-ca.crt
    update-ca-certificates --fresh 2>&1 | tail -3
else
    log "WARNING: ${TLS_DIR}/ca.crt not found; TLS peer verification may fail"
fi

# -----------------------------------------------------------------------
# 2. Bootstrap: create the herold.test domain and alice@herold.test.
#    The script is idempotent: it checks whether the admin API key file
#    already exists before creating the domain/principal.
# -----------------------------------------------------------------------
log "running herold bootstrap"
/usr/local/bin/herold-bootstrap

# -----------------------------------------------------------------------
# 3. Start the server.
# -----------------------------------------------------------------------
log "starting herold server"
exec /usr/local/bin/herold server start \
    --system-config "${HEROLD_SYSTEM_CONFIG:-/etc/herold/system.toml}"
