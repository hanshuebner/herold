# 10 — Client-log pipeline

How browser-side errors, logs, and Web Vitals from the herold-served SPAs (Suite on the public listener, Admin on the admin listener) are received, sanitised, fanned out to the existing observability pipeline, and surfaced back to operators. Pairs with REQ-OPS-200..220 in `requirements/09-operations.md` and REQ-ADM-23/230..233 in `requirements/08-admin-and-management.md`.

## Where it lives

```
internal/
  protoadmin/
    clientlog.go            # both endpoints' handlers; admin REST surface
    clientlog_pipeline.go   # validate -> sanitise -> enrich -> fan-out
    clientlog_reorder.go    # per-session reorder buffer for console sinks
    clientlog_ringbuf.go    # ring-buffer reader/writer (delegates to store)
  protojmap/
    clientlog_meta.go       # injects livetail_until + clientlog descriptor
                            # into the JMAP session response (REQ-OPS-211, REQ-CLOG-12)
  observe/
    client_emitter.go       # adapter that re-emits a client event as slog +
                            # OTLP log records
    console_client.go       # console formatter shim that consults the
                            # reorder buffer for source=client records
  store/
    schema/clientlog.sql    # ring-buffer table + indexes
    repo/clientlog.go       # typed repo: append, read by cursor, evict
```

The handler is registered on every listener that serves an SPA — for v1 that is the public listener (Suite) and the admin listener (Admin SPA). Both listeners mount the same handler with a `listener` tag so downstream records carry the origin.

## Endpoint mounting

| Path | Listener | Auth | Schema | Listener tag |
|---|---|---|---|---|
| `POST /api/v1/clientlog` | public | session cookie | full (REQ-OPS-202) | `public` |
| `POST /api/v1/clientlog/public` | public | none | narrow (REQ-OPS-207) | `public` |
| `POST /api/v1/clientlog` | admin | admin session cookie | full | `admin` |
| `POST /api/v1/clientlog/public` | admin | none | narrow | `admin` |
| `GET /api/v1/admin/clientlog` (and 230..233) | admin only | admin session | n/a | n/a |

The admin REST surface (REQ-ADM-23) is admin-listener-only — the public listener returns 404 for `/api/v1/admin/*` per REQ-OPS-ADMIN-LISTENER-01.

## Pipeline

```
HTTP body                         (already-validated JSON, ≤ body cap)
  │
  ▼
Validate strictly                 reject unknown fields on public endpoint
  │                               truncate oversize fields with _truncated marker
  ▼
Redact (REQ-OPS-84)               same handler chain that protects server logs
  │                               runs per event, never bypassable
  ▼
Enrich                            server_recv_ts, clock_skew_ms, user_id,
  │                               client_ip (truncated), listener, endpoint
  ▼
Fan-out
  ├── slog.Logger.LogAttrs        source=client, activity per REQ-OPS-204
  │      │
  │      ▼
  │   sink chain (REQ-OPS-80)     console sink threads through reorder buffer
  │                               JSON sinks consume in arrival order
  │
  ├── OTLP log exporter            (REQ-OPS-205) when endpoint is configured
  │   (otlploghttp)               anonymous events gated by clientlog.public.otlp_egress
  │
  └── ring buffer                 store.AppendClientLog(slice, row)
```

Fan-out is best-effort: a slow OTLP exporter must not block ingest, and a transient ring-buffer write failure must not drop the slog emission. Each fan-out target is independent; failures are counted in `herold_clientlog_dropped_total{reason}`.

## Concurrency

Each ingest request runs on the HTTP server's worker goroutine. The handler:

1. Reads up to the body cap with `io.LimitReader`.
2. Decodes and validates synchronously.
3. Submits each event to a buffered channel that feeds a small worker pool (default 4 workers) for fan-out.
4. Acks the request (200 with empty body) once the events are queued — not once they have been emitted.

If the queue is full, the handler responds 503 with a `Retry-After` so the SPA can back off. No event is held in the request goroutine after ack.

The reorder buffer (REQ-OPS-210) is owned by a single goroutine per console sink. It maintains a min-heap keyed by `(session_id, client_ts + clock_skew_ms)` plus a per-session deadline timer. On heap insertion it sets the deadline at `client_ts + reorder_window_ms`; the goroutine sleeps until the next deadline, then drains everything ≤ now. Late arrivals (events whose deadline has already passed for that session) bypass the heap and emit immediately with `late=true`.

## Ring buffer

