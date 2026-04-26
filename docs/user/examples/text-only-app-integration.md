# Text-only application integration

Use Herold as a transactional mail substrate for a server-side
application that sends programmatic mail (notifications, receipts,
ticket replies, password resets) and receives reply mail it processes
as plain text - no MIME parsing, no HTML rendering on the application
side.

The recipe is the documented contract for REQ-DIR-RCPT-01..12,
REQ-FLOW-ATTPOL-01..02, REQ-HOOK-EXTRACTED-01..03 (see
`docs/design/requirements/`). Operators can configure against this
shape; implementation tracks the spec.

If you have not stood up Herold on a real domain yet, do
[quickstart-extended.md](../quickstart-extended.md) first - this
recipe assumes a working install with public MX and DKIM published.

## What you build

A transactional mail substrate dedicated to one application:

```
                    +------------------------------------+
                    |  app.example.com (your app)        |
                    |                                    |
     send       +-->|  POST /v1/mail/validate (RCPT     |
     +---------+|   |       hook called per RCPT TO)    |
     |         ||   |                                    |
     v         ||   |  POST /v1/mail/inbound (webhook   |
+-------+      ||   |       called per accepted msg)    |
| your  |------++   +------------------------------------+
| app   |        ^                  ^
+-------+        |                  |
     |           |                  |
     | POST /api/v1/mail/send       |
     v           |                  |
+----------------------------+      |
| herold                     |      |
|                            |      |
|  send queue ------> SMTP --+--> internet
|                            |
|  inbound SMTP <-+----------+--<-- internet
|                 |          |
|                 +- directory.resolve_rcpt plugin --> app
|                                                  |
|                 +- inbound webhook -- extracted -+
+----------------------------+
```

Two HTTP endpoints in your application:

- A **validator** the directory plugin posts to at SMTP RCPT TO time.
  Returns "yes this address is valid" / "no it is not" / "fall through".
- A **webhook receiver** that gets accepted messages as JSON with a
  pre-extracted `body.text` field, never raw RFC 5322.

Plus four pieces of Herold configuration: an API key, a plugin
declaration, an attachment policy, a webhook subscription.

## Prerequisites

- Working Herold install with `app.example.com` added as a domain and
  DKIM published (`herold admin domain add app.example.com` then
  publish the printed selector record). See
  [quickstart-extended.md](../quickstart-extended.md).
- Your application reachable over HTTPS at `app.internal` from the
  Herold host. The validator and webhook URLs in this recipe assume
  this hostname; substitute your own.
- A separate **service principal** for the application (a principal
  without a mailbox - principal-kind `service`):

  ```
  herold admin principal add app-service \
      --kind service --domain app.example.com
  ```

  All sending and inbound routing happens against this principal. Do
  not reuse a human user's principal.

## Part 1 - Sending side: API key scoped to one domain

Mint an API key bound to the service principal with the `mail.send`
scope and an `allowed_from_domains` constraint so the application can
send as `anything@app.example.com` but cannot spoof
`anything@example.com`:

```
herold admin apikey create \
    --principal app-service \
    --name app-send \
    --scope mail.send \
    --allowed-from-domains app.example.com \
    --rate-limit-per-minute 600 \
    --rate-limit-per-day    100000
```

Output is a bearer token printed once - store it in your application's
secret manager. Lost tokens are not recoverable; mint a new one.

The application now sends with:

```
POST https://mail.example.com/api/v1/mail/send
Authorization: Bearer hk_live_<token>
Content-Type: application/json

{
  "from":            "reply+ticket-12345@app.example.com",
  "to":              ["alice@example.net"],
  "subject":         "Re: ticket #12345",
  "text":            "Hi Alice,\n\nThanks for the report ...",
  "headers": {
    "In-Reply-To":   "<original-msg-id@example.net>",
    "References":    "<original-msg-id@example.net>"
  },
  "tags":            ["ticket-reply", "ticket-12345"],
  "idempotency_key": "ticket-12345-reply-7"
}
```

