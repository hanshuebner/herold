# 03 — Mail flow

The path a message takes from the wire to a mailbox (inbound) or from a mailbox to a remote host (outbound). Detailed queue and scheduling design in `architecture/04-queue-and-delivery.md`; this doc is behavioral.

## Ingress paths

| Path | Listener | Auth | Destination |
|---|---|---|---|
| External relay in | 25/tcp | none (DMARC/SPF scored) | local delivery or rejected |
| Authenticated submission | 587, 465, JMAP | required | outbound queue |
| Local delivery (internal) | in-process | trusted | mailbox or outbound queue |

No LMTP ingress in v1 — delivery is in-process (see `04-queue-and-delivery.md`).

## Inbound (external → local mailbox)

### Acceptance

- **REQ-FLOW-01** MUST perform these checks before `DATA` accepted:
  1. Listener allows this connection (rate, IP allow/deny).
  2. EHLO hostname is a valid FQDN.
  3. MAIL FROM: syntactically valid; SPF record fetched for later scoring (not rejected on SPF alone).
  4. RCPT TO: each recipient resolves to a local principal *or* is a valid relay target (authenticated submission only).
  5. Message SIZE ≤ listener limit.
- **REQ-FLOW-02** After DATA, MUST parse the message headers before accepting 250 OK, to fail fast on malformed messages.
- **REQ-FLOW-03** Acceptance MUST be durable — an accepted message is written to persistent storage (queue or mailbox) before the 250 OK is sent. No `fsync`-optional fast path.
- **REQ-FLOW-04** SMTP-layer rejection reasons (5xx) MUST be surfaced in RFC 3463 enhanced status codes and logged with the remote IP / EHLO / MAIL FROM / RCPT / reason.

### Delivery decisions

For each local recipient:

1. **Reject** (5xx): unknown recipient with no catch-all, recipient over quota with `REJECT` policy, recipient disabled.
2. **Defer** (4xx): temporary failure (storage error, directory unreachable, greylist match).
3. **Accept**: proceeds to the filter pipeline.

- **REQ-FLOW-10** Unknown recipients MUST be rejected at RCPT time (5.1.1) unless a catch-all exists.
- **REQ-FLOW-11** Over-quota behavior: if principal `quota_policy=reject`, return 5.2.2. If `quota_policy=defer`, return 4.2.2 (default). No silent drop.
- **REQ-FLOW-12** Disabled principal (admin-set flag): 5.2.1 "mailbox disabled".
- **REQ-FLOW-13** Greylisting (if enabled): 4.5.1 on first-seen triplet (IP/24, MAIL FROM, RCPT), accept after configurable delay (default 5 min) on retry.

### Filter pipeline

Runs once the message is accepted in principle but before it's written to the recipient's mailbox. See `06-filtering.md` for full detail.

1. **Message validation** — headers parseable, size checks, MIME well-formed enough to scan (malformed MIME ≠ reject, but noted in score).
2. **Authentication verification** — SPF verify, DKIM verify (all signatures), DMARC evaluate, ARC evaluate. Results recorded in `Authentication-Results` header.
3. **Spam scoring** — rule engine produces a numeric score and a label (`ham` / `maybe_spam` / `spam`). See REQ-FILT-*.
4. **Global Sieve** (admin-defined, all recipients) — may `discard`, `reject`, `redirect`, `addheader`.
5. **Per-recipient Sieve** — one active script per recipient. Has access to score and headers. Final decision: `keep` (Inbox), `fileinto <folder>`, `discard`, `reject`, `redirect`.
6. **Delivery** — message body written to blob store, metadata written to mailbox store.

- **REQ-FLOW-20** The `Received` header added at acceptance MUST include the protocol, encryption status (ESMTPS + TLS version + cipher), EHLO name, client IP, and the server-assigned message ID.
- **REQ-FLOW-21** The `Authentication-Results` header MUST use the authserv-id from config (per RFC 8601).
- **REQ-FLOW-22** A fatal Sieve error (parse error mid-script, runtime error) on the *user* script MUST NOT lose the message — fall back to "keep to Inbox" and log the error to the admin audit log.
- **REQ-FLOW-23** A fatal Sieve error on the *global* script MUST defer the message (4.x.x) — operator must see and fix.

