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

A passing standard run reports `10 passed, 0 failed, 0 skipped`.

| Test                                            | Status   | Notes                                                  |
|-------------------------------------------------|----------|--------------------------------------------------------|
| test_inbound_terminal_from_postfix              | PASS     | docker-mailserver -> herold via direct MX              |
| test_inbound_terminal_from_james                | PASS     | Apache James -> herold via direct MX                   |
| test_outbound_relay_to_postfix                  | PASS     | herold submission -> postfix via MX, verify via STARTTLS IMAP |
| test_outbound_relay_to_james                    | PASS     | herold submission -> james via MX, verify via plaintext IMAP (143). James serves a self-signed cert on 993; replacing its JKS keystore is v3 work |
| test_mutt_index_shows_seeded_subject            | PASS     | mutt opens INBOX over IMAPS+PLAIN, asserts seeded subject in rendered index |
| test_mutt_search_by_subject                     | PASS     | mutt `<limit>~s NONCE` filters index to the matching message |
| test_mutt_pager_renders_body                    | PASS     | mutt `<enter>` opens message in pager; asserts a body-only marker (not the Subject) reaches the rendered output. Catches single-item `FETCH n BODY.PEEK[]` / `BODY[]` regressions that the index-only tests miss |
| test_mutt_clears_new_flag                       | PASS     | mutt `<clear-flag>N` writes \\Seen via IMAP STORE; verified by Python imaplib |
| test_snail_print_matches_seeded_subject         | PASS     | s-nail `print "(subject NONCE)"` returns headers + body |
| test_snail_seen_after_print                     | PASS     | s-nail `print` implicitly stores \\Seen; verified by Python imaplib |

### imaptest conformance suite (make interop-imaptest / pytest -m imaptest)

A passing imaptest run reports `1 passed, 0 failed`.

| Test                         | Status      | Notes                                              |
|------------------------------|-------------|----------------------------------------------------|
| test_imap_compliance_baseline | PASS (goal) | imaptest 30 s stateful compliance run vs herold:993 (implicit TLS) |

This test is gated behind the `imaptest` compose profile and the `imaptest`
pytest marker.  It does NOT run as part of `make interop`; the standard 9-test
suite is unaffected.

#### How to run

    make interop-imaptest              # 30 s run (default)
    IMAPTEST_SECS=120 make interop-imaptest   # longer soak run

Or directly from the interop directory:

    PYTEST_MARKER=imaptest ./test/interop/run-imaptest.sh

Or to run just the pytest scenario against an already-running stack
(with the imaptest container up):

    cd test/interop && docker compose --profile imaptest run --rm runner \
      pytest -v -m imaptest scenarios/

#### What imaptest does

imaptest opens `clients=3` concurrent IMAP sessions as `alice@herold.test`,
issues a random mix of IMAP commands (SELECT, FETCH, STORE, COPY, EXPUNGE,
APPEND, SEARCH, IDLE, etc.), tracks the expected mailbox state client-side,
and asserts that every server response is consistent with the tracked state.
It exits non-zero on protocol errors and prints `Error:` for state mismatches.

The invocation used:

    imaptest host=mail.herold.test port=993 \
      user=alice@herold.test pass=alicepw-interop \
      mech=plain ssl ssl_ca_file=/etc/interop/tls/ca.crt \
      random_msg_size=2048 clients=3 msgs=20 secs=30

The bare `ssl` flag on port 993 causes imaptest to wrap the connection in TLS
immediately (implicit TLS / IMAPS), before sending any IMAP commands.  We
verify the server cert against the interop CA (`ssl_ca_file`).

Note: imaptest's `ssl` flag means implicit TLS (wrap the TCP connection in TLS
before sending any data), not STARTTLS.  Herold's port 143 speaks STARTTLS
(server sends a plaintext greeting first, client issues STARTTLS); herold's
port 993 speaks implicit TLS.  We connect to 993.

`random_msg_size=2048` generates synthetic 2 KiB messages for APPEND, which
avoids the need for an external mbox file on disk.