The `from` local-part `reply+ticket-12345` carries the correlation
identifier - replies arrive at the same address and the directory
plugin parses the ticket id back out. `allowed_from_domains` permits
any local-part under `app.example.com`, so no per-address
provisioning is needed.

`idempotency_key` lets the application retry on transport error
without sending duplicates within 24 h (REQ-SEND-16).

### Optional: smart-host pass-through for deliverability

If your host has tcp/25 outbound blocked or you already pay for a
deliverability provider, pass outbound through a smart host. Add to
`system.toml`:

```toml
[smart_host]
enabled         = true
host            = "email-smtp.us-east-1.amazonaws.com"
port            = 587
tls_mode        = "starttls"
auth_method     = "plain"
username        = "AKIAEXAMPLE"
password_env    = "AWS_SES_SMTP_PASSWORD"
fallback_policy = "smart_host_only"
```

See [system.toml.smarthost](system.toml.smarthost) for the full set
of provider recipes (SES, SendGrid, Mailgun, Gmail relay, corp MTA).

## Part 2 - Inbound RCPT validation: a directory plugin

The directory plugin owns RCPT TO for `app.example.com` and answers
each address against the application's database. Synthetic addresses
that do not correspond to a real ticket are refused at the SMTP RCPT
phase with 5.1.1 - the sending MTA never queues them, no bounce
traffic is generated on either side.

### Plugin manifest

Save to `/var/lib/herold/plugins/app-rcpt/manifest.json`:

```json
{
  "name":        "app-rcpt",
  "version":     "1.0.0",
  "abi_version": 1,
  "type":        "directory",
  "lifecycle":   "long-running",
  "supports":    ["resolve_rcpt"],
  "options_schema": {
    "validator_url":   {"type": "string", "required": true},
    "validator_token": {"type": "string", "required": true, "secret": true}
  }
}
```

The `supports: ["resolve_rcpt"]` declaration is what tells Herold to
invoke the new method (REQ-DIR-RCPT-01).

### Plugin implementation (Python, ~40 lines)

Save to `/var/lib/herold/plugins/app-rcpt/app-rcpt`, `chmod +x`:

```python
#!/usr/bin/env python3
"""Directory plugin: RCPT-time validator that calls back to the app."""
import json, os, sys, urllib.request, urllib.error

VALIDATOR_URL   = os.environ["HEROLD_OPT_VALIDATOR_URL"]
VALIDATOR_TOKEN = os.environ["HEROLD_OPT_VALIDATOR_TOKEN"]

def respond(req_id, result=None, error=None):
    msg = {"jsonrpc": "2.0", "id": req_id}
    if error: msg["error"] = error
    else:     msg["result"] = result
    sys.stdout.write(json.dumps(msg) + "\n"); sys.stdout.flush()

def resolve_rcpt(params):
    recipient = params["recipient"]
    body = json.dumps({"recipient": recipient,
                       "envelope":  params["envelope"]}).encode()
    req = urllib.request.Request(
        VALIDATOR_URL, data=body, method="POST",
        headers={"Content-Type":  "application/json",
                 "Authorization": f"Bearer {VALIDATOR_TOKEN}"})
    try:
        with urllib.request.urlopen(req, timeout=1.5) as r:
            return json.loads(r.read())
    except urllib.error.HTTPError as e:
        if e.code == 404: return {"action": "reject", "code": "5.1.1",
                                  "reason": "no such recipient"}
        raise   # any other HTTP error -> JSON-RPC error -> defer 4.4.3

for line in sys.stdin:
    msg = json.loads(line)
    rid, method, params = msg.get("id"), msg.get("method"), msg.get("params", {})
    try:
        if method == "directory.resolve_rcpt":
            respond(rid, resolve_rcpt(params))
        elif method == "manifest":
            respond(rid, {"name": "app-rcpt", "version": "1.0.0"})
        else:
            respond(rid, error={"code": -32601, "message": "method not found"})
    except Exception as e:
        respond(rid, error={"code": -32000, "message": str(e)})
```

