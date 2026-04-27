# 02 — Phasing

*(Revised 2026-04-24 for: Go language, SQLite+Postgres, large mailboxes, HTTP send API + webhooks, events + NATS plugin, external OIDC federation. Phase 4 deleted (no multi-node).)*

How we get from zero to a shippable v1, and what comes after. Each phase has explicit entry criteria, deliverables, exit criteria.

## Phase 0: Foundations

**Goal:** project skeleton, CI, and the primitives everything else builds on.

**Deliverables:**
- Go module skeleton per `01-tech-stack.md`.
- CI (GitHub Actions): build, test, `govulncheck`, `staticcheck`, `gofmt`, SBOM on release.
- `store` package: metadata + blob + FTS interfaces, **both SQLite and Postgres metadata backends implemented**, Bleve FTS, filesystem blob store. Both metadata backends in CI from day one.
- Observability wiring: `log/slog` structured logs + `prometheus/client_golang` exporter + `go.opentelemetry.io/otel` OTLP (optional).
- `sysconfig` + `appconfig` split per REQ-OPS-01..25.
- Integration test harness: spins up a test server with tempdir, scripted clients.
- MIME parser baseline (`enmime` initially; fork if we hit correctness gaps).
- Plugin SDK (Go): helper package for writing plugins; JSON-RPC stdio; used by all first-party plugins.

**Exit criteria:**
- `go test ./...` passes metadata + blob round-trip on both SQLite and Postgres.
- Server binary starts, binds nothing, idles.
- System config parser rejects typos.
- Plugin SDK demo (`herold-echo-plugin`) works end-to-end.

Estimated effort: ~5 person-weeks.

## Phase 1: Inbound email works

**Goal:** receive mail on port 25, classify, Sieve-route, deliver. Read it with IMAP. No outbound.

**Deliverables:**
- `protosmtp` relay-in: state machine through MAIL/RCPT/DATA/BDAT, SIZE, STARTTLS, 8BITMIME, SMTPUTF8, PIPELINING, REQUIRETLS.
- `maildkim` verify, `mailspf` verify, `maildmarc` evaluate, `mailarc` verify (`emersion/go-msgauth` evaluated; fork if needed).
- **LLM spam classifier plugin** (first-party) — default endpoint `http://localhost:11434/v1`. `spam` package in server invokes it.
- `sieve`: parser + interpreter with core + `fileinto`, `imap4flags`, `copy`, `body`, `vacation`, `envelope`, `variables`, `mailbox`, `mailboxid`, `spamtestplus`. Sandbox.
- Delivery path: accept → classify → Sieve → deliver to mailbox + state-change feed.
- `protoimap`: IMAP4rev1/rev2 core: LOGIN, LIST, LSUB, SELECT/EXAMINE, FETCH (envelope/body/flags/UID), STORE, APPEND, EXPUNGE, IDLE, UIDPLUS, ESEARCH, SEARCH (basics).
- FTS indexing worker (async, Bleve): body + common attachment text (PDF, DOCX, XLSX, plain, HTML).
- `directory` internal backend: CRUD, password (Argon2id), TOTP enrollment, alias resolution.
- **Per-user external OIDC federation**: `directoryoidc` RP-only; link/unlink flow on admin API.
- `protoadmin`: health, config-check, principals CRUD, domains CRUD, bootstrap, OIDC provider CRUD, principal-OIDC-link CRUD.
- CLI: `herold bootstrap`, `principal create/delete/list`, `domain add/remove` (no auto-DNS yet), `oidc provider add`, `server status`.
- TLS: file-based certs loaded at startup. No ACME yet.
- **Download rate limits** on IMAP FETCH (REQ-STORE-20..25).

**Exit criteria:**
- Send mail from an external server → message appears in local Inbox via IMAP.
- Thunderbird connects, sees Inbox with 1 TB of synthetic mail, FETCH and SEARCH perform under target budgets.
- LLM classifier runs against local Ollama; verdict visible in message headers.
- Sieve routes correctly; spam→Junk by default.
- Per-user external OIDC link/unlink works (test against a local Dex or similar).
- `kill -9`; restart; no data loss.
- Both SQLite and Postgres backends pass the same integration test suite.

Estimated effort: ~14 person-weeks.

## Phase 2: Outbound + ACME + auto-DNS + JMAP + HTTP send API + events

**Goal:** two-way mail. Modern client (JMAP). Automatic TLS + DNS. HTTP-based integrations.

