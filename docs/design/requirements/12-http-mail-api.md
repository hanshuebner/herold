# 12 — HTTP mail API: sending and incoming webhooks

Two orthogonal HTTP interfaces for application integration:

- **Send API** — apps POST mail to the server instead of opening an SMTP submission session. Our shape, not AWS SES's verbatim, but close enough that an SES-based app ports in a day.
- **Incoming webhooks** — when mail arrives for a watched address/domain, server POSTs to a registered URL with message metadata + body access. The automation counterpart to JMAP push.

Both live under `/api/v1/mail/...` alongside the admin API. Same auth model (API keys, scoped).

## Part A: Send API

### Endpoints

- **REQ-SEND-01** `POST /api/v1/mail/send` — send a single message. Request body is JSON; response includes the message ID and queue entry ID.
- **REQ-SEND-02** `POST /api/v1/mail/send-raw` — send a raw RFC 5322 message. Request body is `message/rfc822`. Useful for apps that already produce MIME.
- **REQ-SEND-03** `POST /api/v1/mail/send-batch` — submit up to N messages atomically (all queued or none). Bounded request size.
- **REQ-SEND-04** `GET /api/v1/mail/send/quota` — return current sender's daily / per-minute quota and consumption.
- **REQ-SEND-05** `GET /api/v1/mail/send/stats?since=<ts>` — counters of sent / delivered / deferred / bounced / complained for the authenticated principal.

### Request (`/send` — structured form)

```json
{
  "from": "alerts@example.com",
  "reply_to": "noreply@example.com",
  "to": ["alice@example.net"],
  "cc": [],
  "bcc": [],
  "subject": "Deployment complete",
  "text": "The deployment to prod-west finished at 14:02 UTC.",
  "html": "<p>The deployment to <strong>prod-west</strong> finished at 14:02 UTC.</p>",
  "headers": {
    "X-App-Correlation": "deploy-4812"
  },
  "attachments": [
    {
      "filename": "report.pdf",
      "content_type": "application/pdf",
      "content_base64": "JVBERi0xLjQK..."
    }
  ],
  "tags": ["deploys", "prod-west"],
  "configuration_set": "transactional-mail",
  "return_path": null
}
```

- **REQ-SEND-10** Required fields: `from`, at least one of `to`/`cc`/`bcc`, at least one of `text`/`html`/`raw_parts`, `subject`.
- **REQ-SEND-11** Server assembles the MIME message. Default multipart/alternative for text+html.
- **REQ-SEND-12** `from` MUST be an address the authenticated principal is allowed to send as (REQ-FLOW-41). Override permission via "allowed identities" list on the principal.
- **REQ-SEND-13** `tags` are attached to the queue entry and the stats counters (`herold_send_total{tag="deploys"}`). No tag-based storage retention policy in v1.
- **REQ-SEND-14** `configuration_set` maps to a named set of per-send defaults stored in application config (signing domain, custom headers, custom retry schedule override, event filter for publishing). Stalwart-enterprise-ish but small — think "policy profiles."
- **REQ-SEND-15** `return_path` override requires admin permission; default is the principal's default bounce address.
- **REQ-SEND-16** `idempotency_key` optional — if present, a repeated call with the same key within 24 h returns the same response without re-queuing. (SES has this via `MessageDeduplicationId`; we mirror.)

### Response

```json
{
  "message_id": "<0194bf22-c3b0-7fe9-aef9-8f9d1d2e7f21@example.com>",
  "queue_ids": ["01J3KXAZ9Y7P8M2"],
  "accepted_rcpts": ["alice@example.net"],
  "rejected_rcpts": []
}
```

- **REQ-SEND-20** Response MUST include the generated `Message-ID` and queue IDs (one per recipient per fanout).
- **REQ-SEND-21** Partial acceptance: if some recipients accepted and others rejected (e.g. local recipient over quota vs. external recipient OK), response lists both sets. HTTP 207 semantics not used; 200 with lists is clearer.

### Auth and authorization

- **REQ-SEND-30** Authenticated via Bearer token (API key, scoped) or OIDC-issued access token. API keys have scope `mail.send` plus optional domain/address constraints (`allowed_from_addresses`, `allowed_from_domains`).
- **REQ-SEND-31** API keys per principal or per service account (principal-kind=`service`). Service accounts are principals without mailboxes (REQ-AUTH-01 extension).
- **REQ-SEND-32** Rate limit per key: sent-per-minute, sent-per-day. 429 on exceed with `Retry-After`.

### Errors

