#!/bin/sh
# james-setup.sh - create dave@james.test after James WebAdmin API is ready.
# Runs in an alpine:3.20 container as the james-setup service.

set -eu

apk add --no-cache curl >/dev/null 2>&1

WEBADMIN="http://mail.james.test:8000"

echo "james-setup: waiting for WebAdmin at ${WEBADMIN}"
i=0
while [ "${i}" -lt 60 ]; do
    if curl -sf "${WEBADMIN}/domains" >/dev/null 2>&1; then
        echo "james-setup: WebAdmin ready (attempt ${i})"
        break
    fi
    i=$((i + 1))
    if [ "${i}" -ge 60 ]; then
        echo "james-setup: timed out waiting for WebAdmin; skipping user creation"
        exit 0
    fi
    sleep 2
done

echo "james-setup: registering james.test domain"
curl -sf -X PUT "${WEBADMIN}/domains/james.test" || echo "james-setup: domain may already exist"

echo "james-setup: creating dave@james.test"
curl -sf -X PUT "${WEBADMIN}/users/dave@james.test" \
    -H "Content-Type: application/json" \
    -d '{"password":"testpw-dave1"}' \
    || echo "james-setup: user may already exist"

echo "james-setup: done"
