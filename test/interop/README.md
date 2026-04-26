# Herold interop test suite

End-to-end interoperability tests for herold against real third-party MTAs
running in Docker Compose.  All tests are deterministic and use a private CA
for TLS verification.

## Quick start

    cd test/interop
    ./run.sh            # standard suite (no bulk)

Or from the repo root:

    make interop

## Prerequisites

- Docker + Docker Compose v2
- Linux/amd64 or macOS/arm64 (James container forces linux/amd64 via platform pin)
- Outbound internet access for image pulls on first run

## Architecture

    certgen (alpine:3.20, init)
      |-- writes: /tls-data/ca.crt, /tls-data/ca.key
      |-- writes: /tls-data/{herold,postfix,james}.{crt,key}
      |-- writes: /tls-data/james.p12  (PKCS12 for JVM keystore import)
      v
    coredns (CoreDNS 1.11.3) -- authoritative for *.test zones
      v
    herold (built from repo)  -- system under test
    postfix (docker-mailserver 14.0) -- third-party MTA #1
    james (apache/james)      -- third-party MTA #2
    mutt-client (debian:bookworm-slim + mutt)
    snail-client (debian:bookworm-slim + s-nail)
      v
    runner (python:3.12-slim + pytest)

Stalwart was an earlier candidate but proved infeasible to bootstrap
hands-free in this matrix and was removed (see git history). Two
third-party MTAs is the supported set.

## CA layout (task #2)

The `certgen` init service runs `config/certgen/generate.sh` using
`openssl` from `alpine:3.20`.  All certificates are written to the
`tls-data` Docker volume which is then mounted read-only into every MTA
container.

| File                | Description                                         |
|---------------------|-----------------------------------------------------|
| `tls-data/ca.crt`   | Root CA certificate (RSA-4096, 10-year validity)    |
| `tls-data/ca.key`   | Root CA private key (never leaves tls-data volume)  |
| `tls-data/herold.crt` / `.key`   | Leaf cert for herold (SANs: mail.herold.test, herold, herold.interop, 10.77.0.10) |
| `tls-data/postfix.crt` / `.key`  | Leaf cert for docker-mailserver (SANs: mail.postfix.test, postfix, 10.77.0.12) |
| `tls-data/james.crt` / `.key`    | Leaf cert for Apache James (SANs: mail.james.test, james, 10.77.0.13) |
| `tls-data/james.p12`             | PKCS12 bundle (for James JKS import; pass: changeit) |

To inspect a cert from outside the suite:

    docker run --rm -v interop_tls-data:/tls alpine:3.20 \
      openssl x509 -in /tls/herold.crt -text -noout

### TLS knobs per MTA

| MTA           | TLS mode                  | Knob / mechanism                                     |
|---------------|---------------------------|------------------------------------------------------|
| herold        | STARTTLS on 25/587/143; implicit on 993 | `cert_file`/`key_file` per `[[listener]]` in system.toml |
| docker-mailserver | STARTTLS (Postfix+Dovecot) | `SSL_TYPE=manual`, `SSL_CERT_PATH`, `SSL_KEY_PATH` env vars |
| Apache James  | self-signed only (v2) | JKS keystore swap deferred to v3; runner connects plaintext on 143 within the private network |

The CA root is installed into every container's system trust store:
- Debian-based (herold, mutt-client, snail-client, runner): `cp ca.crt /usr/local/share/ca-certificates/ && update-ca-certificates`
- James: `keytool -importcert` into the JVM cacerts store (for outbound trust)

## Accounts

| Account                  | Password             | MTA             |
|--------------------------|----------------------|-----------------|
| admin@herold.test        | adminpw-interop      | herold (admin)  |
| alice@herold.test        | alicepw-interop      | herold          |
| carol@postfix.test       | testpw-carol1        | docker-mailserver |
| dave@james.test          | testpw-dave1         | Apache James    |

## Test scenarios

### Standard suite (make interop)

A passing standard run reports `4 passed, deselected/skipped, 0 failures`.

| Test                                            | Status   | Notes                                                  |
|-------------------------------------------------|----------|--------------------------------------------------------|
| test_inbound_terminal_from_postfix              | PASS     | docker-mailserver -> herold via direct MX              |
| test_inbound_terminal_from_james                | PASS     | Apache James -> herold via direct MX                   |
| test_outbound_relay_to_postfix                  | PASS     | herold submission -> postfix via MX, verify via STARTTLS IMAP |
| test_outbound_relay_to_james                    | PASS     | herold submission -> james via MX, verify via plaintext IMAP (143). James serves a self-signed cert on 993; replacing its JKS keystore is v3 work |
| scenarios/test_imap_clients.py (multiple)       | skipped  | text-mode IMAP-client coverage deferred to v3 (mutt batch-mode + curses + non-interactive PTY interaction is finicky and slow) |