### Fan-out

- **REQ-FLOW-30** One inbound message for N local recipients becomes one blob (single storage), N mailbox references. No duplicate blob storage.
- **REQ-FLOW-31** Sieve runs once per recipient; results may differ (different folders, different discard decisions).
- **REQ-FLOW-32** DMARC policy that forces `reject` applies at RCPT phase once DMARC evaluated; if DMARC can only be evaluated after DATA, apply at the end of DATA phase with appropriate rejection.

## Outbound (authenticated submission → remote)

### Acceptance

- **REQ-FLOW-40** Submission MUST require AUTH. Anonymous submission is never allowed on 587/465/JMAP.
- **REQ-FLOW-41** MUST validate that the `MAIL FROM` (or JMAP `from`) is an address owned by the authenticated principal. Override requires admin permission or explicit "allowed identities" list.
- **REQ-FLOW-42** MUST rewrite `MAIL FROM` to match envelope if mismatch policy is `rewrite` (default). Policies: `rewrite` | `reject` | `allow`.
- **REQ-FLOW-43** MUST add a `Message-ID` header if missing. MUST add `Date` if missing.
- **REQ-FLOW-44** MAY strip certain headers (`X-Originating-IP`, bare `Bcc` if leaked) — configurable.

### Signing and authentication

- **REQ-FLOW-50** Outbound messages from a sending domain MUST be DKIM-signed using the domain's active key(s). Multiple DKIM signatures allowed (e.g. for DKIM2 transition).
- **REQ-FLOW-51** MUST add `Return-Path` (after queuing) and correct `Received` on the server side.
- **REQ-FLOW-52** ARC-sealing outbound is optional (some orgs want it for forwarders); default off.

### Queue insertion

- **REQ-FLOW-60** Every accepted outbound message goes into the durable outbound queue. No synchronous delivery.
- **REQ-FLOW-61** Each recipient of a multi-recipient message becomes an independent queue item keyed by destination host/IP (to allow per-destination scheduling and retries).
- **REQ-FLOW-62** Queue item carries: message blob reference, sender, recipient, next-attempt time, attempt count, last error, optional priority, DSN NOTIFY flags, expiry time, **scheduled-not-before time (`send_at`)**.
- **REQ-FLOW-63** Queue MUST honour the `EmailSubmission.sendAt` value on submission insertion (REQ-PROTO-58). The first delivery attempt is gated by `next_attempt_time = max(now, send_at)`; the submission is invisible to remote SMTP delivery until `send_at`. `EmailSubmission/set { destroy }` issued before `send_at` MUST atomically remove the queue items belonging to the submission — no message goes out. Cancellation after `send_at` is best-effort: if the message has already begun handoff to remote SMTP, destroy is a no-op. This pairs with tabard's send-undo and (later) user-facing scheduled send.

### Delivery

- **REQ-FLOW-70** MX resolution per recipient domain; fallback to A/AAAA if no MX.
- **REQ-FLOW-71** MUST honor MTA-STS (RFC 8461) and DANE TLSA (RFC 7672) when present. Policy: prefer DANE > MTA-STS-enforce > opportunistic TLS > plaintext. Plaintext-only delivery MAY be disabled globally.
- **REQ-FLOW-72** MUST support TLS-RPT (RFC 8460) — emit TLS reports to the recipient domain's `rua` if configured.
- **REQ-FLOW-73** Connection reuse across queue items going to the same host — open once, send N messages, QUIT.
- **REQ-FLOW-74** Per-destination concurrency limit (default 5) and per-destination rate limit (configurable, default off).
- **REQ-FLOW-75** Retry schedule (default): immediate, +1m, +5m, +15m, +1h, +3h, +6h, +12h, +24h, then hourly to expiry (5 days). Per-domain overrides allowed. Schedule is configurable.
- **REQ-FLOW-76** On 5xx permanent failure, generate DSN (bounce) back to sender if requested by RFC 3464. On queue expiry, always send a delay-then-failure DSN.

