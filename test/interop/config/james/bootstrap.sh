#!/bin/bash
# bootstrap.sh for Apache James in the interop test suite.
#
# Steps:
#   1. Import the interop CA into the JVM trust store (cacerts).
#   2. Build a JKS keystore from the leaf cert + key via PKCS12 bundle.
#   3. Create dave@james.test via the WebAdmin REST API.
#
# The PKCS12 bundle (/etc/interop/tls/james.p12) is produced by certgen.
# James expects certs in a Java Keystore (JKS) at /root/conf/keystore.

set -euo pipefail

TLS_DIR=/etc/interop/tls
JKS=/root/conf/keystore
CACERTS=$(find /usr -name cacerts 2>/dev/null | head -1)
if [ -z "${CACERTS}" ]; then
    # Fallback: inside the James image the JRE is under /usr/lib/jvm/...
    CACERTS=$(find /usr/lib/jvm -name cacerts 2>/dev/null | head -1)
fi

# -----------------------------------------------------------------------
# 1. Trust the interop CA in the JVM cacerts store.
# -----------------------------------------------------------------------
if [ -f "${TLS_DIR}/ca.crt" ] && [ -n "${CACERTS}" ]; then
    echo "james-bootstrap: importing interop CA into JVM trust store"
    keytool -importcert \
        -trustcacerts \
        -noprompt \
        -alias "herold-interop-ca" \
        -file "${TLS_DIR}/ca.crt" \
        -keystore "${CACERTS}" \
        -storepass changeit \
        2>/dev/null || echo "james-bootstrap: CA already in truststore (ignored)"
else
    echo "james-bootstrap: WARNING: could not find cacerts or CA cert; TLS trust may fail"
fi

# -----------------------------------------------------------------------
# 2. Build the JKS keystore from the PKCS12 bundle certgen produced.
# -----------------------------------------------------------------------
if [ -f "${TLS_DIR}/james.p12" ]; then
    echo "james-bootstrap: importing PKCS12 bundle into JKS keystore"
    # Remove existing JKS so keytool does not complain about alias collision.
    rm -f "${JKS}"
    keytool -importkeystore \
        -srckeystore "${TLS_DIR}/james.p12" \
        -srcstoretype PKCS12 \
        -srcstorepass changeit \
        -destkeystore "${JKS}" \
        -deststoretype JKS \
        -deststorepass changeit \
        -destkeypass changeit \
        -noprompt \
        2>/dev/null
    echo "james-bootstrap: JKS keystore written to ${JKS}"
else
    echo "james-bootstrap: WARNING: ${TLS_DIR}/james.p12 not found; James TLS will use default self-signed cert"
fi

# -----------------------------------------------------------------------
# 3. Create dave@james.test via WebAdmin.
#    James starts after this script exits (via Docker CMD), so we poll.
# -----------------------------------------------------------------------
echo "james-bootstrap: waiting for WebAdmin API on port 8000"
for i in $(seq 1 60); do
    if curl -sf "http://127.0.0.1:8000/domains" >/dev/null 2>&1; then
        echo "james-bootstrap: WebAdmin ready (attempt ${i})"
        break
    fi
    if [ "${i}" = "60" ]; then
        echo "james-bootstrap: WebAdmin did not become ready in 60s; skipping user creation"
        exit 0
    fi
    sleep 1
done

echo "james-bootstrap: registering james.test domain"
curl -sf -X PUT "http://127.0.0.1:8000/domains/james.test" \
    || echo "james-bootstrap: domain registration returned error (may already exist)"

echo "james-bootstrap: creating dave@james.test"
curl -sf -X PUT "http://127.0.0.1:8000/users/dave@james.test" \
    -H "Content-Type: application/json" \
    -d '{"password":"testpw-dave1"}' \
    || echo "james-bootstrap: user creation returned error (may already exist)"

echo "james-bootstrap: done"