This is the simplest possible long-running plugin. Production code
would batch / pool HTTP connections and emit `metric` notifications
back to Herold; this is enough to verify the wire shape.

### Application validator endpoint

The application implements `POST /v1/mail/validate`:

```python
@app.post("/v1/mail/validate")
def validate(req):
    body      = req.json()
    recipient = body["recipient"]            # "reply+ticket-12345@app.example.com"
    local, _  = recipient.split("@", 1)
    if not local.startswith("reply+"):
        return {"action": "reject", "code": "5.1.1",
                "reason": "this address does not accept mail"}, 200
    ticket_id = local.removeprefix("reply+")
    ticket    = db.tickets.get(ticket_id)
    if ticket is None:
        return {"action": "reject", "code": "5.1.1",
                "reason": f"unknown ticket {ticket_id}"}, 200
    if ticket.closed:
        return {"action": "reject", "code": "5.7.1",
                "reason": "ticket closed; replies not accepted"}, 200
    return {"action":    "accept",
            "route_tag": f"ticket:{ticket_id}"}, 200
```

The four legal `action` values per REQ-DIR-RCPT-07:

- `accept` with no `principal_id` - synthetic recipient, message
  fires only the inbound webhook, never lands in a mailbox.
- `accept` with `principal_id` - bind to a real mailbox (e.g. an
  archival service-account principal) so the message is also IMAP /
  JMAP retrievable.
- `reject` - 5xx in band; sender's MTA generates the bounce.
- `defer` - 4xx; sender's MTA retries.
- `fallthrough` - the plugin declines; Herold falls back to internal
  directory and catch-all (REQ-DIR-RCPT-03).

The `route_tag` is opaque to Herold; it round-trips into the inbound
webhook payload so the application correlates the RCPT decision with
the eventual message body.

### Wire it into system.toml

```toml
[[plugin]]
name      = "app-rcpt"
path      = "/var/lib/herold/plugins/app-rcpt/app-rcpt"
type      = "directory"
lifecycle = "long-running"

  [plugin.options]
  validator_url   = "https://app.internal/v1/mail/validate"
  validator_token = "${env:APP_VALIDATOR_TOKEN}"

[smtp.inbound]
directory_resolve_rcpt_plugin = "app-rcpt"
plugin_first_for_domains      = ["app.example.com"]
```

`plugin_first_for_domains` (REQ-DIR-RCPT-03) means RCPT TOs in
`app.example.com` go to the plugin BEFORE internal-directory lookup
- the application owns the whole subdomain. RCPT TOs for other
domains still use internal directory first.

Reload: `herold admin reload` (or `kill -HUP <pid>`).

### Smoke test

```
$ herold admin plugin test app-rcpt \
      --recipient reply+ticket-12345@app.example.com
plugin:    app-rcpt
method:    directory.resolve_rcpt
action:    accept
code:      -
route_tag: ticket:12345
latency:   42ms

$ herold admin plugin test app-rcpt \
      --recipient reply+ticket-99999@app.example.com
plugin:    app-rcpt
method:    directory.resolve_rcpt
action:    reject
code:      5.1.1
reason:    unknown ticket 99999
latency:   38ms
```

If you see `action: defer` `reason: directory unreachable`, the
validator URL is wrong, the network path is broken, or the
application is returning non-2xx. Check the plugin's stderr in the
Herold log stream (`herold admin log tail --plugin app-rcpt`).

## Part 3 - Refuse attachments at the SMTP DATA phase

Transactional reply intake almost never wants attachments - they
balloon storage, complicate parsing, and a malicious sender can use
them as an exfiltration channel. Refuse them in the SMTP DATA phase
so the sending MTA never queues the message:

```
herold admin domain set app.example.com \
    --inbound-attachment-policy reject_at_data
```