### Relay policy

- **REQ-FLOW-80** MUST NOT be an open relay. Unauthenticated SMTP may only accept mail for local domains.
- **REQ-FLOW-81** Authenticated relay MAY be further restricted (e.g. certain users may only submit to internal domains). Policy enforced in the MTA, not via Sieve.

## Email reactions — cross-server propagation

Per `requirements/01-protocols.md` REQ-PROTO-100..103 (the JMAP extension) and `/Users/hans/tabard/docs/notes/server-contract.md` § Email reactions (the wire format and end-to-end behaviour).

When a user adds a reaction to a message that has recipients on another server, the reaction is propagated as an outbound email carrying structured reaction headers plus a human-readable body fallback. Herold-aware receivers consume the headers and apply as a native reaction; non-herold receivers see a normal short email threaded with the original.

Phase 2 — alongside the rest of the chat / suite work.

### Outbound

- **REQ-FLOW-100** When `Email/set` adds a reaction (`reactions/<emoji>/<principal-id>: true`), herold MUST examine the original message's recipient set. For each recipient address whose domain is NOT a local domain, herold queues an outbound reaction email per the wire format below. Recipients on local domains see the reaction natively via `Email.reactions` state push; no email goes out for them.
- **REQ-FLOW-101** Wire format of the outbound reaction email:
  - `From`: the reactor's primary identity address.
  - `To`: each external recipient (one outbound queue item per recipient, per REQ-FLOW-61 fanout rules).
  - `Subject`: `Re: ` + original subject (matching standard reply convention so receiving clients thread it correctly).
  - `In-Reply-To`: original `Message-ID`.
  - `References`: original `References` + original `Message-ID`.
  - `Date`: now.
  - `Message-ID`: a fresh id for this reaction email.
  - `X-Tabard-Reaction-To`: original `Message-ID` (verbatim, including angle brackets).
  - `X-Tabard-Reaction-Emoji`: the UTF-8 emoji (no encoding wrapping).
  - `X-Tabard-Reaction-Action`: `add` (only `add` propagates; see REQ-FLOW-103).
  - `Content-Type`: `multipart/alternative` with two parts: a `text/plain` body "<reactor display name> reacted with <emoji> to your message." and a `text/html` body with the same text and the emoji rendered larger.
- **REQ-FLOW-102** Reaction emails follow the normal outbound queue path (REQ-FLOW-50..76) — DKIM-signed, retried, DSN-on-failure. They are NOT distinguished by the queue; the headers are the only chat-aware signal. (DSNs on failed reaction emails are noisy but acceptable; reaction emails are short so failure is rare.)
- **REQ-FLOW-103** Removing a reaction does NOT emit a reaction email. The remove is local to the reactor's server and any local-domain recipients on the same herold. Rationale: an "X removed their reaction" email to non-tabard receivers is awkward UX; reactions are ephemeral signals and the asymmetry is acceptable. (Confirmed by tabard product decision; see `/Users/hans/tabard/docs/requirements/02-mail-basics.md` REQ-MAIL-183.)

### Inbound

- **REQ-FLOW-104** On inbound mail, herold MUST detect the reaction-header set: `X-Tabard-Reaction-To`, `X-Tabard-Reaction-Emoji`, `X-Tabard-Reaction-Action`. Presence of all three triggers the reaction-handling path; absence delivers normally.
- **REQ-FLOW-105** Reaction-handling path:
  1. Look up the recipient's local mailbox copy of the original `Message-ID` (`X-Tabard-Reaction-To` value). The lookup is per-principal (the principal whose mailbox is the inbound destination).
  2. If found, identify the reactor by their `From` address. The reactor must be either the original sender or a recipient (To/Cc/Bcc) of the original message — to prevent third-party spoofing of reactions.
  3. If the reactor is recognised: apply as a native reaction by patching `Email.reactions/<emoji>/<reactor-principal-id>: true` on the local message copy. The reaction email is consumed — NOT delivered to the recipient's inbox. The Email's state string advances; the JMAP push notifies any active client.
  4. If the original message is NOT found in the recipient's mailbox, OR the reactor is NOT a recognised participant: deliver the email normally. The recipient sees a regular short email with the reaction text body, threaded by `In-Reply-To`.