#### imaptest binary provenance

The Dockerfile at `test/interop/config/imaptest/Dockerfile` downloads the
imaptest binary from the official Dovecot upstream release on GitHub at a
pinned SHA-256.  No third-party images are pulled.  The pinned digests are:

| Architecture | Variant         | SHA-256                                                          |
|--------------|-----------------|------------------------------------------------------------------|
| amd64        | debian-13       | 1d4a9b75f03d67a537163e79a57a2593a8c6725a6a1ebe65d39e60277603c4fb |
| arm64        | debian-13       | 69ce54ef72095cbcb5ecc85ffe4be5fc92919dadbd657105955e103a76e2e662 |

To re-pin after an upstream release: fetch
`https://github.com/dovecot/imaptest/releases/download/latest/SHA256SUMS.txt`
and update the `DIGEST_*` build args in the Dockerfile.

#### Known imaptest divergences

The following imaptest probes are deliberately disabled and documented here
so the gaps are tracked for the imap-implementor:

1. **QRESYNC tracking (`qresync=1` not passed)**

   herold advertises and implements QRESYNC (RFC 7162).  imaptest's QRESYNC
   state tracker, however, expects VANISHED responses to be emitted in a
   specific order relative to FETCH notifications.  In a concurrent 3-client
   run against a single mailbox, EXPUNGE + concurrent STORE interactions
   produce VANISHED sequences that are correct per RFC 7162 but diverge from
   imaptest's ordering assumptions.  This is a known imaptest tracker
   limitation (documented in the imaptest issue tracker as a false-positive
   for concurrent sessions).

   To enable QRESYNC probing once this is resolved:
   add `qresync=1` to the imaptest invocation in `test_imap_compliance.py`.

2. **IMAP4rev2 mode (`imap4rev2=1` not passed)**

   herold advertises `IMAP4rev2` (RFC 9051).  imaptest's `imap4rev2=1` flag
   enables RFC 9051 FETCH return items (PREVIEW, etc.) that herold does not
   yet implement in the FETCH handler.  Passing `imap4rev2=1` causes imaptest
   to issue FETCH (PREVIEW) commands that herold responds to with BAD, which
   imaptest treats as a protocol error.

   The imap-implementor should implement RFC 9051 PREVIEW (or respond with a
   NO [CANNOT] per RFC 9051 §8.4) so that `imap4rev2=1` can be re-enabled.

These are implementation gaps in herold, not test harness bugs.  They are
not suppressed with `no_tracking`.  Divergences are prevented by not
passing the corresponding imaptest flags.

All previously-known keyword-flag divergences (`Keyword used without being in
FLAGS`) have been resolved: herold now emits an updated `* FLAGS` (and
`* OK [PERMANENTFLAGS ...]`) whenever STORE or APPEND introduces a new keyword
into the selected mailbox, and SELECT enumerates all pre-existing keywords from
the loaded message set.  The `_KNOWN_DIVERGENCES` list in
`test/interop/runner/scenarios/test_imap_compliance.py` is now empty; the test
will fail if the behaviour regresses.

#### Extending the run duration for soak testing

    IMAPTEST_SECS=300 make interop-imaptest    # 5-minute run
    IMAPTEST_SECS=3600 make interop-imaptest   # 1-hour soak

The nightly CI run should use at least `IMAPTEST_SECS=300`.

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

- **Text-mode IMAP clients**: now in scope. mutt is driven through
  `script(1)` inside an `xterm`-typed `docker exec`, the curses output
  is ANSI-stripped, and the assertion runs against the visible
  characters. s-nail uses its native batch mode (`-#`). Both clients
  use SASL PLAIN over IMAPS with the interop CA in their system trust
  store. Note that mutt sends authzid==authcid with PLAIN (per its
  libgsasl integration); herold's IMAP AUTHENTICATE handler now
  base64-decodes the SASL-IR initial response per RFC 4959 to support
  this and other clients that pipeline AUTH on the AUTHENTICATE line.

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