Per REQ-FLOW-ATTPOL-01, Herold inspects the parsed top-level MIME
structure after DATA and before 250 OK. A message with attachments
gets:

```
552 5.3.4 attachments not accepted on this address
```

The 5.3.4 enhanced status is "message too big for system" - close
enough; some receivers map it to "policy refusal." You can override
the text within the 5.x.x family if a different code reads better
for your application.

Defense in depth: nested MIME (`multipart/alternative` hiding an
attachment under one branch) is caught after acceptance by the FTS
extractor's MIME walker (REQ-FLOW-ATTPOL-02). The message is not
delivered; a bounce is generated to the sender; the audit log
records `attpol_outcome = "refused_post_acceptance"`.

### Smoke test

From an MTA you control (e.g. swaks):

```
$ swaks --to reply+ticket-12345@app.example.com \
        --from tester@example.net \
        --server mail.example.com \
        --attach-type application/pdf \
        --attach @/dev/null
...
<-  552 5.3.4 attachments not accepted on this address
```

Compare with the same command without `--attach`:

```
<-  250 2.0.0 OK
```

## Part 4 - Webhook: extracted text, never raw MIME

Subscribe to synthetic-recipient deliveries on `app.example.com`.
The `extracted` body mode (REQ-HOOK-EXTRACTED-01) means the
application gets pre-extracted plain text in the JSON payload
regardless of message size; it never has to handle raw RFC 5322:

```
herold admin hook create \
    --name           app-inbound \
    --target-kind    synthetic \
    --target-value   app.example.com \
    --url            https://app.internal/v1/mail/inbound \
    --secret         "$(openssl rand -base64 32)" \
    --body-mode      extracted \
    --extracted-text-max-bytes 5242880 \
    --text-required \
    --filter         '{"verdict_in": ["ham", "suspect"]}'
```

Save the printed secret in the application's secret manager - it is
the HMAC key for verifying incoming webhook deliveries.

The application receives `POST /v1/mail/inbound`:

```json
{
  "event":      "mail.received",
  "timestamp":  "2026-04-26T14:02:33.812Z",
  "message_id": "<CAEx...@mail.example.net>",
  "from":       "alice@example.net",
  "rcpt_to":    ["reply+ticket-12345@app.example.com"],
  "envelope": {
    "mail_from": "alice@example.net",
    "rcpt_to":   ["reply+ticket-12345@app.example.com"]
  },
  "subject":    "Re: ticket #12345",
  "headers": {
    "From":        "Alice <alice@example.net>",
    "Date":        "Sun, 26 Apr 2026 14:02:30 +0000",
    "In-Reply-To": "<7d3a...@app.example.com>",
    "References":  "<7d3a...@app.example.com>"
  },
  "auth": {
    "spf":   "pass",
    "dkim":  "pass",
    "dmarc": "pass",
    "arc":   "none"
  },
  "spam":       { "verdict": "ham", "confidence": 0.98 },
  "route_tag":  "ticket:12345",
  "body": {
    "text":        "Hi,\n\nThe printer is working again, thanks!\n\nAlice\n\n> On 2026-04-26 ...",
    "text_origin": "native",
    "text_truncated": false
  },
  "attachments": []
}
```

The fields the application cares about, in order:

- `route_tag` - the value the validator returned for this RCPT.
  Look up ticket 12345 directly; no need to re-parse the recipient.
- `body.text` - plain text, ready to use. Never empty when
  `text_required = true` (REQ-HOOK-EXTRACTED-03); messages with no
  extractable text are dropped server-side and never reach this hook.
- `body.text_origin` - `native` if the message had a `text/plain`
  part, `derived_from_html` if Herold rendered it from `text/html`
  (the same extractor the FTS pipeline uses). `none` is impossible
  here because of `text_required = true`.
- `body.text_truncated` - `true` only if the extracted text exceeded
  `extracted_text_max_bytes` (5 MB above). Application can fetch the
  full raw `message/rfc822` from the signed `body.fetch_url` if it
  ever cares; for ticket replies, ignore.
