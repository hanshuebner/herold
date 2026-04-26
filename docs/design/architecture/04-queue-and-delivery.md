# 04 — Queue and delivery

The outbound queue: scheduling, retries, and delivery mechanics. Inbound is straight-line (accept → filter → deliver) and doesn't need its own queue design; this doc is the outbound MTA.

## What the queue is

A persistent FIFO+priority-queue of **delivery attempts**. One attempt per (message, recipient) pair per hop.

Stored in the metadata store as table `queue`:

```
queue(
  id            u64 primary,
  message_id    u64,
  blob_id       hash,
  sender        text,           -- envelope MAIL FROM
  recipient     text,           -- envelope RCPT TO
  destination   text,           -- derived: MX host + port or literal host
  state         enum(pending, in_flight, held, failed, done),
  attempts      int,
  next_attempt  timestamp,
  last_error    text nullable,
  last_status   text nullable,  -- RFC 3463 enhanced status
  priority      int default 0,
  notify_flags  int,            -- DSN NOTIFY: success/failure/delay
  envid         text nullable,  -- DSN ENVID
  orcpt         text nullable,  -- DSN ORCPT
  created_at    timestamp,
  expires_at    timestamp,      -- permanent-failure deadline
  idempotency   hash            -- for dedup on replay
)
```

One row per recipient, not per message. This is important: a 100-recipient message is 100 queue items with 100 independent schedules.

## Submission path (enqueue)

Client submits via SMTP (587/465) or JMAP `EmailSubmission/set`:

