#!/bin/sh

# Generate a throwaway self-signed cert + key pair for the loopback
# quickstart. The cert carries SubjectAltName entries for the supplied
# DOMAINNAME, "localhost", and 127.0.0.1 so a mainstream IMAP/SMTP
# client (Apple Mail, Thunderbird, mutt, swaks) connecting to
# localhost over STARTTLS / implicit TLS finds a matching identity.
# Modern TLS clients reject certs that rely on Subject CN alone, so
# the SAN block is non-optional.
#
# Usage: make-self-signed-cert.sh [DATADIR [DOMAINNAME]]

DATADIR=${1:-data}
DOMAINNAME=${2:-mail.example.local}
KEYFILE=$DATADIR/admin.key
CERTFILE=$DATADIR/admin.crt

die () {
    echo "$*" 1>&2
    exit 1
}

[ -f $KEYFILE ] && die "key file $KEYFILE exists, not overwriting"
[ -f $CERTFILE ] && die "cert file $CERTFILE exists, not overwriting"

mkdir -p "$DATADIR"

# Build a temporary OpenSSL config so we can include SubjectAltName.
# /tmp here works on every Unix that ships openssl; the file is
# deleted as soon as the cert is written.
CFGFILE=$(mktemp -t herold-self-signed-cert.XXXXXX) || die "mktemp failed"
trap 'rm -f "$CFGFILE"' EXIT INT TERM
cat > "$CFGFILE" <<EOF
[ req ]
distinguished_name = dn
prompt             = no
x509_extensions    = v3_ext
[ dn ]
CN = $DOMAINNAME
[ v3_ext ]
basicConstraints       = critical,CA:false
subjectKeyIdentifier   = hash
authorityKeyIdentifier = keyid,issuer
keyUsage               = critical,digitalSignature,keyEncipherment
extendedKeyUsage       = serverAuth,clientAuth
subjectAltName         = @alt
[ alt ]
DNS.1 = $DOMAINNAME
DNS.2 = localhost
IP.1  = 127.0.0.1
IP.2  = ::1
EOF

openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$KEYFILE" \
  -out "$CERTFILE" \
  -days 365 \
  -config "$CFGFILE" \
  -extensions v3_ext

echo "$KEYFILE and $CERTFILE were created"
echo "  Subject:           CN=$DOMAINNAME"
echo "  SubjectAltName:    DNS:$DOMAINNAME, DNS:localhost, IP:127.0.0.1, IP:::1"
echo
echo "The cert is self-signed and not in any system trust store. Mail"
echo "clients (Apple Mail, Thunderbird) will prompt the first time they"
echo "connect; accept the prompt to proceed."
