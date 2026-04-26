#!/bin/bash
# bootstrap.sh - one-shot setup for the herold interop container.
#
# Called by entrypoint.sh before the server starts.
# This script:
#   1. Generates a throwaway TLS cert for the admin listener
#      (the admin listener uses the CA-signed interop cert but also needs
#      the cert_file path to exist at parse time).
#   2. Runs `herold bootstrap` to create admin@herold.test + API key.
#   3. Starts herold in the background.
#   4. Waits for the admin API to be healthy.
#   5. Creates herold.test domain and alice@herold.test via the REST API.
#   6. Stops the background server; entrypoint.sh will re-exec it.
#
# Idempotent: if /var/lib/herold/data/.bootstrapped exists, everything
# after step 2 is skipped.

set -euo pipefail

DATA_DIR=/var/lib/herold/data
BOOTSTRAP_DONE="${DATA_DIR}/.bootstrapped"
API_KEY_FILE="${DATA_DIR}/admin_api_key.txt"
CONFIG="${HEROLD_SYSTEM_CONFIG:-/etc/herold/system.toml}"
ADMIN_URL="http://127.0.0.1:8080"
ADMIN_EMAIL="admin@herold.test"
ADMIN_PASSWORD="adminpw-interop"
ALICE_EMAIL="alice@herold.test"
ALICE_PASSWORD="alicepw-interop"

mkdir -p "${DATA_DIR}"

if [ -f "${BOOTSTRAP_DONE}" ]; then
    echo "bootstrap: already complete, skipping"
    exit 0
fi

# Step 1: Run herold bootstrap to create admin + API key.
echo "bootstrap: creating admin principal"
API_KEY=$(herold bootstrap \
    --system-config "${CONFIG}" \
    --email "${ADMIN_EMAIL}" \
    --password "${ADMIN_PASSWORD}" \
    --save-credentials=false \
    | grep "api_key:" | awk '{print $2}') || {
    rc=$?
    if [ "${rc}" = "10" ]; then
        echo "bootstrap: already bootstrapped (exit 10); continuing"
    else
        echo "bootstrap: herold bootstrap failed with rc=${rc}"
        exit "${rc}"
    fi
}

if [ -n "${API_KEY:-}" ]; then
    echo "${API_KEY}" > "${API_KEY_FILE}"
    echo "bootstrap: API key written to ${API_KEY_FILE}"
else
    if [ -f "${API_KEY_FILE}" ]; then
        API_KEY=$(cat "${API_KEY_FILE}")
        echo "bootstrap: using saved API key from ${API_KEY_FILE}"
    else
        echo "bootstrap: ERROR: no API key available"
        exit 1
    fi
fi

# Step 2: Start server in background temporarily.
echo "bootstrap: starting herold for domain/principal setup"
herold server start --system-config "${CONFIG}" &
SERVER_PID=$!
trap 'kill ${SERVER_PID} 2>/dev/null || true' EXIT

# Step 3: Wait for admin API.
echo "bootstrap: waiting for admin API at ${ADMIN_URL}"
for i in $(seq 1 30); do
    if curl -sf --insecure "${ADMIN_URL}/api/v1/healthz/live" >/dev/null 2>&1 || \
       curl -sf --insecure -H "Authorization: Bearer ${API_KEY}" \
           "${ADMIN_URL}/api/v1/domains" >/dev/null 2>&1; then
        echo "bootstrap: admin API ready (attempt ${i})"
        break
    fi
    if [ "${i}" = "30" ]; then
        echo "bootstrap: admin API did not become ready in 30s"
        exit 1
    fi
    sleep 1
done

# Step 4: Create herold.test domain.
echo "bootstrap: creating herold.test domain"
curl -sf \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"name":"herold.test"}' \
    "${ADMIN_URL}/api/v1/domains" \
    || echo "bootstrap: domain creation returned error (may already exist)"

# Step 5: Create alice@herold.test.
echo "bootstrap: creating alice@herold.test"
curl -sf \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    -d "{\"email\":\"${ALICE_EMAIL}\",\"password\":\"${ALICE_PASSWORD}\"}" \
    "${ADMIN_URL}/api/v1/principals" \
    || echo "bootstrap: principal creation returned error (may already exist)"

# Step 6: Mark done and stop background server.
touch "${BOOTSTRAP_DONE}"
echo "bootstrap: setup complete"
kill "${SERVER_PID}" 2>/dev/null || true
wait "${SERVER_PID}" 2>/dev/null || true
trap - EXIT