**Deliverables:**
- `queue`: persistent table, scheduler, delivery workers, retries, DSN.
- SMTP outbound: MX resolution, STARTTLS, DKIM signing, MTA-STS, DANE.
- DKIM key management.
- `protosmtp-submission`: 587 STARTTLS + 465 implicit with SASL AUTH (password, OAUTHBEARER/XOAUTH2 via external OIDC tokens).
- `acme`: ACME client with HTTP-01, TLS-ALPN-01, DNS-01 (via DNS plugin).
- **DNS plugins**: Cloudflare + Route53 + Hetzner Cloud DNS + manual (first-party). Cert store ACME-integrated.
- **`autodns` publisher**: on `domain add`, publishes DKIM/MTA-STS/TLSRPT/DMARC via the configured DNS plugin.
- `protojmap`: Core + Mail. `Mailbox`, `Email`, `EmailSubmission`, `Identity`, `Thread`, `SearchSnippet`, `VacationResponse`. EventSource push. Upload/download.
- CONDSTORE/QRESYNC in IMAP.
- IMAP extended: MOVE, LIST-EXTENDED, LIST-STATUS, SPECIAL-USE, MULTIAPPEND, COMPRESS=DEFLATE, UTF8=ACCEPT.
- **IMAP NOTIFY (RFC 5465)** — REQ-PROTO-34. Shares the per-principal change feed with IDLE and JMAP push; one event source, three consumers.
- **JMAP snooze** — REQ-PROTO-49. `$snoozed` keyword + `snoozedUntil` extension property on `Email`, with server-side wake-up sweeper. Migration adds `snoozed_until_us` column to the messages table on both backends; one new typed `store.Metadata` method (`ListDueSnoozedMessages(ctx, now, max)`) plus a tick worker in StartServer (60 s default cadence) that clears the keyword + nulls the column atomically and appends a `state_changes` row so push consumers wake. Move-on-snooze is off by default; reserve the `\Snoozed` mailbox role for Phase-3 promotion. IMAP SNOOZE extension (draft-ietf-extra-imap-snooze) is Phase-3 — JMAP covers the clients that matter today.
- `protomanagesieve` listener.
- **`protosend` HTTP send API** (REQ-SEND): send, send-raw, send-batch, quota, stats, idempotency.
- **`protowebhook` mail-arrival webhooks** (REQ-HOOK): CRUD, delivery with inline/fetch-URL bodies, retry, HMAC signature.
- **`protoevents` event dispatcher + NATS event-publisher plugin** (first-party, REQ-EVT): events fire from mail flow, auth, queue, ACME, DKIM rotation.
- **Shared mailboxes + IMAP ACL (RFC 4314)**: mailbox-ACL schema, fanout in SELECT/STATUS, `SETACL` / `GETACL` / `MYRIGHTS` / `LISTRIGHTS`. JMAP sharing surface aligned.
- **Minimal web UI** (REQ-ADM-200 scope): dashboard, principal CRUD + password/2FA/app-passwords, domain + alias CRUD, queue monitor (list/show/retry/hold/delete), email research (search by sender/recipient/date with status — sent/delivered/deferred/bounced). HTMX + Go templates unless overridden. Not a full SPA.
- DMARC report ingestion + aggregation.
- TLS-RPT emission.
- Backup/restore; **SQLite ↔ Postgres migration tool**.
- CLI extended: queue, cert, spam, hook, api-key, app-config dump/load.

**Exit criteria:**
- Send mail *from* the server to Gmail, arrives, passes DMARC.
- JMAP client (reference) logs in, lists mail, threads.
- ACME provisions Let's Encrypt cert with **zero operator DNS touching** (via Cloudflare plugin).
- HTTP send API: `curl -X POST .../api/v1/mail/send` delivers mail. Idempotency key works.
- Webhook: mail arrives → registered URL receives POST with inline body. Fetch-URL mode fetches successfully.
- NATS plugin publishes events to a running NATS server; `herold.mail.received.*` visible via `nats sub`.
- External OIDC: user signs in to JMAP via Google/GitHub; linked to local principal.
- 1M message delivery + receive stress test: no leaks, queue holds.
- Backup/restore cycle works across SQLite↔Postgres.
- Shared `support@` mailbox: two principals access it via IMAP ACL. ACL mutations take effect within seconds.
- Web UI: a fresh operator can provision one domain + one user + observe an inbound delivery + find a bounced message, entirely in the browser.

Estimated effort: ~28 person-weeks.

## Phase 2.5: Hardening and polish (pre-v1.0)

**Goal:** shippable. Not new features; quality.

**Deliverables:**
- Full conformance test suites passing.
- Fuzz testing (Go 1.18+ native fuzzing) on all wire parsers. Week-long campaign pre-release.
- Load testing at REQ-NFR-01 targets (100 msg/s sustained, 1k concurrent IDLE).
- 1 TB mailbox benchmark: import, browse, search; latency profile documented.
- Documentation: operator manual, admin reference, config reference, DNS setup guide, plugin developer guide, migration guide (SQLite↔Postgres), troubleshooting, SES porting guide.
- Packaging: `.deb`, `.rpm`, Docker image, K8s example manifests.
- Plugin SDK documented + examples (simple DNS provider + minimal webhook event-publisher + custom spam classifier).
- Performance characterization: pprof, tuning guide.
- Security review (external or community).

**Exit criteria:**
- Fresh operator bootstraps working server in 30 minutes from install guide.
- Fuzz finds no new crashes in a week.
- Load test meets REQ-NFR-01.
- 1 TB mailbox stress passes.

Estimated effort: ~10 person-weeks.

**Ship v1.0 here.**

## Phase 3: Pick a subset

Feature candidates post-v1.0; pick based on demand:

- Expanded Web UI (full user self-service panel: vacation, Sieve editor, identities, app passwords, OIDC links): ~6 weeks.
- WebAuthn for admin UI: ~2 weeks.
- Web Push (VAPID) for JMAP: ~3 weeks.
- Maildir/mbox/IMAP importer: ~3 weeks.
- Attachment OCR (bound by Tesseract sidecar): ~4 weeks.
- Additional community DNS plugins (DigitalOcean, Gandi, etc.): ~1–2 weeks each (or community-authored).
- SCIM 2.0 for directory provisioning: ~6 weeks.
- Acme-dns generic DNS-01 plugin: ~1 week.

## Cross-cutting (continuous)

- **Conformance**: IMAP (imaptest), SMTP (interop vs Postfix/Exim), JMAP (Fastmail test corpus), Sieve (Pigeonhole), DKIM/DMARC test vectors.
- **Fuzzing**: parser targets on every PR (short); nightly long; weekly pre-release.
- **Security review**: every feature parsing untrusted input documented with review note.
- **Release cadence** (post-v1): patch as needed; minor monthly; major for breaking changes.

## Team size assumptions

Calendar estimates assume **1 engineer at 100%**. Total to v1.0 by these estimates: ~60 person-weeks ≈ 14 calendar months solo. Proportionally less with help. This is substantial work and the estimates are generous but not fluffy.
