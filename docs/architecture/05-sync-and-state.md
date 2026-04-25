# 05 — Sync and state changes

How clients stay in sync with the server: IMAP CONDSTORE/QRESYNC, JMAP state, and the push mechanisms for each.

The server's job is to assign monotonic change tokens, record state-change events, and deliver notifications to interested sessions.

## Core primitive: the state-change feed

One append-only stream of state changes per principal, persisted in the metadata store:

```
state_changes(
  id                u64 primary,    -- store-global monotonic
  principal_id      u64,
  seq               u64,            -- per-principal monotonic
  entity_kind       text,           -- open enum: mailbox, email,
                                    --   email_submission, identity,
                                    --   vacation_response, …
  entity_id         u64,            -- id of the affected entity in
                                    --   entity_kind's namespace
  parent_entity_id  u64,            -- optional containing-entity id
                                    --   (e.g. mailbox-id for an email)
  op                smallint,       -- 1=created, 2=updated, 3=destroyed
  produced_at       timestamp
)
```

Implementation lives in `internal/store/types.go` `StateChange` plus the SQL migration `0005_state_change_generic.sql` on both the SQLite and Postgres backends.

Properties:
- Per-principal strictly monotonic `seq`.
- Append-only, compacted on retention.
- Retention window: 24h default (enough for reconnects after brief outages; long-absent clients re-sync from scratch).

Every mutation that affects a user-observable entity appends to this feed in the same transaction.

### Forward-compatibility constraint

The schema's `entity_kind` column is intentionally an open enum stored as `TEXT` (the resolved Q5 — see `docs/notes/open-questions.md` Resolved log R40). v1 emits only mail kinds (`mailbox`, `email`, plus the JMAP-Mail `email_submission` / `identity` / `vacation_response` kinds), but the column already accepts `addressbook`, `card`, `calendar`, `event`. Per the v1 scope position (`docs/00-scope.md` NG3), groupware is not in v1 and there is no plan to ship it; this constraint exists only to keep retrofit cheap if that position is ever revisited.

What this means for the implementation:

- `StateChange` in `internal/store/types.go` mirrors the table's `(entity_kind, entity_id, parent_entity_id, op)` shape. Type-specific columns (`MailboxID`, `MessageID`, `MessageUID`, …) MUST NOT live on the change row — the JMAP `Foo/changes` consumer is uniform across types and must stay that way. Per-type sync auxiliaries (IMAP UID/MODSEQ for mail) live on the type's own tables; consumers join when they need them.
- `EntityKind` values (Go `EntityKind` aka SQL `entity_kind`) are the canonical JMAP datatype names: `mailbox`, `email`, `email_submission`, `identity`, `vacation_response`. The `parent_entity_id` column carries an optional containing-entity id — for `email` rows it carries the mailbox so per-mailbox IMAP IDLE filters can dispatch without a join; mailbox-scope rows leave it zero.
- Adding a new entity kind is additive: extend the open enum (no SQL change; it is a `TEXT` column), add the kind's tables, register a JMAP datatype handler. No migration of `state_changes` rows.
- Consumers (broadcaster, JMAP push filter, IDLE filter) MUST dispatch on `entity_kind` (string match) and on `(kind, op)` pairs, not on schema-typed columns, so unknown future kinds flow through without code changes in the dispatch path. Concretely: the FTS worker filters `change.Kind == EntityKindEmail`; the IMAP IDLE broadcaster filters `change.Kind == EntityKindEmail || EntityKindMailbox` with `change.ParentEntityID == selected mailbox`.

## Producers: who writes to the feed

- Mailbox delivery (new message, flag change, expunge): writes one or more `email` changes + `mailbox` changes.
- JMAP `Email/set`, `Mailbox/set`, etc.: writes changes per the spec.
- IMAP commands that modify state (APPEND, STORE, EXPUNGE, COPY, MOVE): write matching changes.
- Admin mutations that affect user data: write changes (e.g. admin quota adjustment doesn't; admin deleting a user's mailbox does).

One mutation = one transaction = atomic change-feed write.

## Consumers: per-protocol sync

### IMAP CONDSTORE / QRESYNC (RFCs 7162)

Per-mailbox monotonic `MODSEQ`. Our `messages` table carries `modseq`, `mailboxes` carries `highest_modseq`.

- On `ENABLE CONDSTORE`: server advertises `HIGHESTMODSEQ` in `SELECT` and `STATUS` responses.
- On `FETCH (MODSEQ)`: return the MODSEQ with each message.
- On `SEARCH MODSEQ <x>`: return messages with `MODSEQ > x`.
- On `STORE (UNCHANGEDSINCE x)`: conditional update (like HTTP If-Match).
- On `SELECT … (CONDSTORE)` or `EXAMINE … (CONDSTORE)`: server informs of `HIGHESTMODSEQ`.

QRESYNC:
- On `SELECT … (QRESYNC (uidvalidity modseq …))`: server computes diff:
  - Expunged UIDs since modseq (from `state_changes` for that mailbox where `op=destroyed`).
  - Changed messages since modseq (`modseq > client_modseq`).
