#!/bin/bash
# bootstrap.sh for Stalwart in the interop test suite.
# Creates bob@stalwart.test for inbound delivery tests.
# Relies on the Stalwart HTTP management API (port 8080 by default in container).

set -euo pipefail

echo "stalwart-bootstrap: waiting for admin API"
for i in $(seq 1 30); do
    if curl -sf -u "admin:adminpw-stalwart" \
        "http://127.0.0.1:8080/api/principal" >/dev/null 2>&1; then
        echo "stalwart-bootstrap: API ready (attempt ${i})"
        break
    fi
    if [ "${i}" = "30" ]; then
        echo "stalwart-bootstrap: admin API did not become ready"
        exit 1
    fi
    sleep 1
done

echo "stalwart-bootstrap: creating bob@stalwart.test"
curl -sf -u "admin:adminpw-stalwart" \
    -H "Content-Type: application/json" \
    -X POST \
    "http://127.0.0.1:8080/api/principal" \
    -d '{"type":"individual","name":"bob","secrets":["testpw-bob1"],"emails":["bob@stalwart.test"],"description":"Bob (interop test)"}' \
    || echo "stalwart-bootstrap: principal creation returned error (may already exist)"

echo "stalwart-bootstrap: done"