Schema lives in `store/schema/clientlog.sql` with both SQLite and Postgres flavours (REQ-STD: dual-backend).

```sql
CREATE TABLE clientlog (
  id            INTEGER PRIMARY KEY,            -- AUTOINCREMENT in sqlite, BIGSERIAL in pg
  slice         TEXT NOT NULL,                  -- 'auth' | 'public'
  server_ts     TIMESTAMPTZ NOT NULL,           -- arrival time on server
  client_ts     TIMESTAMPTZ NOT NULL,           -- as reported by browser
  clock_skew_ms BIGINT NOT NULL,                -- server_ts - client_ts (signed)
  app           TEXT NOT NULL,                  -- 'suite' | 'admin'
  kind          TEXT NOT NULL,                  -- 'error' | 'log' | 'vital'
  level         TEXT NOT NULL,
  user_id       TEXT,                           -- null for slice='public'
  session_id    TEXT,
  page_id       TEXT NOT NULL,
  request_id    TEXT,
  route         TEXT,
  build_sha     TEXT NOT NULL,
  ua            TEXT NOT NULL,
  msg           TEXT NOT NULL,
  stack         TEXT,
  payload_json  TEXT NOT NULL                   -- full enriched record for replay
);

CREATE INDEX clientlog_slice_id            ON clientlog(slice, id DESC);
CREATE INDEX clientlog_request_id          ON clientlog(request_id) WHERE request_id IS NOT NULL;
CREATE INDEX clientlog_session_client_ts   ON clientlog(session_id, client_ts) WHERE session_id IS NOT NULL;
CREATE INDEX clientlog_user_server_ts      ON clientlog(user_id, server_ts DESC) WHERE user_id IS NOT NULL;
```

Eviction runs in a background goroutine started at boot:

- Wakes every 60 s.
- For each slice, deletes rows where `server_ts < now() - ring_buffer_age` OR `id <= max(id) - ring_buffer_rows`.
- Bounded by row count: a single eviction pass deletes at most `eviction_batch` (default 1000) rows to avoid lock pressure on SQLite.

The `id DESC` order is the canonical pagination order for `GET /api/v1/admin/clientlog` (REQ-ADM-230). Cursors are opaque base64 of `(slice, id)`; resuming a paginated read tolerates concurrent eviction because IDs are monotonic.

## Cross-source correlation (REQ-OPS-213)

`X-Request-Id` is generated client-side as a UUID v7 and attached to every JMAP/admin/chat fetch (REQ-CLOG-20). Server middleware in `internal/protojmap` and `internal/protoadmin` reads or mints the header, places the value on the request `context.Context`, and emits it as the `request_id` slog attribute on every server log line for that request. Client events emitted while a request is in flight carry the same id.

The timeline endpoint (`GET /api/v1/admin/clientlog/timeline?request_id=…`, REQ-ADM-231) does:

1. Read all server log records with `request_id=X` from the existing log JSON sink (REQ-OPS-81 file target). When no JSON file sink is configured the endpoint returns 422 with a hint.
2. Read all client-log rows from the ring buffer with `request_id=X`.
3. Merge, sort by `coalesce(client_ts + clock_skew_ms, server_ts)`, return as a JSON array.

The endpoint does NOT read from OTLP — the operator's collector is the canonical store for shipped logs; the timeline view is a "what's still on this server" view.

## Live-tail (REQ-OPS-211, REQ-ADM-232)

Each authenticated session row carries an optional `clientlog_livetail_until TIMESTAMPTZ` column. The flow:

1. Operator (with admin scope on the admin listener) calls `POST /api/v1/admin/clientlog/livetail` with `{session_id, duration}`.
2. Handler clamps `duration` to `livetail_max_duration` (REQ-OPS-219), writes `clientlog_livetail_until = now() + duration` on the target session, audit-logs the action.
3. The JMAP session-meta middleware (`internal/protojmap/clientlog_meta.go`) reads the session row on every JMAP response and includes `clientlog.livetail_until` in the session descriptor when non-zero.
4. The SPA wrapper observes the change and switches to synchronous emission (REQ-CLOG-05) until the timestamp passes.
5. A background sweeper clears expired `clientlog_livetail_until` values (cosmetic — the comparison is done at read time anyway).

`DELETE /api/v1/admin/clientlog/livetail/{session_id}` writes `null`. Cancelling is also audit-logged.

## Per-user opt-out (REQ-OPS-208, REQ-CLOG-06)

