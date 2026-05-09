# 21 - Response time budget and indexed-only access

*(Added 2026-05-09 in response to a real incident: a JMAP `Email/query`
with `text:"Discourse"` against a 404,620-message mailbox pinned the
process at 200 % CPU with no return after ten minutes. Root cause was
two-layered: `gatherCandidatesRaw` bypassed FTS for any filter
containing `text:` or `body:` and fell through to `listPrincipalMessages`
+ per-candidate MIME parse via `enmime.ReadEnvelope`. The bypass was
added on 2026-04-28 (commit cdf8264) on the stale assumption that "the
FTS stub does not index body content"; the FTS index has indexed body
content with `IncludeInAll = true` since 2026-04-24 (commit 9f22f4f).
Search hit the bypass; the bypass scanned the whole mailbox; every hit
got MIME-re-parsed. This document specifies the long-term contract: no
un-indexed scan ever, every operation has a wall-clock budget, and both
properties are observable.)*

## Scope

JMAP method dispatch (`/jmap`) and IMAP command dispatch. Out of scope:
SMTP relay (already has its own bounded acceptance per REQ-NFR-01),
EventSource / push (streaming by design), JMAP `/jmap/upload` blob
upload (treated as I/O, not a method), admin REST (already has
`herold_admin_request_duration_seconds` per REQ-OPS).

## R1 - no un-indexed scans

| ID | Requirement |
|----|-------------|
| REQ-PERF-INDEX-01 | A handler MUST NOT iterate a candidate set drawn from anywhere except a deterministic index lookup: FTS `Query`, SQLite UID/mailbox key, message-ID lookup, principal-scoped envelope page bounded by `Limit`. The forbidden shape is `for msg := range messages { fetch_blob(msg); parse_mime(msg) }` where `messages` was not narrowed by an index. The threshold is zero: even a 1-message mailbox does not exempt a handler from this rule. |
| REQ-PERF-INDEX-02 | `Email/query` text predicates (`text:`, `subject:`, `from:`, `to:`, `cc:`, `body:`) all route through `storefts.Index.Query`. The current bypass for `text:`/`body:` (`gatherCandidatesRaw`'s fallback to `listPrincipalMessages`) is removed. `body:` is mapped onto `q.Body` in `buildFTSQuery` (the Bleve mapping at `internal/storefts/index.go:188` already indexes `fieldBody`; the wiring is the missing part). |
| REQ-PERF-INDEX-03 | `Email/query` thread-keyword predicates (`someInThreadHaveKeyword`, `noneInThreadHaveKeyword`) require a thread-flag index. Until that index exists in `storefts`, the handler MUST refuse the request with `unsupportedFilter` (RFC 8621 §5.5) rather than fall through to a full account scan. |
| REQ-PERF-INDEX-04 | `Email/get` with `ids: null` (RFC 8621 §4.2: "if null, return all") MUST be refused with `requestTooLarge`. The "rarely useful but spec-permitted" comment in `email/get.go:78-80` is replaced by an explicit refusal: clients that legitimately want to walk every email page through `Email/query` with explicit `Limit` + `Position`. |
| REQ-PERF-INDEX-05 | `Thread/get` and `Thread/changes` MUST use a thread-membership index (per-principal thread roster, derived from the existing `X-GM-THRID`-style threading in the store) rather than `listAllMessages`. If the index is not yet present, the handlers MUST refuse with `requestTooLarge`. |
| REQ-PERF-INDEX-06 | IMAP `SEARCH` MUST route every text-bearing criterion through `storefts.Index.Query`. Criteria not expressible as an FTS query (e.g. `KEYWORD <flag>` against a non-indexed flag, or `OR` over heterogeneous criteria the FTS layer does not yet compose) MUST be answered with tagged `NO [SERVERBUG] unindexed search not supported`. The `evalSearch` per-message fallback at `internal/protoimap/session_store_search.go:307` is removed. |
| REQ-PERF-INDEX-07 | Refusal is a method-level error in JMAP and a tagged `NO` in IMAP; refusal does not crash the connection, does not abort a JMAP batch's already-completed calls, and is observable per REQ-PERF-METRIC-01. |
| REQ-PERF-INDEX-08 | The R1 ban is correctness, not performance. The principal flag in REQ-PERF-DEADLINE-30 does NOT relax this rule for any user, including admins. A privileged user with a slow legitimate workload has more time, not more access to slow algorithms. |

## R2 - response time deadline

| ID | Requirement |
|----|-------------|
| REQ-PERF-DEADLINE-01 | Every JMAP method invocation and every IMAP command runs under `context.WithDeadline`. Default `1000ms`. Wall clock starts when the dispatcher hands work to the handler (after auth, JSON decode, capability check). |
| REQ-PERF-DEADLINE-02 | On expiry the handler's `ctx` is cancelled. Downstream `database/sql`, `bleve.SearchInContext`, blob reads, and HTTP fetches MUST honour `ctx.Err()`. Code that ignores `ctx` cancellation (e.g. an unbounded `for` over a `[]Message`) is a defect. |
| REQ-PERF-DEADLINE-10 | JMAP wire shape on deadline: |
| | - If no method response has been written yet for the batch: HTTP 503 with `application/problem+json` body, type `https://netzhansa.com/problems/jmap-deadline-exceeded`, status 503. |
| | - If the K-th call of an N-call batch trips the deadline after K-1 successes: HTTP 200, the K-th method's response is `["error", { "type": "serverFail", "description": "deadline exceeded" }, "<call-id>"]`, the remaining N-K calls each return the same `serverFail` (the batch's overall ctx is cancelled; each subsequent handler observes the cancelled ctx immediately and reports the same error). |
| | - The batch's overall budget is bounded by `N * default_deadline`; a 16-call batch of 1 s methods is capped at 16 s wall. |
| REQ-PERF-DEADLINE-11 | IMAP wire shape on deadline: tagged `NO [INUSE] deadline exceeded`. The connection is left open. The server does NOT issue an untagged `BYE` and does NOT auto-close. |
| REQ-PERF-DEADLINE-12 | Deadline expiry is logged at `WARN` with attributes `subsystem=perf-deadline`, `protocol={jmap,imap}`, `method`, `principal`, `elapsed_ms`, `request_id`. One log entry per expiry; no duplicate per-batch-call entries. |
| REQ-PERF-DEADLINE-13 | A handler that completes fast does not log. The deadline path is observable via metrics (REQ-PERF-METRIC-01) on the happy path; logging is reserved for the actual exceedance event. |

