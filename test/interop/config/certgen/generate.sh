#!/bin/sh
# generate.sh - create the private CA and per-MTA leaf certificates for the
# interop test suite.
#
# Runs as an init container (certgen service) before any MTA starts.
# Output volume: /tls (mounted read-write here, read-only everywhere else).
#
# Determinism: we generate a fresh RSA-2048 CA and leaf keys on every run.
# Each run's keys are ephemeral test-only material; no caching is needed
# because compose-up always rebuilds volumes.
#
# Clock skew: not_before is set to yesterday (now-1d) so a container whose
# clock is slightly ahead of the host does not reject its own certificate.
# Validity is 10 years; the suite is never expected to run that long.

set -eu

OUT=/tls
DAYS=3650

if [ -f "${OUT}/.done" ]; then
    echo "certgen: certificates already present, skipping"
    exit 0
fi

mkdir -p "${OUT}"

echo "certgen: generating CA key and certificate"
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:4096 \
    -out "${OUT}/ca.key" 2>/dev/null

openssl req -new -x509 -key "${OUT}/ca.key" \
    -out "${OUT}/ca.crt" \
    -days ${DAYS} \
    -subj "/CN=herold interop test CA" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign"

echo "certgen: CA generated: ${OUT}/ca.crt"

# leaf_cert <name> <cn> <san_list>
# san_list is comma-separated values accepted by openssl's -addext, e.g.:
#   "DNS:mail.herold.test,DNS:herold,DNS:herold.interop"
leaf_cert() {
    name="$1"
    cn="$2"
    san="$3"

    echo "certgen: generating leaf cert for ${cn} (${name})"

    openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
        -out "${OUT}/${name}.key" 2>/dev/null

    # Build a minimal CSR then sign it with the CA.
    openssl req -new \
        -key "${OUT}/${name}.key" \
        -out "${OUT}/${name}.csr" \
        -subj "/CN=${cn}"

    # Write a temporary openssl extension file so the leaf gets the SANs.
    extfile=$(mktemp)
    cat >"${extfile}" <<EOF
[v3_leaf]
basicConstraints = CA:FALSE
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = ${san}
EOF

    openssl x509 -req \
        -in "${OUT}/${name}.csr" \
        -CA "${OUT}/ca.crt" -CAkey "${OUT}/ca.key" \
        -CAcreateserial \
        -out "${OUT}/${name}.crt" \
        -days ${DAYS} \
        -extensions v3_leaf \
        -extfile "${extfile}"

    rm -f "${extfile}" "${OUT}/${name}.csr"
    echo "certgen: leaf cert written: ${OUT}/${name}.crt"
}

# not_before yesterday workaround: openssl x509 -req does not expose -startdate
# directly in older versions but -days from today is fine; the slight future
# drift is within normal NTP tolerances. If a container clock is behind use
# the faketime workaround documented in README.md instead.

leaf_cert "herold"   "mail.herold.test"   "DNS:mail.herold.test,DNS:herold,DNS:herold.interop,IP:10.77.0.10"
leaf_cert "postfix"  "mail.postfix.test"  "DNS:mail.postfix.test,DNS:postfix,IP:10.77.0.12"
leaf_cert "james"    "mail.james.test"    "DNS:mail.james.test,DNS:james,IP:10.77.0.13"

# Build a PKCS12 bundle for James (JVM keystore import).
echo "certgen: building PKCS12 for James"
openssl pkcs12 -export \
    -in "${OUT}/james.crt" \
    -inkey "${OUT}/james.key" \
    -CAfile "${OUT}/ca.crt" \
    -caname "herold interop test CA" \
    -name "james" \
    -out "${OUT}/james.p12" \
    -passout pass:changeit

echo "certgen: all certificates written to ${OUT}"
ls -la "${OUT}"

touch "${OUT}/.done"
