# 07 — Chat protocol

How chat data flows between the suite's panel and herold. Companion to `requirements/08-chat.md` and `requirements/21-video-calls.md`.

## Two transports

Chat uses two channels concurrently:

- **JMAP over HTTPS** for durable state — conversations, messages, memberships, reactions, read receipts. Requests batched per `02-jmap-client.md`. Push of state changes via the **EventSource** channel that already serves mail (resolved R5).
- **WebSocket** at `wss://<origin>/chat/ws` for ephemeral signals — typing indicators, presence, WebRTC call signaling. Authenticated by the suite session cookie (no extra token).

Why split: typing and presence are high-frequency / low-value-individually / no-history signals. Forcing them through JMAP's `Foo/changes` semantics is wasteful and creates spurious state-change events. A small WebSocket side-channel keeps the JMAP layer unmuddied.

The split is at the data layer, not the conceptual layer. From the user's perspective there is one chat experience.

## Data model

New JMAP datatypes registered by herold (per the capability registry pattern in herold's `architecture/03-protocol-architecture.md`):

### `Conversation`

```
{
  id:                 String,
  type:               "dm" | "space",
  name:               String,           // for spaces; for DMs, computed from the other member
  description:        String?,          // spaces only
  members:            [Membership],     // computed from Membership/query, but flat-listed here for convenience
  createdAt:          UTCDate,
  lastMessageAt:      UTCDate?,
  lastMessagePreview: String?,
  pinned:             Boolean,
  muted:              Boolean,
  unreadCount:        Number            // server-computed for the requesting user
}
```

### `Message`

```
{
  id:               String,
  conversationId:   String,
  senderId:         String,             // PrincipalId
  type:             "text" | "image" | "system",
  body:             { html: String, text: String },   // ProseMirror serialised
  inlineImages:     [BlobId],
  inReplyTo:        String?,            // Message id, optional
  reactions:        { "🎉": [PrincipalId, ...], ... },
  createdAt:        UTCDate,
  editedAt:         UTCDate?,
  deleted:          Boolean
}
```

### `Membership`

```
{
  id:               String,
  conversationId:   String,
  principalId:      String,
  role:             "member" | "admin",
  joinedAt:         UTCDate,
  readThrough:      String?,            // last Message id this member has read
  notificationsMuted: Boolean
}
```

`Reaction` is not its own type — folded into `Message.reactions` for query simplicity. Read receipts are not their own type either — folded into `Membership.readThrough`.

### Methods

Standard JMAP shape:

- `Conversation/get`, `/query`, `/changes`
- `Conversation/set` for creating Spaces and updating mute/pin/name
- `Message/get`, `/query`, `/changes`
- `Message/set` for sending, editing (subject to REQ-CHAT-25 window), deleting, and toggling reactions
- `Membership/get`, `/query`, `/changes`
- `Membership/set` for joining, leaving, role changes, mute toggle, advancing readThrough

State strings advance per the standard JMAP rules (`architecture/03-sync-and-state.md`); push events on EventSource carry chat type names alongside mail's.

## Ephemeral channel: `wss://<origin>/chat/ws`

Single per-session WebSocket. JSON-encoded messages.

### Outbound (client → server)

```
{ "op": "typing", "conversationId": "..." }
{ "op": "typing-stopped", "conversationId": "..." }
{ "op": "presence", "state": "away" | "online" }
{ "op": "call.invite", "conversationId": "...", "sdp": "...", "callId": "..." }
{ "op": "call.accept", "callId": "...", "sdp": "..." }
{ "op": "call.decline", "callId": "..." }
{ "op": "call.candidate", "callId": "...", "candidate": "..." }
{ "op": "call.hangup", "callId": "..." }
{ "op": "call.credentials", "callId": "..." }   // request fresh TURN creds
```

### Inbound (server → client)

Mirrors of the above where applicable, fanned out to other participants of the relevant conversation. Plus:

```
{ "op": "presence-update", "principalId": "...", "state": "online" | "away" | "offline" }
{ "op": "call.credentials.response",
  "callId": "...",
  "config": { "urls": [...], "username": "...", "credential": "...", "ttl": 300 } }
```

### Connection lifecycle

- The suite opens the WebSocket at suite bootstrap (after the JMAP session descriptor confirms `https://netzhansa.com/jmap/chat` capability). The cookie attaches automatically; no separate auth handshake.
- Heartbeats: server sends a `{ "op": "ping" }` every 30 seconds; the suite responds `{ "op": "pong" }`. Missed pong > 90 s → server drops; missed ping > 90 s → client reconnects.
- Reconnect: exponential backoff (1s, 2s, 4s, 8s, max 30s). On reconnect, the suite re-issues `presence` and resubscribes; the server replays no missed messages (ephemeral signals are not retained).
- The presence state defaults to `online` on connect.

### Backpressure

- The server applies per-user rate limits on outbound: typing-stopped/typing once per second, ICE candidates 30 per call, etc. Violations close the connection with a 1008 (policy violation).
- A flooded server-side outbound buffer per session triggers a connection close; the suite reconnects.

## TURN credentials

Per `21-video-calls.md` REQ-CALL-32, the suite fetches TURN credentials per call. Mechanism:

1. The suite sends `{"op": "call.credentials", "callId": "..."}` over the WebSocket.
2. Herold validates that the user is in the conversation and that the call is real (or pending).
3. Herold mints a TURN credential against coturn's shared secret, scoped to the user, valid ~300 seconds.
4. Returns `{"op": "call.credentials.response", ...}`.

If the call lasts longer than the credential TTL, the suite refreshes 30 seconds before expiry.

## Storage in herold

Chat datatypes are new entity kinds in herold's storage. Per the forward-compat work in herold's `architecture/05-sync-and-state.md` (the open `entity_kind` enum), this is purely additive: new tables, new datatype handlers registered with the JMAP capability registry. No migration of existing tables.

The state-change feed gains entries for `conversation`, `message`, `membership` (and `reaction` if it ever splits out). The push broadcaster fans these out on EventSource same as mail.

## Persistent panel and transport lifecycle

The chat panel mounts in the suite shell (`architecture/01-system-overview.md` § Suite shape) once per browser tab. Both the EventSource channel and the chat WebSocket are owned by the shell; route changes within the suite (mail ↔ calendar ↔ contacts) do not tear them down.

If the user opens a second suite tab, that tab opens its own EventSource and WebSocket. Herold tolerates multiple connections per session-cookie; presence is "online if at least one connection is online".

## Capability negotiation

The suite sees the chat capability in the JMAP session descriptor (`https://netzhansa.com/jmap/chat`). If absent, the chat panel renders a single line: "Chat is not configured on this server" — no panel UI. Per resolved R14, this should never happen in production; the empty state exists for development environments and misconfigurations.

## Out of scope

- Federation between herold instances. Single-server.
- Bridges to external chat networks (Matrix, XMPP, Slack, Telegram). The suite users only.
- End-to-end encryption. Same trust posture as mail.
- Voice messages, GIF picker, polls, file uploads beyond images.
- Threaded replies (replies-to-a-message that fork the thread). Single linear timeline per conversation in v1.
- Bots / webhooks into chat. Possibly later.
