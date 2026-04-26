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
      |-- writes: /tls-data/{herold,stalwart,postfix,james}.{crt,key}
      |-- writes: /tls-data/james.p12  (PKCS12 for JVM keystore import)
      v
    coredns (CoreDNS 1.11.3) -- authoritative for *.test zones
      v
    herold (built from repo)  -- system under test
    stalwart (v0.10.6)        -- third-party MTA #1
    postfix (docker-mailserver 14.0) -- third-party MTA #2
    james (apache/james)      -- third-party MTA #3
    mutt-client (debian:bookworm-slim + mutt)
    snail-client (debian:bookworm-slim + s-nail)
      v
    runner (python:3.12-slim + pytest)

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
| `tls-data/stalwart.crt` / `.key` | Leaf cert for Stalwart (SANs: mail.stalwart.test, stalwart, 10.77.0.11) |
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
| Stalwart      | STARTTLS + implicit (993) | `[certificate."default"]` in config.toml with `%{file:...}%` interpolation |
| docker-mailserver | STARTTLS (Postfix+Dovecot) | `SSL_TYPE=manual`, `SSL_CERT_PATH`, `SSL_KEY_PATH` env vars |
| Apache James  | STARTTLS/implicit via JKS keystore | `bootstrap.sh` imports PKCS12 into `/root/conf/keystore` via keytool |

The CA root is installed into every container's system trust store:
- Debian-based (herold, mutt-client, snail-client, runner): `cp ca.crt /usr/local/share/ca-certificates/ && update-ca-certificates`
- Stalwart: relies on the TLS config; no system store update needed
- James: `keytool -importcert` into the JVM cacerts store

## Accounts

| Account                  | Password             | MTA             |
|--------------------------|----------------------|-----------------|
| admin@herold.test        | adminpw-interop      | herold (admin)  |
| alice@herold.test        | alicepw-interop      | herold          |
| bob@stalwart.test        | testpw-bob1          | Stalwart        |
| carol@postfix.test       | testpw-carol1        | docker-mailserver |
| dave@james.test          | testpw-dave1         | Apache James    |

## Test scenarios

### Standard suite (make interop)

A passing standard run reports `4 passed, 10 skipped, 3 deselected, 2 xfailed`.

| Test                                            | Status   | Notes                                                  |
|-------------------------------------------------|----------|--------------------------------------------------------|
| test_inbound_terminal_from_postfix              | PASS     | docker-mailserver -> herold via direct MX              |
| test_inbound_terminal_from_james                | PASS     | Apache James -> herold via direct MX                   |
| test_inbound_terminal_from_stalwart             | xfail    | Stalwart accepts herold's CA-signed cert via STARTTLS but the inbound delivery handshake fails on UnknownIssuer; v3 fix is to install the interop CA into Stalwart's container trust store |
| test_outbound_relay_to_postfix                  | PASS     | herold submission -> postfix via MX, verify via STARTTLS IMAP |
| test_outbound_relay_to_james                    | PASS     | herold submission -> james via MX, verify via plaintext IMAP (143). James serves a self-signed cert on 993; replacing its JKS keystore is v3 work |
| test_outbound_relay_to_stalwart                 | xfail    | herold submission lands in bob's mailbox (verified in stalwart.log: `Message ingested ham accountId=8`), but Stalwart 0.10.6 IMAP LOGIN fails for the principal regardless of name vs email login form; needs Stalwart-auth investigation |
| scenarios/test_imap_clients.py (10 tests)       | skipped  | text-mode IMAP-client coverage deferred to v3 (mutt batch-mode + curses + non-interactive PTY interaction is finicky and slow) |

### Bulk suite (make interop-bulk / pytest -m bulk)

A passing bulk run reports `1 passed, 2 skipped, 16 deselected`.

| Test                  | Status  | Description                                                       |
|-----------------------|---------|-------------------------------------------------------------------|
| test_bulk_inbound     | PASS    | Runner sends BULK_N (default 500) messages directly to herold:25 in one SMTP session; IMAP STATUS + SEARCH verify the count; Prometheus deltas asserted |
| test_bulk_outbound    | skipped | Inherits the Stalwart IMAP-auth issue above; deferred to v3       |
| test_bulk_mixed       | skipped | Two-thread inbound+outbound; v3                                   |

Note: bulk_inbound deliberately bypasses Postfix-as-sender. Postfix's default `smtp_destination_concurrency_limit=20` opens concurrent deliveries to herold, which exceeds herold's `MaxConcurrentPerIP=16` (`internal/protosmtp/server.go`), so a chunk of every batch gets refused with "smtp connection refused (per-IP cap)". A single sequential SMTP session models herold's bulk-receive path cleanly.

## Tuning bulk tests

    BULK_N=200 make interop-bulk

The default is 500.  On a laptop a run of 500 messages typically completes in
under 60 seconds wall-clock for the inbound path.  If your Docker host is
resource-constrained set BULK_N=50.

## Known limitations and v3 follow-ups

- **Stalwart inbound (xfail)**: when stalwart accepts an inbound message
  destined for bob and tries to deliver it onward via STARTTLS to its own
  MX (mail.stalwart.test), the handshake fails with `invalid peer
  certificate: UnknownIssuer`. The interop CA is installed at config
  level (Stalwart serves the CA-signed leaf cert) but the *system* trust
  store inside the Stalwart container does not include the CA. v3 fix:
  copy `ca.crt` into `/usr/local/share/ca-certificates/` and run
  `update-ca-certificates` in a stalwart entrypoint wrapper, similar to
  the `stalwart-config-init` sidecar pattern.

- **Stalwart outbound (xfail)**: the message reaches bob's mailbox
  (verified by `Message ingested ham accountId=8` in `stalwart.log`) but
  IMAP LOGIN to retrieve it returns `AUTHENTICATIONFAILED` regardless of
  whether the test logs in as `bob` or `bob@stalwart.test`. The
  principal is created with `enabledPermissions=["email-receive",
  "email-send", "authenticate", "imap-authenticate"]` and exists in the
  directory (`/api/principal` returns it), but Stalwart 0.10.6 still
  rejects LOGIN. Likely a Stalwart 0.10.6 directory/auth wiring quirk;
  needs investigation against newer Stalwart versions and the official
  example configs.

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