1. Message authenticated and envelope validated (REQ-FLOW-40–44).
2. Message parsed, DKIM-signed, outbound headers added.
3. Message blob written to blob store (content-addressed).
4. For each recipient:
   - MX lookup deferred to delivery time (don't bake in at enqueue — MX can change).
   - Row inserted into `queue` with state=`pending`, `next_attempt=now`, `attempts=0`, `expires_at=now+expiry`.
5. Single metadata-store transaction covers all inserts.
6. Scheduler woken up (in-process notify; no polling delay).

## Scheduling

One scheduler task runs continuously:

1. Query `queue` for items where `state='pending' AND next_attempt <= now()`, ordered by `next_attempt` then `priority DESC`, limit batch size.
2. For each item:
   - Mark `state='in_flight'` (atomic compare-and-swap on state; another worker can't pick the same row).
   - Hand to a delivery worker.
3. Sleep until the soonest `next_attempt` for any pending item, or wake-on-new-item signal, whichever comes first.

Key property: **no polling**. Scheduler blocks until there's work.

## Grouping: one SMTP session per destination

Delivery worker groups items by destination host. Rationale: RFC-good behavior + throughput.

1. Worker picks up a batch of in-flight items.
2. Groups by MX host (after resolution).
3. For each (destination, batch):
   a. Open SMTP connection (with TLS per MTA-STS/DANE).
   b. HELO/EHLO.
   c. Send items sequentially on the connection (one MAIL/RCPT/DATA per item).
   d. Close with QUIT.

Concurrency caps:
- Per-destination concurrent sessions (default 5, configurable).
- Per-destination-IP max messages per session (default 100, then QUIT and reopen).
- Global delivery concurrency (default 50 simultaneous sessions).

## Retries

Per-item state transitions after a delivery attempt:

- **2xx accepted by receiver** → `state='done'`, blob refcount decremented (if no other refs hold it), DSN if `NOTIFY=success`.
- **4xx temporary** → increment `attempts`, compute `next_attempt` from retry schedule, `state='pending'`. If `next_attempt >= expires_at`, treat as permanent failure.
- **5xx permanent** → `state='failed'`, generate DSN bounce to sender, blob refcount handling.
- **Connection error / DNS error / TLS error** → treat as 4xx retry.
- **Policy error** (MTA-STS enforce + cert invalid, DANE mismatch) → treat as 4xx retry unless policy says fail-closed permanently.

Retry schedule (default; configurable in config):

```
+0s, +1m, +5m, +15m, +1h, +3h, +6h, +12h, +24h, +48h, +72h, then expires at 5 days.
```

Per-destination override possible: e.g. "retry Gmail on a tighter schedule because they rate-limit gently" or "retry big-enterprise-customer's domain on a looser schedule because they impose long greylisting".

## Hold and release

Operators can:

- Hold a specific item (`queue show` → `queue hold <id>`) → `state='held'`.
- Hold all items matching a filter (by sender, by destination, by keyword).
- Release → `state='pending'` with `next_attempt=now`.

Useful when a downstream is known-down (don't spam retries) or when investigating a burst of items.

## DSN (Delivery Status Notification)

- **Delay DSN** (RFC 3464): if item still pending after a configurable threshold (default 4h after first attempt), emit a delay DSN to sender (one per item, not per retry).
- **Failure DSN**: on permanent failure or expiry, emit bounce to sender. Includes original envelope and as much of the original headers as fits.
- **Success DSN**: on 2xx accepted, only if the submitter requested `NOTIFY=SUCCESS` (rare, per-submission).
- DSN generation itself MUST respect loop protection (REQ-FLOW-90): don't DSN a DSN.
- DSN is delivered via the normal inbound path back to the sender (or locally if the sender is local).

## MTA-STS / DANE application

Per delivery attempt:

1. Look up `_mta-sts.<domain>` TXT: get policy ID.
2. If changed from cache or missing, fetch `https://mta-sts.<domain>/.well-known/mta-sts.txt`.
3. Look up `_<mx>._tcp.<domain>` TLSA: presence indicates DANE.
4. Apply precedence (DANE > MTA-STS enforce > opportunistic TLS > plaintext):
   - DANE: must have DNSSEC-authenticated TLSA, TLS handshake cert must match. Fail = don't deliver.
   - MTA-STS enforce: TLS required, cert must match MX host name. Fail = don't deliver (retry).
   - MTA-STS testing: TLS attempted; failure scored but doesn't block delivery.
   - Neither: opportunistic TLS; plaintext fallback if TLS fails (unless operator sets global `require_tls=true`).

TLS-RPT: if the destination publishes `_smtp._tls.<domain> TXT v=TLSRPTv1; rua=…`, we collect TLS connection outcomes and send a JSON report to that `rua` (daily).

## IPv4/IPv6

- Prefer IPv6 if both available (per-destination policy overridable).
- Happy-eyeballs style fallback if IPv6 fails quickly.
- Per-IP reputation tracking for outbound (reverse: recipient MX may block one of our source IPs).

## Source IP selection

For operators with multiple outbound IPs (helpful for rate-limit distribution):

- Configured list of source IPs per IP family.
- Per-domain source IP pinning option (useful when recipient domain has rate limits on per-IP basis).
- Default: OS-chosen.

Not a v1 must-have but the queue item can carry a "source IP preference" field.

## Submission vs relay envelope rewriting

- Outbound submitted: `Return-Path:` set to sender; `Received:` line added noting submission auth.
- Forwarded/redirected: Sieve `redirect` preserves RFC 5322 `From:` but rewrites envelope MAIL FROM to a configured rewrite address (e.g. `<user+forwarded=orig@example.com>@example.org`) — avoids DMARC alignment damage by Srs-like encoding. Optional but recommended; configurable.
- Alias fanout: same SRS-ish rewriting option.

## Crash safety

Invariants:

1. A queue row is never dropped without either a successful delivery or a failure DSN (or operator-initiated delete).
2. `state='in_flight'` rows found on restart are reset to `state='pending'` with `next_attempt=now` (safe: at-least-once delivery is fine if we idempotency-key at the destination — we don't, so we accept rare duplicate deliveries on worker crash mid-DATA).
3. Blob refcount for a queued item is incremented on enqueue, decremented only on final state (done/failed with no retry path).
4. DSN generation is idempotent; duplicate on restart means the sender might get two bounces for the same failed item. Acceptable.

## Rate-limit interactions

Outbound rate limiting per destination is a **scheduler** concern, not a delivery-time concern:

- Each destination has a token bucket (refill rate + bucket size configurable).
- Scheduler only hands items to workers if the destination's bucket has tokens.
- If not, items stay `pending` with `next_attempt += delay`.

Default rates: permissive (0 rate limit = unlimited). Operators tune per destination.

## Metrics

- `queue_size{state}` — pending / in_flight / held / failed (gauge)
- `queue_oldest_seconds{state}`
- `delivery_attempts_total{status,destination_class}` — where destination_class bins into major (Gmail/Outlook/etc.) for quick ops insight
- `delivery_duration_seconds` histogram
- `delivery_tls_usage_total{policy}` — none/opportunistic/mta_sts/dane

## Administrative operations

- `herold queue list [--state pending] [--dest example.com] [--sender …]`
- `herold queue show <id>` — full detail including last error
- `herold queue retry <id|filter>` — reset to pending, next_attempt=now
- `herold queue hold <id|filter>` / `release`
- `herold queue delete <id|filter>` — with `--yes` guard
- `herold queue bounce <id>` — mark failed, send bounce, finish

## What we don't build

- Per-message priority levels beyond a single `priority` int (we don't implement SMTP MT-PRIORITY extension in v1).
- Scheduled delivery ("send tomorrow 9am") — optional phase 3.
- Email-warmup automation (ramping up new IPs).
- Per-destination rate-limit auto-tuning based on receiver feedback.
- Outbound clustering (sharding delivery across multiple sender nodes).
