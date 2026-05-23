#!/usr/bin/env bash
#
# scripts/smoke-docker.sh - drive the README quickstart against a herold
# Docker image and assert it works end-to-end.
#
# Usage:
#   scripts/smoke-docker.sh <image>
#
# Example:
#   scripts/smoke-docker.sh ghcr.io/hanshuebner/herold:latest
#   scripts/smoke-docker.sh herold:smoke-local
#
# Exit code is zero on success and non-zero on any step failure. On
# failure the container's logs are dumped before the script exits so
# CI logs surface the actual breakage.
#
# Requirements on the host:
#   - docker
#   - python3 (used for free-port allocation and IMAP verification)
#   - bash (uses /dev/tcp for readiness probes)
#
# What the script does, mirroring the README quickstart:
#   1. Pick five free host ports (SMTP / submission / IMAP / public
#      HTTP / admin HTTP).
#   2. docker run the image with those ports mapped to the container's
#      1025 / 1587 / 1143 / 8080 / 9443.
#   3. Wait for the admin and SMTP listeners to accept TCP.
#   4. docker exec the bootstrap to create admin@example.local.
#   5. docker exec to add the example.local domain.
#   6. Send a uniquely-marked test message via the loopback SMTP relay.
#   7. Verify via plain IMAP that the message landed in INBOX.
#
# The baked-in quickstart system.toml runs every listener plaintext on
# loopback (ADR-0001 / issue #109): no self-signed certificate is shipped
# in the image, so neither implicit-TLS IMAPS nor STARTTLS is offered.
#
# On any failure the container is removed via an EXIT trap so the
# script is safe to retry without manual cleanup.

set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: $0 <image>" >&2
  exit 2
fi
IMAGE="$1"

NAME="herold-smoke-$$-${RANDOM}"
PASSWORD="smoke-pass-${RANDOM}..."
MARKER="smoke-marker-${RANDOM}-${RANDOM}"

# Pick free host ports. Asking the kernel for an unused port via
# bind(0) then closing the socket is racy in theory, but on a CI
# runner with no other listener about to grab the same port it is
# good enough and lets parallel smoke tests run without colliding.
free_port() {
  python3 -c '
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
'
}
PORT_SMTP=$(free_port)
PORT_SUBMISSION=$(free_port)
PORT_IMAP=$(free_port)
PORT_PUBLIC=$(free_port)
PORT_ADMIN=$(free_port)

