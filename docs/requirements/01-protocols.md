# 01 — Protocols

Every wire protocol the server speaks, with the RFCs that define correctness. Anything not listed here is out of scope unless promoted by edit.

## In scope (v1)

| Area | Protocol | Core RFCs | Notes |
|---|---|---|---|
| MTA | SMTP (submission + relay) | 5321, 5322 | With ESMTP extensions below |
| Mailbox access | IMAP4rev2 | 9051 | rev2 preferred; rev1 (3501) as fallback for old clients |
| Modern mailbox access | JMAP Core + Mail | 8620, 8621 | Primary API for new clients |
| Managed Sieve | ManageSieve | 5804 | Script upload/validate from clients |
| Sieve | Sieve | 5228 + extensions (below) | Filter execution at delivery |

## Deferred / optional

| Protocol | RFC | Decision |
|---|---|---|
| POP3 | 1939, 2449, 5034 | Not in v1. Reconsider phase 3. |
| LMTP | 2033 | Not needed for a single-binary design; our delivery is in-process. Could be added as an ingress option later. |
| NNTP | 3977 | No. |
| Submission-over-HTTP (JMAP submission) | 8621 §7 | Yes — it's part of JMAP Mail. |

## SMTP (REQ-PROTO-SMTP)

### Listeners

- **REQ-PROTO-01** MUST listen on **25/tcp** (relay in), **465/tcp** (implicit TLS submission), **587/tcp** (STARTTLS submission). Each listener bindable independently.
- **REQ-PROTO-02** MUST support IPv4 and IPv6 on every listener.
- **REQ-PROTO-03** MUST support PROXY protocol v1 and v2 (opt-in per listener) for operation behind L4 load balancers.

### ESMTP extensions to support

| Extension | RFC | Direction |
|---|---|---|
| STARTTLS | 3207 | in + out |
| AUTH (SASL) | 4954 | submission in, relay out |
| SIZE | 1870 | in + out |
| PIPELINING | 2920 | in + out |
| 8BITMIME | 6152 | in + out |
| SMTPUTF8 | 6531 | in + out |
| CHUNKING / BDAT | 3030 | in + out |
| DSN (NOTIFY/RET/ENVID/ORCPT) | 3461 | in + out |
| ENHANCEDSTATUSCODES | 2034 | in + out |
| BURL | 4468 | deferred |
| MT-PRIORITY | 6710 | deferred |
| REQUIRETLS | 8689 | **yes** |
| Future/experimental (e.g. LIMITS, MAIL-ADDRESS-TAGS) | — | skip |

- **REQ-PROTO-04** MUST advertise only the extensions actually implemented. No stubs.
- **REQ-PROTO-05** MUST support SMTPUTF8 end-to-end (mailbox names, headers, envelope) per RFC 6531.
- **REQ-PROTO-06** MUST enforce max message size per listener (SIZE advertised value), rejecting RCPT with appropriate 552 before DATA when declared SIZE exceeds limit.
- **REQ-PROTO-07** MUST emit RFC 3461 DSN on delivery failure for relays that requested NOTIFY.
- **REQ-PROTO-08** MUST support both command-line and BDAT ingestion; DATA and BDAT paths share the same message parser.

### SASL mechanisms (for submission)

- **REQ-PROTO-09** MUST support `PLAIN` and `LOGIN` (over TLS only).
- **REQ-PROTO-10** MUST support `SCRAM-SHA-256` and `SCRAM-SHA-256-PLUS`.
- **REQ-PROTO-11** MUST support `OAUTHBEARER` (RFC 7628) and `XOAUTH2` (Google/Microsoft compat).
- **REQ-PROTO-12** MUST reject plain-text mechanisms outside of TLS (STARTTLS-accepted or implicit TLS).

### Rate limiting and abuse controls

- **REQ-PROTO-13** MUST support per-IP connection rate limiting, per-IP concurrent connection limits, per-session command rate limiting, per-account submission rate limits.
- **REQ-PROTO-14** MUST support greylisting (optional per-domain/per-IP) and tarpitting on abuse signals.
- **REQ-PROTO-15** MUST support RBL lookups at CONNECT time for relay-in; with configurable action (reject / defer / tag).

## IMAP (REQ-PROTO-IMAP)

### Listeners

- **REQ-PROTO-20** MUST listen on **993/tcp** (implicit TLS) and **143/tcp** (STARTTLS). 143 may be disabled by config.

### Capabilities to advertise and implement