- **REQ-SEND-40** 4xx for client error (bad from, bad MIME, over quota). 5xx for server error (storage unavailable). Stable error codes in response body.
- **REQ-SEND-41** Body size limit (default 30 MB per request). 413 on exceed.
- **REQ-SEND-42** Per-recipient errors surfaced in the `rejected_rcpts` array of an otherwise 2xx response; HTTP-level 4xx only for request-wide failures.

### Deliverability handling (plumbing)

- **REQ-SEND-50** Accepted messages go through the same outbound queue as SMTP submissions. Same retry schedule, DKIM signing, MTA-STS/DANE, DSN generation.
- **REQ-SEND-51** Bounce tracking: messages sent via API have their bounce events recorded against the `tags` and `configuration_set` for the stats endpoint.
- **REQ-SEND-52** Per-message tracking ID (`X-Herold-Id` header) stamped at queue time. Makes log correlation trivial.

### SES portability

- **REQ-SEND-60** Documented porting guide: field mapping from SES `SendEmail` / `SendRawEmail` to ours. Same field names where feasible (`from`, `to`, `cc`, `bcc`, `subject`, `body.text`/`body.html`, `tags`).
- **REQ-SEND-61** Not committed: SigV4 auth, exact SES JSON schemas, SES config-set full feature set, SES event destinations, SES suppression lists as REST entities. Apps switch to bearer auth + our events (REQ-EVT-*) for the destination side.

## Part B: Incoming webhooks

Fires when mail is delivered to a watched target. Target = (address | domain | principal). Register targets per-principal-key in application config.

### Registration

- **REQ-HOOK-01** `POST /api/v1/hooks` — create webhook subscription. Body:

```json
{
  "name": "ticket-system-intake",
  "target": { "kind": "address", "value": "tickets@example.com" },
  "url": "https://ticket.internal/mail",
  "secret": "whsec_...",
  "body_mode": "inline",
  "filter": { "verdict_in": ["ham", "suspect"] }
}
```

- **REQ-HOOK-02** `target.kind` ∈ {`address`, `domain`, `principal`}. Multiple hooks per target allowed.
- **REQ-HOOK-03** `body_mode` ∈ {`inline`, `url`}. `inline` embeds the message body in the POST (up to configurable size, default 1 MB). `url` provides a **signed fetch URL** valid for 24h that the receiver calls to retrieve the raw RFC 5322 body.
- **REQ-HOOK-04** `filter` narrows deliveries: by spam verdict, by subject regex (bounded), by header match, by attachment presence.
- **REQ-HOOK-05** `secret` used to HMAC-sign each delivery (`X-Herold-Signature: t=<ts>,v1=<hmac-sha256>`) so the receiver can verify authenticity.

### Delivery

- **REQ-HOOK-10** On mail arrival that matches a hook, server POSTs JSON payload. Payload includes: message ID, headers (parsed subset), sender, recipients, arrival timestamp, auth results, spam verdict, and either the body (inline) or a signed URL.
- **REQ-HOOK-11** POST timeout default 10 s. On 2xx → delivered. On 4xx → **permanent failure**, dropped after one retry. On 5xx / timeout / connection error → retried.
- **REQ-HOOK-12** Retry schedule: +0s, +1m, +5m, +30m, +2h, +12h, +24h, then give up (drop). Per-hook override possible.
- **REQ-HOOK-13** Delivery independent of mail delivery — webhooks don't block mailbox delivery. (Mail lands in the mailbox regardless; webhook fires afterwards.)

### Payload (inline body mode)

```json
{
  "event": "mail.received",
  "timestamp": "2026-05-10T12:34:56.789Z",
  "message_id": "<abc@example.com>",
  "from": "alice@example.net",
  "rcpt_to": ["tickets@example.com"],
  "envelope": { "mail_from": "alice@example.net", "rcpt_to": ["tickets@example.com"] },
  "subject": "Help: printer issue",
  "headers": { "From": "Alice <alice@example.net>", "Date": "...", ... },
  "auth": { "spf": "pass", "dkim": "pass", "dmarc": "pass", "arc": "none" },
  "spam": { "verdict": "ham", "confidence": 0.97 },
  "body": {
    "text": "Hi team,\n\nThe printer on floor 3 ...",
    "html": "<p>Hi team,</p><p>The printer on floor 3 ...</p>"
  },
  "attachments": [
    {
      "filename": "error.log",
      "content_type": "text/plain",
      "size": 2048,
      "fetch_url": "https://mail.example.com/api/v1/mail/blobs/01JK.../error.log?sig=..."
    }
  ]
}
```

- **REQ-HOOK-20** Body (text/html) inlined when total payload ≤ 1 MB. Otherwise body switches to `fetch_url` mode regardless of subscription preference (with a `body_truncated: true` marker).
- **REQ-HOOK-21** Attachments never inlined in the main payload; always fetch-URL referenced.