### Bulk suite (make interop-bulk / pytest -m bulk)

A passing bulk run reports `1 passed, 1 skipped, 16 deselected`.

| Test                  | Status  | Description                                                       |
|-----------------------|---------|-------------------------------------------------------------------|
| test_bulk_inbound     | PASS    | Runner sends BULK_N (default 500) messages directly to herold:25 in one SMTP session; IMAP STATUS + SEARCH verify the count; Prometheus deltas asserted |
| test_bulk_mixed       | skipped | Two-thread inbound+outbound; v3                                   |

Note: bulk_inbound deliberately bypasses Postfix-as-sender. Postfix's default `smtp_destination_concurrency_limit=20` opens concurrent deliveries to herold, which exceeds herold's `MaxConcurrentPerIP=16` (`internal/protosmtp/server.go`), so a chunk of every batch gets refused with "smtp connection refused (per-IP cap)". A single sequential SMTP session models herold's bulk-receive path cleanly.

## Tuning bulk tests

    BULK_N=200 make interop-bulk

The default is 500.  On a laptop a run of 500 messages typically completes in
under 60 seconds wall-clock for the inbound path.  If your Docker host is
resource-constrained set BULK_N=50.

## Known limitations and v3 follow-ups

- **Stalwart removed**: Stalwart 0.10.6 was an earlier candidate but
  could not be bootstrapped hands-free. Two issues blocked it: (1) its
  outbound STARTTLS handshake to herold failed because the Stalwart
  container's system trust store did not include the interop CA, and
  (2) Stalwart's IMAP listener returned `AUTHENTICATIONFAILED` for
  every LOGIN against principals created via the management API
  regardless of name vs email login form. Both required Stalwart-
  specific configuration that ate disproportionate time relative to
  the coverage gain. The suite now runs against docker-mailserver and
  Apache James only; that is enough divergent stack coverage (Postfix +
  Dovecot vs JVM mail server) for the interop signal we care about.

- **James IMAP keystore (deferred)**: James serves its built-in
  self-signed cert on STARTTLS (143) and IMAPS (993). The
  `config/james/bootstrap.sh` script is written to install the interop
  PKCS12 bundle into the JVM JKS keystore via `keytool` but is not
  wired into the running James container's startup; the JKS swap needs
  to happen before James opens its TLS listeners. v2 workaround: the
  outbound test connects to james:143 plaintext (acceptable on the
  private docker network).

- **Text-mode IMAP clients (deferred to v3)**: `test_imap_clients.py` is
  skipped at module level. mutt's batch mode + curses + non-interactive
  PTY interaction was finicky and slow (30s/test). The mutt-client and
  snail-client compose services and CA-trusting Dockerfiles remain in
  place for v3 pickup.

- **Bulk via postfix-as-sender**: not viable because postfix's default
  `smtp_destination_concurrency_limit=20` exceeds herold's
  `MaxConcurrentPerIP=16` (see `internal/protosmtp/server.go`). The
  bulk inbound test sends direct from the runner instead. Lowering the
  postfix concurrency would require docker-mailserver config overrides
  not yet wired.

- **James platform**: the apache/james image is `linux/amd64` only. On
  Apple Silicon it runs under Rosetta/QEMU; expect slower startup.

- **Postfix-accounts.cf hash regeneration**: the SHA512-CRYPT hash for
  `carol@postfix.test` was generated with:
      docker run --rm --entrypoint /bin/sh mailserver/docker-mailserver:14.0 \
        -c "doveadm pw -s SHA512-CRYPT -p testpw-carol1"
  Regenerate if docker-mailserver changes its expected format.

- **Docker Desktop network flakiness on macOS**: occasional
  "failed to set up container networking: Address already in use" on
  bring-up after a previous run. `run.sh` includes a `clean_compose_state`
  helper that retries the bring-up once on this specific error.

- **Prometheus metrics invariant** (test_bulk_inbound): if herold's
  metrics endpoint is unreachable (e.g. metrics_bind misconfigured), the
  metric assertion is skipped with a log warning rather than failing.

## Cleaning up

    make interop-clean
    # or:
    docker compose down --remove-orphans --volumes
