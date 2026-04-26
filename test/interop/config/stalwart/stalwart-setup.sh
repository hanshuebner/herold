#!/bin/sh
# stalwart-setup.sh - create bob@stalwart.test after Stalwart is ready.
# Runs in an alpine:3.20 container as the stalwart-setup service.

set -eu

apk add --no-cache curl >/dev/null 2>&1

ADMIN="http://mail.stalwart.test:8080"
AUTH="admin:adminpw-stalwart"

echo "stalwart-setup: waiting for admin API at ${ADMIN}"
i=0
while [ "${i}" -lt 30 ]; do
    if curl -sf -u "${AUTH}" "${ADMIN}/api/principal" >/dev/null 2>&1; then
        echo "stalwart-setup: API ready (attempt ${i})"
        break
    fi
    i=$((i + 1))
    if [ "${i}" -ge 30 ]; then
        echo "stalwart-setup: timed out waiting for admin API; skipping user creation"
        exit 0
    fi
    sleep 2
done

echo "stalwart-setup: registering stalwart.test domain"
curl -sf -u "${AUTH}" \
    -H "Content-Type: application/json" \
    -X POST \
    "${ADMIN}/api/principal" \
    -d '{"type":"domain","name":"stalwart.test","description":"interop test domain"}' \
    || echo "stalwart-setup: domain registration returned error (may already exist)"

echo "stalwart-setup: creating bob@stalwart.test"
curl -sf -u "${AUTH}" \
    -H "Content-Type: application/json" \
    -X POST \
    "${ADMIN}/api/principal" \
    -d '{"type":"individual","name":"bob","secrets":["testpw-bob1"],"emails":["bob@stalwart.test"],"description":"Bob (interop)","enabledPermissions":["email-receive","email-send","authenticate","imap-authenticate"]}' \
    || echo "stalwart-setup: principal creation may have failed (may already exist)"

echo "stalwart-setup: done"