- `auth.dmarc` - if `pass`, the From address is trustworthy. The
  application should refuse to act on `fail` for any privileged
  ticket operation.

Verify the HMAC on every delivery (REQ-HOOK-05). The header is
`X-Herold-Signature: t=<unix-ts>,v1=<hex-sha256>`; the signing
string is `<unix-ts>.<raw-body>`.

### Smoke test

```
$ herold admin hook test app-inbound
delivering canned message to https://app.internal/v1/mail/inbound
status:        200
latency:       87ms
response body: {"ok":true,"ticket":"test-fixture"}

$ herold admin hook log --name app-inbound --since 5m
14:02:34  POST /v1/mail/inbound  200  87ms   {"ok":true,...}
14:01:12  POST /v1/mail/inbound  200  64ms   {"ok":true,...}
```

## Part 5 - Verify it is working: audit log and metrics

### Audit log

Every plugin RCPT decision and every attachment-policy outcome is in
the audit log (REQ-DIR-RCPT-09, REQ-FLOW-ATTPOL-02, REQ-ADM-300).
Tail it during the first day of integration:

```
$ herold admin audit tail --filter rcpt_decision_source=plugin:app-rcpt
2026-04-26T14:02:30Z  rcpt  app-rcpt  reply+ticket-12345@app.example.com  accept  route_tag=ticket:12345  41ms
2026-04-26T14:02:31Z  rcpt  app-rcpt  reply+ticket-99999@app.example.com  reject  5.1.1                   38ms
2026-04-26T14:02:35Z  rcpt  app-rcpt  noise@app.example.com               reject  5.1.1                   35ms

$ herold admin audit tail --filter attpol_outcome
2026-04-26T14:02:31Z  attpol  reply+ticket-12345@app.example.com  refused_at_data
2026-04-26T14:02:33Z  attpol  reply+ticket-12345@app.example.com  passed
```

`outcome=dropped_no_text` from REQ-HOOK-EXTRACTED-03 also appears
here; if you ever see one in production, an upstream sender is
shipping body-less mail and the application should know.

### Metrics

Scrape `/metrics` and watch:

```
# RCPT-time validation
herold_directory_resolve_rcpt_total{plugin="app-rcpt", action="accept"}        12473
herold_directory_resolve_rcpt_total{plugin="app-rcpt", action="reject"}        1842
herold_directory_resolve_rcpt_total{plugin="app-rcpt", action="defer"}         3
herold_directory_resolve_rcpt_latency_seconds{plugin="app-rcpt", quantile="0.99"}  0.12
herold_directory_resolve_rcpt_timeouts_total{plugin="app-rcpt"}                0
herold_directory_synthetic_accepted_total{plugin="app-rcpt"}                   12473
herold_directory_plugin_breaker_state{plugin="app-rcpt"}                       0

# Attachment policy
herold_inbound_attachment_policy_total{recipient_domain="app.example.com", outcome="refused_at_data"}        184
herold_inbound_attachment_policy_total{recipient_domain="app.example.com", outcome="refused_post_acceptance"}  2
herold_inbound_attachment_policy_total{recipient_domain="app.example.com", outcome="passed"}                12289

# Webhook delivery
herold_hook_deliveries_total{name="app-inbound", status="2xx"}              12289
herold_hook_deliveries_total{name="app-inbound", status="dropped_no_text"}  0
herold_hook_latency_seconds{name="app-inbound", quantile="0.99"}            0.21
herold_hook_in_flight{name="app-inbound"}                                   2

# Send side
herold_send_total{tag="ticket-reply", status="queued"}    14201
herold_smtp_outbound_total{outcome="delivered"}           14132
herold_smtp_outbound_total{outcome="bounced_5xx"}         44
```

The two alerts worth setting up on day one:

- `herold_directory_plugin_breaker_state{plugin="app-rcpt"} > 0` for
  more than 60 s. The validator endpoint is failing; new RCPT TOs
  are being deferred. Check the application before the senders give
  up retrying.
- `herold_directory_resolve_rcpt_latency_seconds{plugin="app-rcpt",
  quantile="0.99"} > 1.0`. The validator is slow; SMTP peers will
  time out at 30 s but Herold's hard cap is 5 s (REQ-PLUG-32).

## End-to-end smoke test

Three commands from a host that can reach `mail.example.com:25`:

```
# Should be accepted, message lands at the application's webhook.
swaks --to   reply+ticket-12345@app.example.com \
      --from tester@example.net \
      --server mail.example.com \
      --body  "This reply should reach the app."

# Should be refused at RCPT TO with 5.1.1.
swaks --to   reply+ticket-99999@app.example.com \
      --from tester@example.net \
      --server mail.example.com

# Should be refused at DATA with 5.3.4.
swaks --to   reply+ticket-12345@app.example.com \
      --from tester@example.net \
      --server mail.example.com \
      --attach-type application/pdf \
      --attach @/dev/null
```

Then verify in the application's logs that the webhook fired exactly
once, with `route_tag=ticket:12345`, `body.text_origin=native`, and
no attachments listed.

## Common gotchas

- **Validator latency.** The plugin's HTTP call to your app blocks
  the SMTP peer's RCPT TO. Keep the validator under 200 ms p99 or
  senders will see slow handshakes. Cache hot tickets in the
  validator's process memory; do not hit the database on every call.
- **Validator idempotency.** A peer may issue the same RCPT TO twice
  in one session (rare, but legal). The validator must be safe to
  call repeatedly with the same inputs.
- **DKIM on outbound from a subdomain.** `app.example.com` needs its
  own DKIM selector record published. `herold admin domain add
  app.example.com` prints the record; do not skip publishing it - a
  missing DKIM signature on transactional mail tanks deliverability
  fast.
- **DMARC alignment.** The `From:` header domain must match the
  DKIM-signing domain for DMARC `aligned` pass. Sending as
  `reply+ticket-12345@app.example.com` requires the DKIM key to be
  on `app.example.com`, not on `example.com`.
- **`text_required` drops are silent to the sender.** A message that
  reaches the webhook stage with no extractable text is dropped
  server-side; the sender's peer already got 250 OK back at the SMTP
  layer. The audit log is the only signal. If your application
  expects "always reach the webhook or always bounce", do not enable
  `text_required` - handle empty bodies in the application instead.
- **Plugin-first ownership is whole-domain.** With
  `plugin_first_for_domains = ["app.example.com"]`, internal
  principals on `app.example.com` are NOT consulted before the
  plugin. If you need a real human mailbox on the same domain, host
  it on a sibling domain or have the plugin return `fallthrough` for
  that local-part.
- **Plugin fail-closed.** If the validator endpoint is down,
  Herold returns `4.4.3 directory unreachable` for every RCPT TO,
  not 5xx. Senders retry on the standard retry schedule. This is
  correct behavior - an outage of your validator must not silently
  start accepting unknown addresses. Make sure your monitoring
  catches the breaker-open metric quickly.

## See also

- [system.toml.smarthost](system.toml.smarthost) - smart-host
  recipes for SES, SendGrid, Mailgun, Gmail relay, corp MTA.
- [../administer.md](../administer.md) - principal, domain, alias,
  API key, hook CRUD reference.
- [../operate.md](../operate.md) - audit log, metrics, alerting.
- `docs/design/requirements/11-plugins.md` REQ-DIR-RCPT-* - the
  plugin contract this recipe builds on.
- `docs/design/requirements/12-http-mail-api.md` REQ-HOOK-* and
  REQ-SEND-* - the HTTP send and webhook contracts.
- `docs/design/requirements/03-mail-flow.md` REQ-FLOW-ATTPOL-* -
  the attachment-policy contract.