Baseline (IMAP4rev2 / rev1 interop):
- `IMAP4rev2`, `IMAP4rev1`
- `STARTTLS`, `AUTH=…` (same mechanisms as SMTP submission)
- `IDLE` (2177)
- `LIST-EXTENDED`, `LIST-STATUS`, `SPECIAL-USE`, `CREATE-SPECIAL-USE` (5258, 5819, 6154)
- `ENABLE` (5161), `UTF8=ACCEPT` (6855), `LITERAL+` (7888)
- `UIDPLUS` (4315), `ESEARCH` (4731), `SEARCHRES` (5182), `SORT` (5256), `THREAD` (5256)
- `CONDSTORE` (7162), `QRESYNC` (7162)
- `MOVE` (6851), `UNSELECT` (3691), `NAMESPACE` (2342)
- `ACL` (4314) — for shared mailboxes (phase 2; see REQ-PROTO-33)
- `QUOTA`, `QUOTA=RES-STORAGE` (9208)
- `BINARY` (3516), `CATENATE` (4469), `MULTIAPPEND` (3502)
- `COMPRESS=DEFLATE` (4978)
- `ID` (2971) — advertise with server name/version
- `METADATA` (5464), `METADATA-SERVER` — for JMAP interop
- `OBJECTID` (8474) — threading / JMAP interop
- `WEBPUSH` (not standardized yet) — skip
- `NOTIFY` (5465)

Deferred: `LIST-MYRIGHTS`, `CONTEXT=SEARCH`, `URLAUTH`.

- **REQ-PROTO-30** MUST pass the `imapserver-tests` public interop matrix for all listed capabilities.
- **REQ-PROTO-31** MUST handle `IDLE` for ≥2,000 concurrent sessions without per-session threads.
- **REQ-PROTO-32** MUST implement `CONDSTORE`/`QRESYNC` correctly — this is a hard requirement for modern clients (Apple Mail) and tricky to get right. See architecture/05-sync-and-state.md.
- **REQ-PROTO-33** Shared mailboxes and IMAP `ACL` (RFC 4314: `SETACL` / `GETACL` / `MYRIGHTS` / `LISTRIGHTS`) are **in scope for phase 2** (pre-v1.0). Schema carries per-mailbox ACL entries; SELECT / STATUS / fanout respect them; JMAP sharing surface aligned.
- **REQ-PROTO-34** MUST implement IMAP `NOTIFY` (RFC 5465). Clients subscribe to state-change events across mailboxes without holding a SELECT + IDLE per mailbox, which is how modern clients stay efficient across large folder sets. NOTIFY draws from the same per-principal change feed that backs IDLE and JMAP push — one event source, three consumers. Phase 2 alongside CONDSTORE/QRESYNC and MOVE.

## JMAP (REQ-PROTO-JMAP)

- **REQ-PROTO-40** MUST implement JMAP Core (RFC 8620): session, request/response envelope, error model, push.
- **REQ-PROTO-41** MUST implement JMAP Mail (RFC 8621): `Mailbox`, `Email`, `EmailSubmission`, `Identity`, `Thread`, `SearchSnippet`, `VacationResponse`. Upload/download endpoints.
- **REQ-PROTO-42** MUST implement `EmailSubmission` tied to the outbound SMTP queue (not a parallel submission path).
- **REQ-PROTO-43** MUST serve the session endpoint at `/.well-known/jmap` per RFC 8620.
- **REQ-PROTO-44** MUST support push via EventSource (SSE) at minimum; WebSocket push per RFC 8887 is optional.
- **REQ-PROTO-45** MUST NOT require a separate authentication subsystem — JMAP auth shares the identity model with IMAP/SMTP.
- **REQ-PROTO-46** `VacationResponse` object maps to a Sieve vacation rule (REQ-FILT-sieve).
- **REQ-PROTO-47** `Email/query` + `Email/get` MUST back onto the same FTS index used for IMAP `SEARCH`. One index, two query paths.
- **REQ-PROTO-48** `pushSubscription` deferred to phase 3 (Web Push with VAPID is complex; SSE covers clients that matter).

JMAP Calendars/Contacts/Tasks: out of scope. Groupware dropped entirely (scope non-goal NG3).

## ManageSieve (REQ-PROTO-MGSV)

- **REQ-PROTO-50** MUST listen on **4190/tcp** (STARTTLS) with the capabilities from RFC 5804.
- **REQ-PROTO-51** MUST validate scripts on upload using the same Sieve parser used at delivery — no divergence between "accepted" and "runnable".
- **REQ-PROTO-52** MUST support HAVESPACE, CHECKSCRIPT, and GETSCRIPT/PUTSCRIPT/LISTSCRIPTS/SETACTIVE/DELETESCRIPT/RENAMESCRIPT.

## Sieve (REQ-PROTO-SIEVE)

Sieve runs at delivery. See `06-filtering.md` for the pipeline; here we list language support.