## R3 - overrides

| ID | Requirement |
|----|-------------|
| REQ-PERF-DEADLINE-20 | System config gains a `[performance]` block: |
| | ```toml |
| | [performance] |
| | default_deadline = "1s" |
| | |
| | [performance.method_deadline] |
| | "Email/import"      = "30s" |
| | "Email/set"         = "5s" |
| | "Mailbox/changes"   = "2s" |
| | "IMAP:APPEND"       = "30s" |
| | "IMAP:FETCH"        = "5s" |
| | ``` |
| | JMAP method names use the RFC 8620 `Type/methodName` form. IMAP commands use `IMAP:<COMMAND>` with the command verb uppercased. Unknown method names are rejected at config load with a non-fatal warning; the entry is dropped. |
| REQ-PERF-DEADLINE-21 | A new principal flag `bypass_response_deadline` skips the deadline entirely for that principal's requests. Granted via `herold principal grant-flag <email> bypass_response_deadline`; revoked via `revoke-flag`. The flag is orthogonal to `admin` (an admin running buggy code still benefits from a fast-fail). |
| REQ-PERF-DEADLINE-22 | Resolution order at request time: principal flag (skip) wins over method override wins over default. The principal flag is checked once per request; the method override is keyed by the method name produced by the dispatcher. |
| REQ-PERF-DEADLINE-23 | Reload (SIGHUP) re-reads `[performance]` and applies the new defaults to subsequent requests. In-flight requests retain the deadline they were dispatched under. |

## R4 - metrics

| ID | Requirement |
|----|-------------|
| REQ-PERF-METRIC-01 | New histogram `herold_jmap_method_duration_seconds{method, outcome}` where `outcome` is one of `{ok, error, deadline_exceeded, unindexed_refused}`. Buckets match the existing `herold_admin_request_duration_seconds` histogram (the standard Prometheus default plus `0.025`). |
| REQ-PERF-METRIC-02 | New histogram `herold_imap_command_duration_seconds{command, outcome}` with the same outcome vocabulary. The `command` label is the uppercase IMAP verb. |
| REQ-PERF-METRIC-03 | New counter `herold_request_deadline_exceeded_total{protocol, op}` for alerting. Allows a Prometheus rule `rate(herold_request_deadline_exceeded_total[5m]) > 0.1` without histogram-bucket arithmetic. |
| REQ-PERF-METRIC-04 | New counter `herold_request_unindexed_refused_total{protocol, op}`. Same shape as REQ-PERF-METRIC-03; alerts when a client persistently asks for a banned shape (suggests UI bug or misconfiguration, not server bug). |