Stored as `clientlog_telemetry_enabled BOOLEAN` on the principal row, with NULL meaning "use the system default from `clientlog.defaults.telemetry_enabled`". Default value is computed at session creation and embedded in the session descriptor as `clientlog.telemetry_enabled` so the SPA does not need a round trip to read it. Changes via the user-self-service `/settings` API write the principal column and bump the session descriptor on next response.

Errors (`kind=error`) bypass this flag at the SPA — they are always sent. Server-side, when an authenticated event arrives with `kind != error` and the session's effective telemetry flag is `false`, the event is silently dropped and counted in `herold_clientlog_dropped_total{reason="telemetry_disabled"}`. This is defence-in-depth: a buggy or out-of-date SPA build cannot leak telemetry against the user's wish.

## Console rendering of client records

The console handler in `internal/observe` recognises `source=client` records and:

- Routes them through the reorder buffer keyed by `session_id`.
- On TTY targets, renders them with a leading marker (`[suite]` / `[admin]` in a distinct colour) so an operator scanning the console can distinguish browser events from server events at a glance.
- Indents multi-line `stack` fields under the parent line, capped at the console width.
- Suppresses `kind=log` records below the per-source level threshold (`clientlog.console_level`, default `warn`); errors and Web Vitals are unaffected by this throttle.

Non-TTY (file/JSON) sinks bypass the reorder buffer entirely. They get the raw arrival-ordered stream so external log-shipping tools can apply their own sorting on `client_ts`.

## OTLP shape

Each client event becomes one OTLP log record (`otlploghttp` exporter, the same connection already used for traces in `internal/observe/trace.go`). Resource attributes:

```
service.name              = herold-suite | herold-admin
service.version           = <build_sha>
deployment.environment    = <operator-config; default "production">
service.instance.id       = <hostname>
```

Record body: `msg`. Severity: maps `level` to OTLP severity numbers. Attributes:

```
client.session_id         (string)
client.page_id            (string)
client.route              (string)
client.ua                 (string)
client.kind               error | log | vital
client.build_sha          (string)
client.client_ts          (RFC3339)
client.clock_skew_ms      (int)
client.endpoint           auth | public
client.listener           public | admin
user.id                   (string; auth slice only)
request_id                (string; when correlated)
exception.type            (string; for kind=error, parsed best-effort from msg)
exception.stacktrace      (string; raw, unsymbolicated)
```

Anonymous events are not exported unless `clientlog.public.otlp_egress = true` (REQ-OPS-205, REQ-OPS-217). When export is off, anonymous events still land in slog and the public-slice ring buffer.

## Failure modes

| Condition | Behaviour |
|---|---|
| Body > cap | 413, count `body_too_large` |
| Schema invalid (extra field on public, type mismatch anywhere) | 400, count `schema` |
| Per-session quota exceeded (auth) | 429 with `Retry-After`, count `rate_limit` |
| Per-IP quota exceeded (public) | 200 empty body (silent drop), count `rate_limit` |
| Field allowlist hit | drop the offending field, keep the event, count `field_allowlist` |
| Telemetry-disabled non-error event | silent drop, count `telemetry_disabled` |
| Fan-out worker queue full | 503 with `Retry-After`, count `backpressure` |
| Ring-buffer write fails | log `internal` error, continue with slog + OTLP fan-out, count `ringbuf_write_failed` |
| OTLP exporter slow / failing | drop on the exporter's own retry policy, count `otlp_dropped` |

## Test surfaces

- **Unit**: schema validation table, redaction table (each REQ-OPS-84 class), enrichment correctness, reorder-buffer property test (any input order produces output sorted by effective time within the window), eviction batching.
- **Integration**: full HTTP round-trip on both endpoints with a fake SPA payload; assert the resulting slog records, ring-buffer rows, and OTLP records (recorded via an in-test OTLP collector).
- **Both backends**: ring-buffer schema, eviction, and pagination tested under SQLite and Postgres.
- **Conformance / fuzz**: `internal/protoadmin/clientlog.go` parser is fuzz-targeted (REQ-NFR-73) — JSON body in, drop-or-accept decision out.

## What this is NOT

- Not a general-purpose log-shipping receiver. It accepts only the herold client schema; arbitrary payloads are rejected.
- Not an OTLP receiver in the standards sense. We do not implement OTLP ingest. Browsers speak our JSON; the server speaks OTLP outbound.
- Not a long-term storage layer. The ring buffer is "last N hours". Operators wanting permanent retention configure OTLP egress to their own backend.
- Not a source-map server. Stacks travel raw; symbolication happens client-side in the admin viewer (REQ-OPS-212).