Core:
- **REQ-PROTO-60** MUST implement Sieve (RFC 5228) base language.
- **REQ-PROTO-61** MUST implement extensions: `fileinto` (5228), `reject` (5429), `envelope` (5228), `imap4flags` (5232), `body` (5173), `vacation` (5230), `relational` (5231), `subaddress` (5233), `regex` (draft / de facto), `copy` (3894), `include` (6609), `variables` (5229), `date` (5260), `mailbox` (5490), `mailboxid` (9042), `encoded-character` (5228), `editheader` (5293), `duplicate` (7352).
- **REQ-PROTO-62** Notification (`enotify` — RFC 5435) limited to mailto at v1; XMPP/SMS notifiers out of scope.
- **REQ-PROTO-63** `vacation-seconds` (6131) supported.
- **REQ-PROTO-64** `extlists` (6134), `foreverypart` (5703), `mime` (5703) — yes, required for modern content rules.
- **REQ-PROTO-65** `spamtest`/`spamtestplus` (5235) — yes; maps to our spam filter score.
- **REQ-PROTO-66** Non-standard Sieve extensions that break the sandbox — **no**: no `execute` / `extprograms` (spawn subprocess), no `llm` action (user script calling an external LLM), no arbitrary shell-out. The server's LLM spam classifier (REQ-FILT-10) is a *separate*, server-side pipeline step that runs *before* Sieve and produces a score Sieve scripts can read via `spamtestplus` (REQ-PROTO-65) — that is the only way LLM output reaches a Sieve script.
- **REQ-PROTO-67** Per-user scripts; one active script at a time (ManageSieve `SETACTIVE`). Global/admin scripts run *before* user script.
- **REQ-PROTO-68** Sieve execution MUST be sandboxed: no filesystem access, no network (except `redirect`), bounded CPU + memory per invocation.

## TLS (wire concern — detailed in 04-email-security.md and 09-operations.md)

- **REQ-PROTO-70** MUST support TLS 1.2 and TLS 1.3 on all protocols. TLS 1.0/1.1 rejected.
- **REQ-PROTO-71** Cipher suites: Mozilla "intermediate" by default, "modern" opt-in.
- **REQ-PROTO-72** SNI-based certificate selection across all listeners.
- **REQ-PROTO-73** ALPN advertised where relevant (HTTP/1.1, h2 for JMAP).

## HTTP surfaces the server DOES serve

All HTTPS, all on one port (default 443) with path-based routing unless the operator configures a dedicated admin port. SNI-aware cert selection per hostname.

| Path / host | Purpose | REQ refs |
|---|---|---|
| `POST /jmap`, `/jmap/eventsource`, `/jmap/upload/*`, `/jmap/download/*` | JMAP Core + Mail + SSE push | REQ-PROTO-40..48 |
| `/.well-known/jmap` | JMAP session discovery | RFC 8620 |
| `/api/v1/mail/send`, `/send-raw`, `/send-batch`, `/send/quota`, `/send/stats` | **HTTP send API** (mail-submission for apps) | REQ-SEND-01..61 |
| `/api/v1/hooks/*` | Mail-arrival webhook subscriptions (register/list/delete) | REQ-HOOK-01..05 |
| `/api/v1/mail/blobs/<id>/raw?sig=…` | Signed fetch URL served to webhook receivers fetching message bodies | REQ-HOOK-30..31 |
| `/api/v1/principals/*`, `/domains/*`, `/queue/*`, `/spam/*`, `/sieve/*`, `/tls/*`, `/reports/*`, `/audit-log`, `/server/*` | Admin REST API | REQ-ADM-10..22 |
| `/api/openapi.json` | OpenAPI 3.1 spec | REQ-ADM-05 |
| `/admin/*` (phase 2) | Admin web UI (HTMX + Go templates) | REQ-ADM-200..202 |
| `/settings/*` (phase 3) | User self-service panel | REQ-ADM-203 |
| `/auth/oidc/<provider>/link`, `/auth/oidc/<provider>/callback` | External OIDC federation flow (RP only) | REQ-AUTH-51 |
| `/healthz/live`, `/healthz/ready` | Health endpoints (unauthenticated) | REQ-OPS-110..112 |
| `/metrics` | Prometheus exposition (unauthenticated, separate bind by default) | REQ-OPS-90..92 |
| `mta-sts.<hosted-domain>/.well-known/mta-sts.txt` | MTA-STS policy for each hosted domain (separate vhost, cert via ACME) | REQ-SEC-54 |
| `/.well-known/acme-challenge/<token>` (port 80) | ACME HTTP-01 challenge responder; ephemeral, only during ACME dance | REQ-OPS-50 |

## What we don't do on the wire

HTTP is one of our primary surfaces (see the table above). What we *don't* serve or speak:

- **No HTTP endpoints outside the table above.** Specifically: no CalDAV/CardDAV/WebDAV (groupware dropped, NG3), no generic file-serving, no OIDC-issuer endpoints (we are a relying party only, NG11), no arbitrary plugin-registered HTTP routes, no "POST an RFC 5322 message to /inbox" (the send API is structured, not raw-upload), no SES receive-rule DSL.
- **No DNS server.** We're a resolver client only.
- **No LDAP**, not even as client — out of scope.
- **No SNMP.**
- **No gRPC.**
- **No WebSockets** except optional JMAP push (RFC 8887) in phase 3.

## Open questions

- Sieve `notify` via Web Push: worth it, or mailto-only forever? (Web Push itself is phase 3 per REQ-PROTO-48.)