## Implementation order

| ID | Requirement |
|----|-------------|
| REQ-PERF-IMPL-01 | First commit: route `text:`/`body:` through FTS in `gatherCandidatesRaw` and `buildFTSQuery`. Drops the immediate CPU spin. The body-via-FTS path is the same one `subject:`/`from:`/`to:`/`cc:` already use; the change is removing the `!filterNeedsBodyBlobParse(f)` guard at `query.go:648` and adding `f.Body` to `buildFTSQuery`. The `filterNeedsBodyBlobParse` function and `buildFilterData`'s body-parse branch (`query.go:308-316, 352-354`) are removed. |
| REQ-PERF-IMPL-02 | Second commit: the JMAP / IMAP duration histograms (REQ-PERF-METRIC-01..02) and counters (REQ-PERF-METRIC-03..04). No deadline enforcement yet; metrics first so the deadline rollout is observable. |
| REQ-PERF-IMPL-03 | Third commit: the deadline middleware + system config + principal flag (R2 + R3). The middleware wraps the existing JMAP dispatcher and IMAP command runner; the existing context plumbing already reaches every handler. |
| REQ-PERF-IMPL-04 | Fourth commit: refusals for the remaining banned shapes (REQ-PERF-INDEX-03..06). `Email/get(ids=null)` and the IMAP `evalSearch` removal are mechanical. `Thread/get` / `Thread/changes` and thread-keyword aggregation require a thread-membership index in `storefts`; if the index is not ready, ship the refusal first (REQ-PERF-INDEX-05/03 explicitly permit refusal as the interim shape) and add the index in a follow-up. |
| REQ-PERF-IMPL-05 | The four commits land separately on `main`. Each is independently revertable. The first commit alone is a complete user-visible win and may ship before the rest. |

## Test strategy

| ID | Requirement |
|----|-------------|
| REQ-PERF-TEST-01 | The bug reproducer is a regression test: `TestEmailQuery_TextFilter_RoutesThroughFTS` asserts that a `text:` filter against a fixture mailbox with N messages does not invoke `mailparse.Parse` more than once (zero is the target; one tolerates body-rendering of the returned hit). The test injects a `parseFn` that increments a counter (the existing `parseFn` injection point at `register.go:90-106` already supports this). |
| REQ-PERF-TEST-02 | Deadline test: `TestJMAPMethodDeadline` configures a 50 ms default and dispatches a method whose handler `time.Sleep`s 200 ms; asserts HTTP 503 (or method-level `serverFail` per REQ-PERF-DEADLINE-10), the histogram increments with `outcome="deadline_exceeded"`, and a single WARN log line is emitted. |
| REQ-PERF-TEST-03 | Override test: `TestJMAPMethodDeadline_PrincipalBypass` grants the test principal `bypass_response_deadline` and asserts the same handler now succeeds. |
| REQ-PERF-TEST-04 | Refusal test: `TestEmailGet_NilIDs_Refuses` asserts the refusal shape (method-level `requestTooLarge`) and that no scan was performed (counter for `unindexed_refused` increments; no message envelope was loaded). |

## Out of scope

| ID | Requirement |
|----|-------------|
| REQ-PERF-OOS-01 | Per-principal QPS limits or token-bucket rate limiting. Existing per-IP / per-account auth limits (REQ-NFR-110) cover the abuse path; per-method rate limiting is a separate concern. |
| REQ-PERF-OOS-02 | Bulk-batch deadline pooling (e.g. "the whole batch shares one deadline"). Each method gets its own. A client that needs the lower bound writes a smaller batch. |
| REQ-PERF-OOS-03 | Server-side request prioritisation / scheduling. The Go runtime's goroutine scheduler is sufficient; we do not implement a method-priority queue. |
| REQ-PERF-OOS-04 | Adaptive deadlines based on recent p99. The deadline is a static config value; operators tune it. |

## Open questions

None at landing time; the three fork decisions (HTTP 503 + method error envelope, per-method config + principal flag, threshold zero) were resolved during 2026-05-09 design discussion.
