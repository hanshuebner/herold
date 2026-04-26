# 13 — Events

Server-wide event stream published to external buses via `event-publisher` plugins. Distinct from mail-arrival webhooks (those deliver *mail content*; events are a *broader observability/automation stream*).

## Model

The server generates typed events. Event-publisher plugins subscribe to a filter and forward to whatever backend they implement. One server-side event-dispatcher; many plugins; many downstream buses.

```
internal events ──► event-dispatcher ──► plugin A (NATS)
                                  │
                                  ├──► plugin B (Kafka)
                                  │
                                  └──► plugin C (webhook → Slack)
```

## Event types (v1)

| Type | When | Payload fields |
|---|---|---|
| `mail.received` | Inbound mail accepted and delivered to ≥1 local mailbox | msg_id, rcpt, sender, verdict, size |
| `mail.sent` | Outbound mail accepted for delivery (queue insert) | queue_id, sender, rcpt, via (smtp/api) |
| `mail.delivered` | Remote 2xx ack on an outbound attempt | queue_id, rcpt, duration_ms, relay_host |
| `mail.deferred` | 4xx or error on an outbound attempt | queue_id, rcpt, attempt, reason, next_at |
| `mail.bounced` | Permanent failure (5xx) or queue expiry | queue_id, rcpt, final_reason, attempts |
| `mail.rejected` | Inbound rejected at SMTP (DMARC, quota, unknown rcpt, etc.) | stage, reason, sender, rcpt, remote_ip |
| `principal.auth.success` | Auth success on any protocol | principal_id, protocol, source_ip |
| `principal.auth.failure` | Auth failure on any protocol | principal_hint, protocol, source_ip, reason |
| `principal.created` / `principal.deleted` / `principal.updated` | Admin mutation | principal_id, changed_fields |
| `domain.created` / `domain.deleted` | Admin mutation | domain |
| `dkim.rotated` | DKIM key rotation | domain, new_selector, old_selector |
| `cert.issued` / `cert.renewed` / `cert.renew_failed` | ACME | hostname, expires_at |
| `queue.expired` | Queue entry hit expiry | queue_id, rcpt, reason |
| `plugin.restarted` / `plugin.failed` | Plugin supervisor | plugin_name, cause |
| `system.started` / `system.stopped` / `system.reloaded` | Lifecycle | version, uptime_seconds |

- **REQ-EVT-01** The event type set is closed (additions come with minor-version bumps). Plugins declare which types they want; the dispatcher filters server-side.
- **REQ-EVT-02** Every event carries a common envelope: `event_id` (UUIDv7), `type`, `timestamp` (RFC 3339), `server_hostname`, `schema_version`, plus the type-specific payload.
- **REQ-EVT-03** Payloads MUST NOT include sensitive secrets (passwords, full message bodies, API keys). For mail events, reference IDs; a separate call to the admin API fetches the body if authorized.
- **REQ-EVT-04** Payloads are bounded in size (default 16 KiB). Exceeded → fields truncated with a marker.

## Delivery guarantees

- **REQ-EVT-10** **At-most-once** in v1. The event dispatcher emits to plugins once; if a plugin is temporarily unable to receive, the event may be dropped. Operators requiring at-least-once rely on the plugin's own buffering (NATS JetStream, Kafka producer buffer).
- **REQ-EVT-11** Event ordering per source: events for the same principal are dispatched in arrival order. Cross-principal ordering not guaranteed.
- **REQ-EVT-12** Plugin-side backpressure: if a plugin's in-flight queue fills (bounded; default 1000 events), new events are dropped with a counter incremented. Log warning at >50% fill.
- **REQ-EVT-13** Dispatcher never blocks the server's hot path. Event emit is fire-and-forget via a bounded in-process channel.

## Plugin contract

Event-publisher plugins implement:

- `events.subscribe(types) → {ack}` — declare which types this plugin wants (called at configure time).
- `events.publish(event) → {ack}` — receive an event. Response acknowledges receipt on the plugin side (not end-to-end delivery).
- `events.health() → {ok, lag_ms}` — reports plugin-side liveness and approximate backlog.

Details in `architecture/07-plugin-architecture.md`.

## Configuration

- **REQ-EVT-20** Plugin declared in system config (REQ-PLUG-10).
- **REQ-EVT-21** Event filters declared in application config per-plugin-instance: types + optional tag filters (e.g. "only events for domain=example.com").
- **REQ-EVT-22** Multiple plugins may subscribe to the same event types; each gets an independent copy.

## Default plugin: NATS

- **REQ-EVT-30** Ships with v1. Publishes to a NATS subject derived from event type:
  - `herold.mail.received.<domain>`
  - `herold.mail.sent.<domain>`
  - `herold.principal.auth.<result>`
  - etc.
- **REQ-EVT-31** Payload: JSON, schema as above.
- **REQ-EVT-32** Options: server URL(s), credentials (nats-context or user/pass or nkey), subject prefix override, optional JetStream stream binding.
- **REQ-EVT-33** Uses NATS built-in reconnection / buffering — drops events only if NATS-side buffer fills, reported via plugin-level metric.

## Examples of use

- Feed an SIEM: run a plugin that writes events to syslog / CEF / a SIEM-specific format.
- Trigger alerting: NATS or webhook plugin consumed by a Slack alert bot.
- Observability pipeline: plugin publishes to Kafka → analytics.
- Reputation service: receive bounce events to feed a sender-reputation model.
- Tenant dashboards (in a custom wrapper): consume NATS subjects scoped by domain.

## What events are NOT

- **Not webhook deliveries of mail content.** Use `/api/v1/hooks` (REQ-HOOK-*) for mail-arrival webhooks with body access. Events reference mail; webhooks deliver mail.
- **Not metrics.** Use Prometheus for counters/gauges/histograms. Events are transitions; metrics are samples.
- **Not audit log.** Audit log (REQ-ADM-300) is an append-only store of admin actions; events are an ephemeral stream. Audit entries may produce events, but the audit log is canonical.
- **Not log lines.** Logs are human-readable + grep-able. Events are machine-readable with schemas.

## Out of scope (v1)

- At-least-once delivery from server to plugin.
- Event replay / server-side persistence of events.
- Schema registry / versioned schema evolution (we version schema per minor release; breaking changes bump major).
- Signed events (HMAC) — trust boundary ends at the plugin; plugins sign downstream if their consumer requires it.
- Event sourcing / rebuilding state from events.
