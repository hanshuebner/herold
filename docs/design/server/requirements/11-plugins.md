# 11 — Plugins

Plugins extend Herold at defined points. First-class in v1. Out-of-process, JSON-RPC, language-agnostic.

## What plugins are for (v1)

Concrete use cases, ordered by priority:

1. **DNS providers** — most important. One plugin per DNS provider. Used for:
   - ACME DNS-01 challenges (`present`/`cleanup` a TXT record).
   - Automatic publication of DKIM, MTA-STS, TLSRPT, DMARC, DANE TLSA, SPF records when a domain is added / keys rotated / certs renewed.
2. **Spam classifier** — the default implementation is itself a plugin (OpenAI-compatible HTTP adapter). Operators can swap it for a different classifier.
3. **Event publishers** — forward server events (mail.received, mail.sent, mail.bounced, auth events, queue events, …) to external brokers. **NATS plugin ships with v1** as the default. Operators can add Kafka, SQS, Redis Streams, Pulsar, custom webhook destinations.
4. **Directory adapters** — auth / user stores beyond the built-in internal directory (e.g. LDAP, a proprietary HR system, a custom OAuth flow). LDAP is not built-in (out of scope v1); operators wanting LDAP write or install an LDAP directory-adapter plugin. External OIDC federation is *not* plugin-based — it's built-in.
5. **Delivery hooks** — pre-delivery or post-delivery callbacks. Use cases: external archival, SIEM ingest, custom routing decisions, virus scan via external service.

Out of v1 scope:

- Plugins that add wire protocols (e.g. "add a Gemini listener"). Protocol handlers are in-tree.
- Plugins that run inside the hot SMTP path as filters (performance-sensitive, stability-sensitive — delivery hooks cover the common need without sitting inline).
- Plugins that modify message content at delivery. (Reconsider post-v1.)

## Plugin types and contracts

Each type has a fixed JSON-RPC method set. Plugins declare their type in their manifest.

### DNS provider

Methods:
- `dns.present(zone, record_type, name, value, ttl) → {id}` — create the record; return opaque id for later cleanup.
- `dns.cleanup(id)` — delete a previously-created record.
- `dns.list(zone, record_type, name?) → [{id, value, ttl}]` — list records (for reconciliation).
- `dns.replace(zone, record_type, name, value, ttl) → {id}` — idempotent upsert.

Used by:
- `acme` module (DNS-01 challenges).
- Automatic DNS publication module (DKIM selector TXT, `_mta-sts` TXT, `_smtp._tls` TXT, `_dmarc` TXT, `_<mx>._tcp.<domain>` TLSA).

### Spam classifier

Methods:
- `spam.classify(envelope, headers, body_excerpt, auth_results, context) → {verdict, confidence, reason}`
- `spam.health() → {ok, latency_ms_p50}`

The default shipped plugin is an OpenAI-compatible HTTP adapter. An operator-written plugin could be anything — a local classifier binary, a rules engine, a call to a proprietary service.

### Directory

Methods:
- `directory.lookup(email) → {principal|null}`
- `directory.authenticate(credential) → {principal_id|null, reason?}`
- `directory.list_aliases(email) → [alias]`
- `directory.resolve_rcpt(envelope, recipient, context) → {action, reason?, code?, principal_id?, route_tag?}` — RCPT-time recipient validation, see REQ-DIR-RCPT-*. Optional method; the manifest must declare `supports: ["resolve_rcpt"]` for the server to invoke it.

Called alongside or instead of built-in directory (plugin participation in the lookup chain is configured per REQ-AUTH-13 style ordering).

The `resolve_rcpt` method exists for application-driven inbound where addresses are dynamic (e.g. `reply+ticket-12345@app.example.com`) and cannot be pre-provisioned as principals. The plugin returns one of:
- `accept` — the address is valid; the SMTP RCPT phase replies 250 and DATA proceeds. `principal_id` MAY be supplied to bind the message to an existing mailbox; if absent, the recipient is **synthetic** — the message is dispatched only to inbound webhooks (REQ-HOOK-02 `kind: "synthetic"`) and to `delivery.post`, never stored in a mailbox.
- `reject` — the SMTP RCPT phase replies 5xx; default code 5.1.1, override allowed within the 5.x.x family, `reason` becomes the SMTP text.
- `defer` — the SMTP RCPT phase replies 4xx; default code 4.5.1.
- `fallthrough` — plugin declines; the server falls back to internal-directory and catch-all rules (REQ-FLOW-10).

