#!/bin/sh

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

mkdir -p data
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout $KEYFILE \
  -out $CERTFILE \
  -subj "/CN=$DOMAINNAME" \
  -days 365

echo $KEYFILE and $CERTFILE were created