- Emits `* VANISHED` and `* FETCH` batches.

Implementation: IMAP server keeps per-selected-mailbox `last_seen_modseq`. On IDLE, state-change feed subscription filters to the selected mailbox; each relevant change emits the corresponding untagged response immediately.

### IMAP IDLE (RFC 2177)

- Session enters IDLE state.
- Subscribes to the state-change feed for the current principal, filtered to current mailbox.
- Each change → write corresponding untagged response (`EXISTS`, `EXPUNGE`, `FETCH (FLAGS …)`, etc.).
- Heartbeat every ~29 minutes (RFC recommendation) keeps NAT state alive.
- Client sends `DONE` to exit IDLE.

Scaling: 1k+ concurrent IDLE sessions (REQ-NFR target) — each is a goroutine + a subscription handle. The state-change feed broadcaster is single-producer (metadata-store writer) to many-consumer (waiting sessions). In-process Go channel, bounded.

### JMAP state (RFC 8620 §5.2)

Every JMAP type has a `state` string — opaque to the client, monotonic per type per account. Our implementation: concatenate entity kind with the max `seq` of state_changes for that kind.

- `Email/get` returns the current `Email` state.
- `Email/changes` (since state X) returns created/updated/destroyed IDs since that state.
- `Email/set` returns the new state.

Implementation: state string is `<entity>-<seq>` (e.g. `email-12345`). On query, server scans `state_changes` from `since_seq` to current for that entity kind.

### JMAP push (RFC 8620 §7, RFC 8887 optional)

Two delivery paths:

**EventSource (SSE)** — v1 primary:
- Client connects `GET /jmap/eventsource?types=Email,Mailbox&closeafter=no&ping=30`.
- Server streams `StateChange` events.
- Implementation: subscribes the connection to the state-change feed, filters to requested types, emits SSE events with current state strings for changed types.

**WebSocket** (RFC 8887) — phase 3:
- Richer bidirectional. Same semantics, different transport. Skip for v1.

**PushSubscription** (Web Push with VAPID) — phase 3:
- For mobile clients. Server sends push to an external push service which forwards to the device. Complex and not required for v1.

## Notification broadcasting (internal)

Inside the process:

```
       mutation transaction commits
               │
               ▼
    state_changes table append
               │
               ▼
       broadcaster task
       (wakes on post-commit hook)
               │
          ┌────┼────┐
          ▼    ▼    ▼
      session subscribers (IDLE, SSE, WebSocket)
```

Broadcaster reads post-commit signals from the store (transactional: commit hooks fire after the metadata-store transaction commits) and dispatches events to subscribers.

Subscribers register with filters (principal, entity kinds, mailbox id). Dispatch is synchronous-per-subscription to avoid reorder, but fanout is concurrent across subscribers.

Backpressure: if a subscriber's outbound queue is full, we drop the connection (IDLE session closed with `BYE`). Clients reconnect and resync — that's the protocol's resilience model.

## Crash and reconnect semantics

### After server restart:
- `state_changes.seq` continues from where it left off (persistent sequence).
- Clients' stored state strings / MODSEQ / sync tokens remain valid (they refer to seq numbers that survive restart).
- In-flight IDLE / SSE sessions dropped; clients reconnect and re-sync.

### After long client absence:
- If client's state is older than retention window, server returns `CANNOT` / JMAP `cannotCalculateChanges`, client does a full resync.
- Default retention: 24h. Operator can extend.

### UIDVALIDITY (IMAP):
- Normally never changes.
- Bumped only on catastrophic events (mailbox deleted and recreated with same name, cross-node data migration). Clients resync fully when UIDVALIDITY changes.

## Correctness checks

CONDSTORE/QRESYNC is tricky. Invariants the implementation must maintain:

- MODSEQ monotonic per mailbox. Never decreases.
- Every mutation to a message bumps its MODSEQ to a new, globally-unique-per-mailbox value.
- EXPUNGE records preserve enough to answer "what was expunged since MODSEQ x" for the retention window.
- Flag changes are MODSEQ-bumping events.
- HIGHESTMODSEQ is the max MODSEQ in the mailbox.

Test strategy: a "sync oracle" test that replays a recorded sequence of operations and diffs the CONDSTORE/QRESYNC outputs against a reference (e.g. against Dovecot's behavior for the same inputs).

## JMAP `changes` edge cases

- A single atomic mutation might create, update, and destroy within the same transaction. Per JMAP `changes` spec, we report as:
  - `created`: IDs that didn't exist at `since` and do exist at current.
  - `updated`: IDs that existed at both but differ.
  - `destroyed`: IDs that existed at `since` and don't now.
- A create-then-destroy in the same tx is correctly reported as "neither" (the client never needed to know).

## Out of band (not in v1)

- Push to clients across multiple servers (federation / multi-master).
- Native iOS APNS push.
- Web Push with VAPID keys.
- Shared-state across replicas (would require a global change-feed backed by an external broker).