### Payload (url body mode)

Same as above, but `body` replaced with:

```json
"body": {
  "fetch_url": "https://mail.example.com/api/v1/mail/blobs/01JK.../raw?sig=...",
  "size_bytes": 3145728
}
```

- **REQ-HOOK-30** Signed fetch URL embeds `{message_id, exp, scope, hmac(secret, ...)}` — opaque to the receiver, validated on fetch.
- **REQ-HOOK-31** Fetch URL returns the raw `message/rfc822` body (the full original message). Receiver parses MIME themselves.

### Observability

- **REQ-HOOK-40** Per-hook metrics: `herold_hook_deliveries_total{name,status}`, `herold_hook_latency_seconds`, `herold_hook_retries_total`, `herold_hook_in_flight`.
- **REQ-HOOK-41** `herold admin hook test <name>` delivers a canned message to the webhook (with a distinct `event=test` marker) — useful after registration.
- **REQ-HOOK-42** `herold admin hook log --name ... --since ...` shows recent delivery attempts (request URL, status, latency, response body first 512 bytes).

### Interaction with Sieve

- **REQ-HOOK-50** Webhooks fire for messages **after** Sieve runs. Messages discarded by Sieve don't fire webhooks. Messages redirected still fire against the original recipient.
- **REQ-HOOK-51** Webhooks don't see headers added by Sieve `addheader` — hooks run against the final delivered state.

## Part C: Inbound image proxy

Distinct from the send API and webhooks. Tabard renders HTML mail in a sandboxed iframe; external `<img>` references go through this proxy so the user's browser doesn't directly contact the sender's image hosts (defeats tracking pixels, prevents IP exposure, allows server-side caching).

Used by tabard's reading pane (`/Users/hans/tabard/docs/requirements/13-nonfunctional.md` REQ-SEC-07; full contract in tabard's `notes/server-contract.md` § Image proxy).

### Endpoint

- **REQ-SEND-70** MUST serve `GET /proxy/image?url=<urlencoded-https-url>` to authenticated users (suite session cookie). No anonymous access — the proxy is a per-user feature, not a generic content tunnel.
- **REQ-SEND-71** Upstream URL constraints: `https://` only (`http://` returns 400). URL-encoded query parameter, max 2048 chars. Up to 3 redirect hops followed; further redirects abort with 502.

### Outgoing request shape

- **REQ-SEND-72** Request to upstream: NO `Cookie`, NO `Referer`, fixed generic `User-Agent` (e.g. `herold-image-proxy/1`) — same value for every request, no per-user fingerprinting. No other identifying headers.

### Response handling

