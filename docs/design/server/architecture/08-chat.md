# 08 — Chat protocol architecture

How chat data flows from clients through herold's transports, storage, and fanout machinery. Companion to `requirements/14-chat.md` and `requirements/15-video-calls.md`.

## Wire-shape decision (locked 2026-04-25)

All chat WebSocket and call-signaling frames use the JMAP-style `{type, payload}` envelope. The previous `{op, ...}` shape from REQ-CHAT-/REQ-CALL- spec drafts is superseded; see waves 2.8-2.9.6 for the migration. The frame schema in this document is the authoritative wire contract.

## Three transports for chat

Chat shares two of mail's existing transports and adds a third:

- **JMAP over HTTPS** — durable state. `Conversation/*`, `Message/*`, `Membership/*` methods. Same machinery as `Email/*`: batched method calls, `using` capability list, error model. Carries reads, mutations, search.
- **EventSource (`/jmap/eventsource`)** — durable state-change push. Chat datatypes flow through the same EventSource that already pushes mail state changes. Per `architecture/05-sync-and-state.md` § Forward-compatibility constraint, this is additive: new entity_kind values flow through the dispatch path without code changes.
- **WebSocket (`/chat/ws`)** — ephemeral signals. Typing indicators, presence, WebRTC call signaling. New endpoint; details below.

Why split: typing and presence are high-frequency / low-value-individually / no-history signals. Forcing them through JMAP's `Foo/changes` semantics is wasteful and creates spurious state-change rows. A small WebSocket side-channel keeps the JMAP layer unmuddied. The split is at the data layer; from the user's perspective there's one chat experience.

## Storage shape

Three new tables additive over the existing schema:

```
conversations(
  id              TEXT PRIMARY KEY,
  type            TEXT NOT NULL,        -- 'dm' | 'space'
  name            TEXT,
  description     TEXT,
  created_at      TIMESTAMP NOT NULL,
  last_message_at TIMESTAMP,
  retention_days  INTEGER,              -- NULL = forever
  read_receipts_enabled BOOLEAN DEFAULT 1
)

memberships(
  id                     TEXT PRIMARY KEY,
  conversation_id        TEXT NOT NULL REFERENCES conversations(id),
  principal_id           BIGINT NOT NULL,
  role                   TEXT NOT NULL,    -- 'member' | 'admin'
  joined_at              TIMESTAMP NOT NULL,
  left_at                TIMESTAMP,         -- NULL = current member
  read_through           TEXT,              -- last read message id
  notifications_muted    BOOLEAN DEFAULT 0,
  pinned                 BOOLEAN DEFAULT 0,
  UNIQUE (conversation_id, principal_id)
)

messages(
  id              TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id),
  sender_id       BIGINT NOT NULL,
  type            TEXT NOT NULL,        -- 'text' | 'image' | 'system'
  body_text       TEXT NOT NULL,        -- plain text projection (FTS)
  body_html       TEXT,                 -- rendered HTML (ProseMirror serialised)
  inline_images   TEXT,                 -- JSON array of blob ids
  in_reply_to     TEXT,                 -- nullable, references messages(id)
  reactions       TEXT,                 -- JSON: { emoji: [principal_id, ...] }
  created_at      TIMESTAMP NOT NULL,
  edited_at       TIMESTAMP,
  deleted         BOOLEAN DEFAULT 0,
  system_payload  TEXT                  -- JSON for system messages (call records, member changes, etc.)
)

CREATE INDEX messages_by_conversation ON messages(conversation_id, created_at);
```

Block list is a separate table:

```
chat_blocks(
  blocker_principal_id   BIGINT NOT NULL,
  blocked_principal_id   BIGINT NOT NULL,
  blocked_at             TIMESTAMP NOT NULL,
  PRIMARY KEY (blocker_principal_id, blocked_principal_id)
)
```

Reactions are stored denormalised on `messages.reactions` for query simplicity; a separate `message_reactions` table is a future optimisation if the JSON column becomes a hotspot.

## State-change feed integration

Per the entity-kind-agnostic state-change feed (`architecture/05-sync-and-state.md`), each chat mutation appends to `state_changes` with one of the new `entity_kind` values: `conversation`, `message`, `membership`. The dispatch path is unchanged; consumers (EventSource fanout, FTS worker) filter on `kind == EntityKindMessage` or similar.

State strings advance per the standard rules: a chat type's state string is `<kind>-<seq>` where `seq` is the max `state_changes.seq` for that kind.

## FTS

A separate Bleve index for `messages` (alongside the mail index). Fields indexed: `body_text`, `sender_id` (faceted), `conversation_id` (faceted), `created_at` (date faceted). Same machinery as mail FTS (`requirements/05-storage.md` § FTS).

The split between mail-index and chat-index is operational: mail is the dominant storage shape (large mailboxes, deep history); chat tends toward smaller messages, faster ingest, different scoring. One index per concern keeps either one's cost from affecting the other.

## Ephemeral channel protocol

WebSocket at `/chat/ws`. Each frame is a single JSON object using the `{type, payload}` envelope -- the same shape as JMAP method-call entries on the durable surface, so a single frame dispatcher serves both.

```
{ "type": "<frame-type>", "payload": { ... } }
```