- **REQ-FLOW-106** The reactor-recognition step in REQ-FLOW-105.2 uses the reactor's `From` address to look up a principal id. For external reactors (not on this herold), the principal id is allocated as a synthetic external-principal record (the same machinery that backs `From` display in mail UI). Reactor-principal ids are stable per address; the same external sender reacting twice yields the same id.
- **REQ-FLOW-107** If REQ-FLOW-105.3 succeeds (native reaction applied), the inbound queue records a metric `reaction_consumed_total` per recipient principal. The email body is not stored in the blob store; the action is purely metadata mutation. (This is the only case where inbound mail does NOT result in a stored Email — surfacing it explicitly so the storage GC and retention paths don't trip.)
- **REQ-FLOW-108** Spam classification (`requirements/06-filtering.md`) runs BEFORE the reaction-detection check. A reaction email scored as spam is delivered to junk normally — the operator's spam policy wins over reaction handling. (Edge case; spam-flagged reactions are unlikely but not protected against.)

## Loop and delivery-status protection

- **REQ-FLOW-90** MUST honor `Auto-Submitted:` headers: do not auto-reply to messages with `Auto-Submitted: auto-replied` or `auto-generated` (vacation responder, DSN generator).
- **REQ-FLOW-91** MUST implement Received-header loop detection: max 50 `Received` headers before rejection (per SMTP convention).
- **REQ-FLOW-92** Bounce suppression: a bounce to a non-existent sender generates a double-bounce; limit to one double-bounce per sender.

## Forwarding

Two distinct concepts kept separate:

- **Alias**: a local address that fans out to one or more local and/or remote addresses. Admin-configured. Applied at RCPT TO time; DMARC considerations described in `04-email-security.md`.
- **Sieve redirect**: a user action on an already-accepted message. Triggers ARC sealing (to preserve original authentication). Rate-limited per user.

- **REQ-FLOW-100** Aliases MUST resolve recursively with a depth limit (default 10).
- **REQ-FLOW-101** Sieve `redirect` count MUST be capped per message (default 5) to prevent amplification.

## Smart host (outbound relay)

Default outbound delivery is direct-MX (REQ-FLOW-70..76). Some operator deployments cannot or do not want to do that: a residential/cloud VM with port 25 blocked outbound, an enterprise that mandates an internal corporate relay, a deliverability-conscious operator who wants every outbound message to flow through AWS SES / SendGrid / Mailgun / a Gmail G-Suite SMTP relay. For those cases, herold supports a **smart host** in `system.toml`. Architecturally, the smart-host fork lives at the same point in the queue worker as the MX-resolution step (`architecture/04-queue-and-delivery.md` §Grouping). Provider-specific `system.toml` examples for SES, SendGrid, Mailgun, Gmail relay, and a generic corp-relay sit under `docs/user/examples/system.toml.smarthost`.

- **REQ-FLOW-SMARTHOST-01** Operator config block `[smart_host]` in `system.toml` with fields `enabled` (bool, default `false`), `host` (string), `port` (int -- `587` for STARTTLS submission, `465` for implicit TLS, `25` permitted only in dev mode), `tls_mode` (`starttls` | `implicit_tls` | `none`; defaults to `starttls` for 587 and `implicit_tls` for 465), `auth_method` (`plain` | `login` | `scram-sha-256` | `xoauth2` | `none`), `username` (used only when `auth_method != none`), `password_env` / `password_file` (secret reference per REQ-OPS-04 / REQ-OPS-161), `fallback_policy` (`smart_host_only` | `smart_host_then_mx` | `mx_then_smart_host`, default `smart_host_only`), `connect_timeout_seconds` and `read_timeout_seconds` (bounded). When `enabled=false` the queue does direct-MX delivery exactly as REQ-FLOW-70..76 specify (see REQ-OPS-03, REQ-OPS-30).
- **REQ-FLOW-SMARTHOST-02** `[smart_host.per_domain.<recipient-domain>]` sub-tables override the global smart-host fields on a per-recipient-domain basis (e.g. corporate mail through a corp relay, consumer mail through SES); match is exact on the RCPT TO domain with no wildcard support in v1, falling through to the global `[smart_host]` block on no match (see REQ-FLOW-61, REQ-FLOW-70).
- **REQ-FLOW-SMARTHOST-03** Smart-host delivery MUST DKIM-sign with the herold-side sending domain key (REQ-SEC-10..15) just as direct-MX delivery does; the upstream relay's own DKIM/ARC behaviour is opaque to herold. The SMTP envelope to the upstream uses the herold-side `MAIL FROM` / return-path unchanged -- v1 does NOT rewrite RFC 5322 headers, the From/Reply-To/Sender chain, or the envelope sender for a smart-host hop (see REQ-FLOW-50, REQ-FLOW-51).
- **REQ-FLOW-SMARTHOST-04** TLS verify mode for the smart-host upstream is `tls_verify` (`system_roots` | `pinned` | `insecure_skip_verify`); default `system_roots`, which is the only acceptable production value when the upstream is a public deliverability provider (e.g. `email-smtp.us-east-1.amazonaws.com:587` STARTTLS or `:465` implicit TLS). `pinned` requires a `tls_pin_sha256` SPKI hash for self-hosted enterprise relays; `insecure_skip_verify` is dev-only and refused in production builds (see REQ-SEC-80, REQ-SEC-91, REQ-SEC-92).
- **REQ-FLOW-SMARTHOST-05** Auth credentials MUST be supplied via `password_env=<NAME>` or `password_file=<path>` per REQ-OPS-04 / REQ-OPS-161 -- inline plaintext is rejected at `herold server config-check` time (REQ-OPS-06). The recommended operator-friendly shape is `password_env=AWS_SES_SMTP_PASSWORD`; mode-0600 file references are the supported alternative for setups that prefer file-based secrets over the environment.
- **REQ-FLOW-SMARTHOST-06** When `fallback_policy=smart_host_then_mx`, a smart-host attempt is considered failed and falls back to direct-MX after EITHER (a) connection refused / TCP unreachable for `fallback_after_failure_seconds` continuous seconds (default 300, sustained outage) OR (b) the upstream returns a 5xx on the SMTP transaction (immediate fallback for that one delivery). `mx_then_smart_host` is the inverse (try MX, fall back to smart host on persistent failure); `smart_host_only` refuses to fall back and surfaces the error per REQ-FLOW-75. Each delivery records which path fired and why in the audit log (see REQ-FLOW-75, REQ-ADM-300).
- **REQ-FLOW-SMARTHOST-07** The substrate accepts smart-host configurations targeting AWS SES (IAM user with `ses:SendRawEmail`, SMTP password derived per AWS docs from the IAM secret access key), SendGrid, Mailgun, and Gmail G-Suite SMTP relay (app password). Provider-specific recipe documents live in `docs/user/examples/system.toml.smarthost`; this REQ only confirms that the `[smart_host]` config surface is sufficient to express those four canonical setups without provider-specific code paths.
- **REQ-FLOW-SMARTHOST-08** Each smart-host attempt emits `herold_smtp_outbound_total{path="smart_host"|"direct_mx", outcome=...}` and a connection-establishment latency histogram `herold_smtp_outbound_connect_seconds{path=...}`. The audit log records the chosen path and the firing fallback policy per delivery (REQ-ADM-300). These metrics extend the queue-and-delivery families enumerated in REQ-OPS-91.

## Out of scope

- SMTP relay authentication via source-IP whitelisting beyond a `trusted_networks` config (avoid; it's a misconfiguration vector).
- Outbound scheduling by sender reputation (email warmup automation). Operator's job.
- Milter protocol for external filters.
- Smart-host header rewriting, From-rewriting, or SRS-style envelope rewriting on the smart-host hop. Direct-MX SRS for forwarding is covered separately under `architecture/04-queue-and-delivery.md` §Submission vs relay envelope rewriting.
- Wildcard / suffix matching in `[smart_host.per_domain.*]`. Exact-match only in v1; operators with hundreds of routed domains can revisit in a later wave.