- **REQ-SEND-73** Upstream `Content-Type` MUST start with `image/` — otherwise the proxy returns 415 to the client. Prevents the proxy from being used to tunnel arbitrary content.
- **REQ-SEND-74** Size cap: 25 MB per response (configurable). Larger upstreams: the proxy returns 413. Connection timeouts: 10 s connect, 30 s total.
- **REQ-SEND-75** Caching: honour upstream `Cache-Control`, capped at 24 h regardless. Shared cache keyed by URL hash (cross-user OK — the URL is the cache key; a hit for user B doesn't reveal that user A also opened it). LRU eviction on size pressure; operator-configurable max cache size.
- **REQ-SEND-76** Retries: one retry on 5xx / network error after 1 s. No retries on 4xx. After exhaustion, return upstream status (or 502 for network failures) verbatim — the client renders the browser's native broken-image placeholder. No tabard-side custom placeholder image.
- **REQ-SEND-77** Per-user rate limits: 200 fetches per minute, 10 per (user, upstream-origin) per minute, 8 concurrent. Rate-limit responses: 429 with a `Retry-After` header. Operator-configurable.

### Where it runs

- **REQ-SEND-78** v1: in-process inside herold (simplest fit for the single-node target). Future: may graduate to a herold plugin / sidecar so operators can swap in a third-party proxy if they want — out of scope for the initial implementation.

## Part D: AWS SES inbound (S3 + SNS)

Some operators want SES to be the public-facing MX (port-25 ingress, DKIM/SPF/DMARC verified inside AWS, mail rules dispatch to S3) and herold to be the mailbox + IMAP/JMAP layer. This is a specialisation of the generic webhook contract in Part B: the SNS notification points at an S3 object containing the raw RFC 5322 bytes, herold fetches the object, and from there the message follows exactly the same inbound path as an SMTP-delivered message (REQ-FLOW-01..32, REQ-FLOW-50..52 do not apply -- this is inbound, not outbound). The inverse (using SES as an outbound smart host) is covered by REQ-FLOW-SMARTHOST-01..08 in `requirements/03-mail-flow.md`. Operator-facing recipe (the SES rule action layout, the SNS topic ARN, IAM policies) lives in `docs/user/examples/system.toml.smarthost` alongside the smart-host recipes.

- **REQ-HOOK-SES-01** A SES receipt rule action delivers raw RFC 5322 message bytes to an operator-controlled S3 bucket and notifies via SNS; herold subscribes to the SNS topic and exposes `POST /hooks/ses/inbound` as the SNS HTTPS subscription target. The webhook body is the SNS Notification envelope wrapping a SES `Mail` notification (`mail.messageId` plus `receipt.action.bucketName` / `receipt.action.objectKey`); herold fetches the S3 object and processes the bytes as if they had arrived via SMTP (REQ-FLOW-01..32, see REQ-HOOK-SES-04).
- **REQ-HOOK-SES-02** The webhook MUST verify the SNS message signature against the X.509 cert at `SigningCertURL` (RSA over the SNS-canonical signing-string per the SNS HTTPS-subscriber contract), MUST verify the cert chain to a system root CA, MUST dedupe by `MessageId` for at least 24h to defeat replay, and MUST auto-confirm `SubscriptionConfirmation` requests only when the SNS topic ARN is in `sns_topic_arn_allowlist` (REQ-HOOK-SES-03). Signature, replay, and SubscribeURL outcomes are observable per REQ-HOOK-SES-05.
- **REQ-HOOK-SES-03** Operator config block `[hooks.ses_inbound]` in `system.toml` with fields `enabled` (bool, default `false`), `aws_region` (string), `s3_bucket_allowlist` (`[]string` -- buckets the webhook is allowed to fetch from; references to other buckets are rejected), `sns_topic_arn_allowlist` (`[]string` -- topic ARNs from which `SubscribeURL` confirmations are auto-confirmed), `aws_credentials_env` / `aws_credentials_file` (IAM credentials reference per REQ-OPS-04 / REQ-OPS-161; the IAM principal MUST hold `s3:GetObject` on the allowlisted bucket), and `signature_cert_host_allowlist` (`[]string` -- typically `sns.<region>.amazonaws.com`, constraining the host the SigningCertURL fetch is allowed to dial).
- **REQ-HOOK-SES-04** After the S3 fetch succeeds, the message bytes traverse the same pipeline as SMTP-delivered mail: DKIM verification (REQ-SEC-20..22), SPF verification using the SES-reported source IP from `mail.source` as the apparent sending host (REQ-SEC-01..04), DMARC evaluation (REQ-SEC-30..33), Sieve (REQ-FLOW-22, REQ-FLOW-23), spam classification, categorisation, and mailbox delivery (REQ-FLOW-30, REQ-FLOW-31). No SES-specific code runs past the fetch boundary.
- **REQ-HOOK-SES-05** Per-flow metrics `herold_hook_ses_received_total{outcome=...}`, `herold_hook_ses_signature_verify_total{outcome=...}`, and `herold_hook_ses_s3_fetch_total{outcome=...}` (extending the family in REQ-OPS-91 / REQ-HOOK-40). The audit log records every accepted SES inbound message's SNS `MessageId`, S3 bucket, and S3 object key (REQ-ADM-300).
- **REQ-HOOK-SES-06** The S3 `GetObject` fetch and the `SigningCertURL` fetch MUST traverse the same `internal/netguard` SSRF predicate that the inbound image proxy (REQ-SEND-71..72) and the LLM categoriser use: refuse to dial RFC 1918 / loopback / link-local / IPv6-ULA / multicast destinations. Operators expose AWS regional endpoints, never internal addresses; this guard prevents a maliciously crafted `SigningCertURL` or a hijacked SNS payload from steering the substrate at internal services.
- **REQ-HOOK-SES-07** Out of scope for v1 in this spec: SES OUTBOUND -- using SES as a smart host -- is configured via `[smart_host]` (REQ-FLOW-SMARTHOST-01..08), NOT via a separate inbound spec. Bit-compat with the SES API surface remains a non-goal per `00-scope.md` NG12.

## What's NOT in v1

- SES suppression lists as REST-managed entities.
- SES-style receipt rule DSL (S3 destinations, SNS actions — we hand these off to `event-publisher` plugins if operators want).
- Kafka / RabbitMQ delivery of mail bodies — use an `event-publisher` plugin for that.
- Client-scoped OAuth for external apps (phase 2).
- Signed sender reputation history (operators who want this roll their own via events).