The chat-side frame types are `typing.start`, `typing.stop`, `presence.set`, `subscribe`, `unsubscribe`. The call-side frame type is `call.signal`; its `payload.kind` discriminates the inner call-signaling vocabulary (`offer | answer | decline | ice-candidate | hangup | busy | timeout`).

### Client to server

```
{ "type": "presence.set",   "payload": { "state": "online" | "away" } }
{ "type": "typing.start",   "payload": { "conversationId": "..." } }
{ "type": "typing.stop",    "payload": { "conversationId": "..." } }
{ "type": "subscribe",      "payload": { "conversationId": "..." } }
{ "type": "unsubscribe",    "payload": { "conversationId": "..." } }
{ "type": "call.signal",    "payload": { "kind": "offer",         "conversationId": "...", "callId": "...", "sdp": "..." } }
{ "type": "call.signal",    "payload": { "kind": "answer",        "callId": "...", "sdp": "..." } }
{ "type": "call.signal",    "payload": { "kind": "decline",       "callId": "..." } }
{ "type": "call.signal",    "payload": { "kind": "ice-candidate", "callId": "...", "candidate": "..." } }
{ "type": "call.signal",    "payload": { "kind": "hangup",        "callId": "..." } }
{ "type": "pong",           "payload": {} }
```

### Server to client

Mirrors of the above (call-signal kinds fanned out to the other peer; typing/presence frames fanned out to interested peers), plus server-originated frames:

```
{ "type": "presence.update", "payload": { "principalId": "...", "state": "online" | "away" | "offline" } }
{ "type": "call.signal",     "payload": { "kind": "busy",    "callId": "..." } }
{ "type": "call.signal",     "payload": { "kind": "timeout", "callId": "..." } }
{ "type": "ping",            "payload": {} }
```

TURN credentials are NOT minted on this WebSocket. Clients call `POST /api/v1/call/credentials` on the admin HTTP surface (dual-authenticated by the suite session cookie or a `Bearer hk_...` API key) and receive `{ "urls": [...], "username": "...", "credential": "...", "ttl": 300 }`. See `requirements/15-video-calls.md` REQ-CALL-20..24.

### Connection lifecycle

- Client opens the WebSocket after the JMAP session descriptor confirms `https://tabard.dev/jmap/chat`. The suite session cookie attaches automatically; no separate auth handshake.
- Server accepts; assigns the connection a per-user-session id (multiple connections per user across tabs are tolerated).
- Server sends `{"type":"ping","payload":{}}` every 30 seconds.
- Client responds `{"type":"pong","payload":{}}`.
- A missed pong (>90 s) triggers server-side connection close; client reconnects with exponential backoff (1s, 2s, 4s, 8s, max 30s).
- On reconnect: client re-emits its initial `presence.set` state. Server replays no missed ephemeral signals -- durable state advances are picked up via `Foo/changes` on the JMAP path.

### Backpressure

Per-session outbound buffer is bounded (default 256 frames). A flooded buffer triggers a connection close with code 1009 (message too big). Client reconnects.

Per-session inbound rate limits per `requirements/14-chat.md` REQ-CHAT-43: violations close with 1008 (policy violation).

## Fanout

Internal in-process broadcaster (the same one that drives EventSource and IDLE for mail) gains chat awareness:

```
       message mutation in transaction commits
                     │
                     ▼
            messages table append
            state_changes append
                     │
                     ▼
              broadcaster task
                     │
            ┌────────┼────────┐
            ▼        ▼        ▼
      EventSource    chat WS   FTS worker
      subscribers    typing/   indexer
                     presence
                     fanout
```

The chat WebSocket subscribers are a separate set from the EventSource subscribers but receive from the same broadcaster -- chat WS receives chat-relevant ephemeral signals (typing, presence) plus call-signaling fanout; EventSource receives durable state changes (chat + mail).

## Authentication

The WebSocket authenticates via the suite session cookie (`requirements/02-identity-and-auth.md` cookie auth path). The cookie is `HttpOnly; Secure; SameSite=Strict`; the suite's WebSocket open uses the cookie automatically. There is no chat-specific token, no JWT, no second auth surface.

The TURN-credential mint at `POST /api/v1/call/credentials` is dual-authenticated: the suite session cookie OR a `Bearer hk_...` API key (for non-browser / CLI clients). Per-principal rate limits apply. Mint is deliberately kept off the JMAP request envelope and off the chat WebSocket so the credential issuance side channel can be fuzzed and audited in isolation.

## TURN credential generation

Per `requirements/15-video-calls.md` REQ-CALL-20..24:

```
username = "<expiry-unix-ts>:<principal-id>"          // expiry ~ now+300s
credential = base64(HMAC-SHA1(shared-secret, username))
config = {
  "urls": [ "turn:turn.example.com:3478?transport=udp",
            "turns:turn.example.com:5349?transport=tcp" ],
  "username": "<username>",
  "credential": "<credential>",
  "ttl": 300
}
```

coturn validates inline (no per-credential server state). The shared secret is shared between herold and coturn via system config. Rotation: operator updates `/etc/coturn/turnserver.conf` and `/etc/herold/system.toml` synchronously; SIGHUP both.

## Out of scope (chat architecture)

- Group call SFU (mediasoup, Pion, LiveKit) — see `requirements/15-video-calls.md` § Out of scope.
- E2E encryption — same trust posture as mail.
- WebSocket clustering across nodes — single-node only (NG2).
- Chat federation between herold instances.
- Bridges to external chat networks.
