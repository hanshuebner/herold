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
BOB_EMAIL="bob@herold.test"
BOB_PASSWORD="bobpw-interop"

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

# Step 5b: Create bob@herold.test (secondary user the JMAP test suite needs
# for EmailSubmission tests; harmless for non-JMAP runs).
echo "bootstrap: creating bob@herold.test"
curl -sf \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    -d "{\"email\":\"${BOB_EMAIL}\",\"password\":\"${BOB_PASSWORD}\"}" \
    "${ADMIN_URL}/api/v1/principals" \
    || echo "bootstrap: bob principal creation returned error (may already exist)"

# Step 5c: Grant alice access to bob's INBOX so the JMAP test suite's
# cross-account Blob/copy and Email/copy tests have a second account
# visible in alice's session. The grant is full RFC 4314 rights minus
# admin (lrswipkxte): lookup, read, set-seen, write, insert, post,
# create-mailbox, delete-mailbox, delete-message, expunge. The "a"
# right (administer ACL) intentionally stays with bob.
#
# Unblocks the 5 cross-account jmaptest cases that previously skipped
# with "No cross-account access available":
#   binary/blob-copy-cross-account
#   binary/blob-copy-not-found
#   binary/blob-copy-response-structure
#   email/copy-cross-account
#   email/copy-not-found

resolve_principal_id() {
    local email="$1"
    curl -sf \
        -H "Authorization: Bearer ${API_KEY}" \
        "${ADMIN_URL}/api/v1/principals" \
        | jq -r ".items[] | select(.canonical_email==\"${email}\") | .id"
}

resolve_mailbox_id() {
    local pid="$1" name="$2"
    curl -sf \
        -H "Authorization: Bearer ${API_KEY}" \
        "${ADMIN_URL}/api/v1/principals/${pid}/mailboxes" \
        | jq -r ".items[] | select(.name==\"${name}\") | .id"
}

ALICE_PID=$(resolve_principal_id "${ALICE_EMAIL}")
BOB_PID=$(resolve_principal_id "${BOB_EMAIL}")
if [ -z "${ALICE_PID}" ] || [ -z "${BOB_PID}" ]; then
    echo "bootstrap: WARNING could not resolve principal ids (alice=${ALICE_PID}, bob=${BOB_PID}); skipping ACL grant"
else
    BOB_INBOX_ID=$(resolve_mailbox_id "${BOB_PID}" "INBOX")
    if [ -z "${BOB_INBOX_ID}" ]; then
        echo "bootstrap: WARNING could not resolve bob's INBOX id; skipping ACL grant"
    else
        echo "bootstrap: granting alice (pid=${ALICE_PID}) rights lrswipkxte on bob's INBOX (mailbox=${BOB_INBOX_ID})"
        curl -sf \
            -X PUT \
            -H "Authorization: Bearer ${API_KEY}" \
            -H "Content-Type: application/json" \
            -d '{"rights":"lrswipkxte"}' \
            "${ADMIN_URL}/api/v1/principals/${BOB_PID}/mailboxes/${BOB_INBOX_ID}/acl/${ALICE_PID}" \
            || echo "bootstrap: ACL grant returned error"
    fi
fi

# Step 6: Mark done and stop background server.
touch "${BOOTSTRAP_DONE}"
echo "bootstrap: setup complete"
kill "${SERVER_PID}" 2>/dev/null || true
wait "${SERVER_PID}" 2>/dev/null || true
trap - EXIT