`route_tag` is opaque, echoed into the inbound webhook payload and the `delivery.post` event, so the application can correlate the RCPT decision with the eventual message body.

### Delivery hooks

Methods:
- `delivery.pre(envelope, headers) → {action, reason?}` where `action` ∈ {`allow`, `reject`, `defer`, `tag`}
- `delivery.post(envelope, headers, verdict, mailbox) → void` (fire-and-forget, for archival/SIEM)

`pre` is synchronous; `post` is async (server doesn't wait).

### Event publisher

Methods:
- `events.subscribe(types[], filter?) → {ack}` — called once at plugin configure; plugin declares desired event types (full list in `requirements/13-events.md`).
- `events.publish(event) → {ack}` — one event delivered per call; `ack` is plugin-side receipt, not end-to-end.
- `events.health() → {ok, backlog_ms?}` — optional backlog hint for observability.

Delivery semantics: at-most-once (REQ-EVT-10). Event payload capped at 16 KiB (REQ-EVT-04). Events are fire-and-forget from the server's perspective — the bounded in-process event channel protects the hot path.

## Requirements

### Lifecycle

- **REQ-PLUG-01** Plugins are executables (native binary or interpreted script with a shebang). Language-agnostic.
- **REQ-PLUG-02** The server launches each plugin as a child process on startup (long-running plugins) or on-demand per invocation (short-lived plugins). Lifecycle mode declared in plugin manifest.
- **REQ-PLUG-03** Long-running plugins communicate over JSON-RPC on stdin/stdout (newline-delimited, one message per line).
- **REQ-PLUG-04** On-demand plugins are invoked per-call with arguments on stdin, response on stdout, exit 0 on success.
- **REQ-PLUG-05** Server MUST restart crashed long-running plugins with exponential backoff. Repeated crashes (>5 in 5 min) put the plugin in `disabled` state; operator alerted via metric + log.
- **REQ-PLUG-06** Plugin stderr captured into the server's log stream with the plugin name as a field.

### Configuration

- **REQ-PLUG-10** Plugins declared in **system config** (not application config). Each declaration: `name`, `path`, `type` (dns | spam | directory | delivery-hook | event-publisher), `lifecycle` (long-running | on-demand), plugin-specific `options`.
- **REQ-PLUG-11** Multiple plugins of the same type allowed (e.g. DNS plugin for Cloudflare zones, another for Route53). Each is named and referenced by name.
- **REQ-PLUG-12** Application config references plugins by name (e.g. domain `example.com` has `dns_plugin = "cloudflare"`).
- **REQ-PLUG-13** SIGHUP reloads plugin manifests (starts new, stops removed, restarts changed).

### Manifest

Every plugin ships with a manifest (JSON, served via `manifest` JSON-RPC call at startup):

```json
{
  "name": "herold-dns-cloudflare",
  "version": "1.0.0",
  "abi_version": 1,
  "type": "dns",
  "lifecycle": "long-running",
  "capabilities": ["dns.present", "dns.cleanup", "dns.list", "dns.replace"],
  "options_schema": {
    "api_token": {"type": "string", "required": true, "secret": true}
  }
}
```

- **REQ-PLUG-20** Server MUST verify ABI version compatibility at startup. Incompatible plugin → plugin disabled, operator alerted.
- **REQ-PLUG-21** `options_schema` is JSON-schema-ish; server validates operator config against it before starting the plugin.
- **REQ-PLUG-22** Secrets in options are passed via env vars or a FIFO, never on command line (visible in `ps`).

### JSON-RPC

- **REQ-PLUG-30** JSON-RPC 2.0 over newline-delimited JSON on stdin/stdout. One message per line.
- **REQ-PLUG-31** Server-to-plugin methods listed per type above. Plugin-to-server: `log` (emit a log line into server's stream), `metric` (emit a metric), `notify` (raise an event).
- **REQ-PLUG-32** Timeouts: per-method defaults (DNS: 30s, spam: 5s, directory: 2s, `directory.resolve_rcpt`: 2s with a hard cap of 5s — the SMTP RCPT phase cannot wait longer, delivery-hook pre: 2s, post: 10s), configurable per-plugin.
- **REQ-PLUG-33** Request cancellation: server sends `cancel` for the given request id; plugin SHOULD stop work and respond with a `cancelled` error.
- **REQ-PLUG-34** Concurrent requests to one long-running plugin: allowed. Plugin indicates max concurrency in manifest (`max_concurrent_requests`). Server queues beyond.

### Security model

- **REQ-PLUG-40** Plugins run as a **less-privileged user** than the main server (default: own `herold-plugin` user, or unprivileged nobody-equivalent). Process isolation is the security boundary.
- **REQ-PLUG-41** Plugin filesystem access: by default, only its own directory (working dir) and `/tmp`. Server SHOULD use Linux namespaces / macOS sandbox-exec where available.
- **REQ-PLUG-42** Plugin network access: allowed by default (DNS plugins need it, spam LLM plugins need it). Per-plugin deny-list in config if operator wants to restrict.
- **REQ-PLUG-43** Plugins run under the same resource limits as the server's own workers (or tighter). Memory cap, CPU cap via cgroups / systemd slice when available.
- **REQ-PLUG-44** Plugins MUST NOT be able to read the server's application config, data directory (except via server-mediated requests), or DB directly. All state access goes through JSON-RPC.

### Observability

- **REQ-PLUG-50** Per-plugin metrics: `herold_plugin_invocations_total{plugin,method,status}`, `herold_plugin_latency_seconds{plugin,method}` histogram, `herold_plugin_restarts_total{plugin}`, `herold_plugin_state{plugin}` (up/down gauge).
- **REQ-PLUG-51** Plugin logs tagged with `plugin=<name>` in the structured log stream.
- **REQ-PLUG-52** `herold admin plugin list` shows state, version, uptime, invocation counts, recent errors.
- **REQ-PLUG-53** `herold admin plugin test <name>` runs a smoke test (plugin-type-specific: DNS test writes+reads+deletes a scratch record; spam plugin classifies a canned message; directory plugin does a test lookup).

### Distribution and discovery

- **REQ-PLUG-60** No central registry in v1. Operators install plugins by dropping executables into `<data_dir>/plugins/<name>/` and declaring them in system config.
- **REQ-PLUG-61** First-party plugins (bundled with release): **Cloudflare DNS, Route53 DNS, Hetzner Cloud DNS, manual DNS** (operator confirms each record via API/CLI), OpenAI-compat LLM spam classifier, NATS event publisher.
- **REQ-PLUG-62** Community plugins distributed out-of-tree. We publish a plugin developer guide (manifest format, JSON-RPC contract, examples).

### Stability promise

- **REQ-PLUG-70** The plugin ABI (method set, JSON-RPC schema per type) is stable across minor versions of Herold. Breaking changes bump a major ABI version; old plugins continue to work until deprecation period ends.
- **REQ-PLUG-71** Between ABI versions, both versions supported for at least one major Herold release cycle.
- **REQ-PLUG-72** Deprecation notices surfaced in `herold admin plugin list` when a loaded plugin uses a deprecated ABI.

### RCPT-time directory validation (REQ-DIR-RCPT-*)

Application-driven inbound mail (ticket systems, transactional reply intake, per-request reply-by-email correlation) routinely uses dynamic addresses that cannot be pre-provisioned as principals. The `directory.resolve_rcpt` method (above) lets a directory plugin own a whole subdomain of the address space and answer per-RCPT TO whether the address is valid, without polluting the principal model and without forcing the application to accept-then-bounce.

- **REQ-DIR-RCPT-01** A directory plugin MAY implement `directory.resolve_rcpt`. The server discovers support via the plugin manifest (`supports: ["resolve_rcpt"]`); a plugin without it is treated as `fallthrough` for every RCPT. The method coexists with the existing `directory.lookup` / `directory.authenticate` / `directory.list_aliases` methods.
- **REQ-DIR-RCPT-02** When `[smtp.inbound]` declares `directory_resolve_rcpt_plugin = "<name>"`, the inbound SMTP state machine (REQ-FLOW-10) MUST call the plugin **at RCPT TO time**, before the 250 / 4xx / 5xx reply, with the envelope and recipient. The plugin response governs the SMTP reply for that RCPT only; multi-recipient sessions invoke the plugin once per RCPT (no batching in v1).
- **REQ-DIR-RCPT-03** Resolution order at RCPT TO is:
  1. Internal directory exact match. If found → `accept` and the plugin is not called.
  2. Plugin (if configured). `accept` / `reject` / `defer` short-circuits.
  3. Plugin returns `fallthrough` (or no plugin configured) → catch-all (REQ-FLOW-10).
  4. No catch-all → 5.1.1.
  Operators MAY invert (1) and (2) per address pattern via `[smtp.inbound.plugin_first_for_domains = ["app.example.com"]]` so the plugin owns whole subdomains without per-address provisioning friction.
- **REQ-DIR-RCPT-04** Plugin call timeout is the `directory.resolve_rcpt` value of REQ-PLUG-32 (default 2s, hard cap 5s). Timeout, plugin crash, plugin `disabled` state (REQ-PLUG-05), or transport error MUST be treated as `defer` with 4.4.3 (`directory unreachable`) — NEVER as `accept`. Fail-closed is the security invariant: an outage of the application's validation service must not turn the substrate into an open relay for synthetic addresses.
- **REQ-DIR-RCPT-05** A circuit breaker MUST be applied per plugin: when error / timeout rate exceeds the configured threshold within a sliding window (default 50% over 30s with at least 20 calls), the server switches to `defer` for all subsequent RCPTs without invoking the plugin until the breaker recovers (default 60s half-open probe). State is observable as `herold_directory_plugin_breaker_state{plugin}`.
- **REQ-DIR-RCPT-06** Per-listener and per-source-IP rate limit on plugin invocations (default 50/sec/source-IP, configurable). Excess RCPTs receive 4.7.1 `try again later`. Prevents a flood of synthetic RCPTs from being amplified into a flood of HTTP calls to the application.
- **REQ-DIR-RCPT-07** An `accept` response with no `principal_id` declares the recipient **synthetic**: the message bypasses mailbox storage entirely and is dispatched only to (a) inbound webhooks whose `target` matches by domain or by an explicit `kind: "synthetic"` (REQ-HOOK-02), and (b) `delivery.post` plugins. Synthetic recipients still pass through SPF/DKIM/DMARC verification (REQ-FLOW-20..21) and the global Sieve script; they skip per-recipient Sieve (no recipient mailbox to act on), spam classification (operator may opt in via `[smtp.inbound.spam_for_synthetic = true]`), and quota accounting. The blob is reference-counted by the webhook delivery record and garbage-collected when no hook subscription remains.
- **REQ-DIR-RCPT-08** `accept` with `principal_id` MUST resolve to an existing principal; an unknown id is logged and downgraded to `defer 4.3.0`. The plugin MAY use this to route every dynamic address into a single shared service-account mailbox (a transactional intake account), in which case the message lands in IMAP/JMAP and is also visible to webhooks subscribed to that principal.
- **REQ-DIR-RCPT-09** The audit log (REQ-ADM-300) MUST record every plugin RCPT decision: timestamp, remote IP, recipient, action, code, plugin name, latency, and `route_tag` if returned. The connection-level SMTP log line includes `rcpt_decision_source = "internal" | "plugin:<name>" | "catchall"` for fast triage.
- **REQ-DIR-RCPT-10** Metrics (extending REQ-PLUG-50 and REQ-OPS-91): `herold_directory_resolve_rcpt_total{plugin,action}`, `herold_directory_resolve_rcpt_latency_seconds{plugin}` (histogram), `herold_directory_resolve_rcpt_timeouts_total{plugin}`, `herold_directory_synthetic_accepted_total{plugin}`, `herold_directory_plugin_breaker_state{plugin}` (gauge: 0=closed, 1=half-open, 2=open).
- **REQ-DIR-RCPT-11** `herold admin plugin test <name>` (REQ-PLUG-53) for a directory plugin MUST exercise `resolve_rcpt` with a canned envelope and an operator-supplied test recipient, printing the action, code, `route_tag`, and round-trip latency.
- **REQ-DIR-RCPT-12** `directory.resolve_rcpt` is an inbound-only hook. Outbound `MAIL FROM` policing remains REQ-FLOW-41 / REQ-SEND-12 (principal `allowed_from_*` lists). Submission-listener RCPT TO is **not** routed through the plugin — to do so would let one tenant's plugin observe another tenant's outbound recipients.

## Out of scope (v1)

- Plugin signing / verification / enforced publisher identity.
- A sandboxing story stronger than process-level (no seccomp filter bundled; no Wasmtime host; operators who want stronger isolation use systemd service hardening or containers).
- A plugin marketplace or registry.
- In-process plugins (cdylibs). Deliberate: the bug-containment benefit of process isolation outweighs the performance cost at our scale.
- Plugin-declared REST endpoints or UI extensions. (Plugins can implement webhooks consumed by external services, but they don't get to register HTTP routes on our admin surface.)
- Plugins that intercept or rewrite SMTP/IMAP/JMAP protocol traffic.
