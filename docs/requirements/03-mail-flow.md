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
- **REQ-FLOW-62** Queue item carries: message blob reference, sender, recipient, next-attempt time, attempt count, last error, optional priority, DSN NOTIFY flags, expiry time.

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

## Out of scope

- SMTP relay authentication via source-IP whitelisting beyond a `trusted_networks` config (avoid; it's a misconfiguration vector).
- Outbound scheduling by sender reputation (email warmup automation). Operator's job.
- Milter protocol for external filters.