cleanup() {
  local rc=$?
  if [ $rc -ne 0 ]; then
    echo "smoke: FAIL (rc=$rc) - container logs:" >&2
    docker logs "$NAME" 2>&1 | tail -200 >&2 || true
  fi
  docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "smoke: image=${IMAGE} container=${NAME}"
echo "smoke: ports SMTP=${PORT_SMTP} submission=${PORT_SUBMISSION} IMAP=${PORT_IMAP} public=${PORT_PUBLIC} admin=${PORT_ADMIN}"

docker run -d --name "$NAME" \
  -p "127.0.0.1:${PORT_SMTP}:1025" \
  -p "127.0.0.1:${PORT_SUBMISSION}:1587" \
  -p "127.0.0.1:${PORT_IMAP}:1143" \
  -p "127.0.0.1:${PORT_PUBLIC}:8080" \
  -p "127.0.0.1:${PORT_ADMIN}:9443" \
  "$IMAGE" >/dev/null

# Wait for the server to be fully ready inside the container, not just
# for Docker's userspace port proxy to accept connections. The proxy
# accepts the moment `-p` is wired up, BEFORE the server has finished
# applying migrations or binding listeners. If the smoke script
# proceeds to `docker exec ... bootstrap` at that point the bootstrap
# subprocess opens the same SQLite DB concurrently with the server's
# in-progress migration application -- the migration runner reads
# schema_migrations and the live ALTER TABLE state in two separate
# operations, so it races and hits "duplicate column" errors.
#
# `herold: ready` is the server's last startup log line, emitted after
# all listeners are bound (see internal/admin/server.go). Polling
# `docker logs` for that string is unambiguous: once present, every
# subsequent CLI invocation can open the store without racing.
wait_ready() {
  local max="$1"
  local i
  for i in $(seq 1 "$max"); do
    if docker logs "$NAME" 2>&1 | grep -q '"msg":"herold: ready"'; then
      echo "smoke: server reports ready after ${i}s"
      return 0
    fi
    if ! docker ps --filter "name=^${NAME}\$" --format '{{.Names}}' | grep -q "^${NAME}\$"; then
      echo "smoke: container exited during startup" >&2
      return 1
    fi
    sleep 1
  done
  echo "smoke: timeout waiting for 'herold: ready' log line" >&2
  echo "smoke: last container logs:" >&2
  docker logs "$NAME" 2>&1 | tail -30 >&2
  return 1
}

wait_ready 90

echo "smoke: bootstrapping admin principal..."
docker exec "$NAME" /usr/local/bin/herold bootstrap \
  --email admin@example.local --password "$PASSWORD"

echo "smoke: ensuring domain example.local is registered..."
if ! docker exec "$NAME" /usr/local/bin/herold domain add example.local 2>&1; then
  # Bootstrap auto-creates the domain from the admin email
  # (admin@example.local -> example.local) on newer builds, so a
  # 409 here is expected. Verify via domain list before continuing.
  if docker exec "$NAME" /usr/local/bin/herold domain list 2>/dev/null | grep -q '"name": *"example\.local"'; then
    echo "smoke: domain example.local already present (likely auto-created by bootstrap)"
  else
    echo "smoke: domain add failed AND example.local not listed" >&2
    exit 1
  fi
fi

echo "smoke: sending test message via SMTP relay..."
{
  printf 'EHLO smoke-test\r\n'
  printf 'MAIL FROM:<admin@example.local>\r\n'
  printf 'RCPT TO:<admin@example.local>\r\n'
  printf 'DATA\r\n'
  printf 'From: Smoke Test <admin@example.local>\r\n'
  printf 'To: admin@example.local\r\n'
  printf 'Subject: %s\r\n' "$MARKER"
  printf 'Date: %s\r\n' "$(date -u +'%a, %d %b %Y %H:%M:%S +0000')"
  printf '\r\n'
  printf 'Hello from the herold Docker smoke test.\r\n'
  printf '.\r\n'
  printf 'QUIT\r\n'
} | python3 -c '
import socket, sys
s = socket.create_connection(("127.0.0.1", '"$PORT_SMTP"'), timeout=10)
s.settimeout(5)
data = sys.stdin.buffer.read()
# Read the banner, then drive line by line so we can fail loudly if
# any 2xx is missing. Simpler: just send everything and read until
# the server closes after QUIT.
s.sendall(data)
buf = b""
try:
    while True:
        chunk = s.recv(4096)
        if not chunk:
            break
        buf += chunk
except socket.timeout:
    pass
s.close()
text = buf.decode("ascii", "replace")
for line in text.splitlines():
    if not line:
        continue
    if not (line[:3].isdigit() and line[0] in "23"):
        print("smoke: SMTP response not 2xx/3xx:", line, file=sys.stderr)
        sys.exit(1)
print("smoke: SMTP transaction OK")
'

echo "smoke: verifying receipt via IMAP on port ${PORT_IMAP}..."
PORT_IMAP_ENV="$PORT_IMAP" PASSWORD_ENV="$PASSWORD" MARKER_ENV="$MARKER" python3 - <<'PYEOF'
import imaplib
import os
import sys
import time

port = int(os.environ["PORT_IMAP_ENV"])
password = os.environ["PASSWORD_ENV"]
marker = os.environ["MARKER_ENV"]

last_err = None
for attempt in range(30):
    try:
        m = imaplib.IMAP4("127.0.0.1", port)
        try:
            m.login("admin@example.local", password)
            m.select("INBOX")
            typ, data = m.search(None, '(SUBJECT "%s")' % marker)
            if typ != "OK":
                raise RuntimeError("IMAP SEARCH typ=%s" % typ)
            ids = data[0].split()
            if ids:
                print("smoke: IMAP message found, count=%d" % len(ids))
                m.logout()
                sys.exit(0)
        finally:
            try:
                m.logout()
            except Exception:
                pass
    except Exception as exc:
        last_err = exc
    time.sleep(1)

print("smoke: IMAP message NOT found after retries (last error: %r)" % last_err, file=sys.stderr)
sys.exit(1)
PYEOF

echo "smoke: PASS"
