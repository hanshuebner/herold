# 14 — Chat

Real-time conversation between herold users. DMs (1:1) and Spaces (group rooms) plus 1:1 video calls. Closed system — no federation, no XMPP/Matrix bridges. Driven by the tabard suite plan; the client surface lives in tabard's `docs/requirements/08-chat.md` and `docs/requirements/21-video-calls.md`.

Phase 2 — runs after the JMAP-suite work for calendars/contacts is in flight. Chat shares the JMAP capability registry, the entity-kind-agnostic state-change feed, and the suite session cookie with mail.

## In scope (v1 of chat)

- DMs (1:1) and Spaces (group, 2+ members).
- Text messages with rich formatting (subset of mail's HTML schema), inline images, emoji.
- Reactions on messages.
- Read receipts (DMs always-on, Spaces configurable per-Space).
- Typing indicators (ephemeral, per-conversation fanout).
- Presence (`online` / `away` / `offline`), with per-user "show as offline" override.
- Mute conversations, block users (DM only, soft-block — sender doesn't know).
- Search across messages (FTS, same Bleve index machinery as mail).
- 1:1 video calls (WebRTC + self-hosted coturn TURN).

## Out of scope (chat v1)

- Group video calls — require an SFU; substantial new server. Phase 3+ candidate; not committed.
- Threaded replies inside Spaces.
- Federation, bridges to external networks (Matrix, XMPP, Slack, Telegram).
- End-to-end encryption — same trust posture as mail (operator runs the server).
- File uploads beyond images.
- Voice messages, polls, GIF picker, custom emoji.
- Bots, webhooks into chat.
- Browser-level "incoming call" or "new message" notifications when the tab is closed (depends on service worker; would re-litigate herold's NG-side scope on web push).

## Datatypes (REQ-CHAT-DATA)

New JMAP entity kinds, registered with the capability registry per `architecture/03-protocol-architecture.md` § Capability and account registration. Storage tables additive over the existing schema; per the entity-kind-agnostic state-change feed (`architecture/05-sync-and-state.md`) the change rows for these types flow through the dispatch path without core edits.

- **REQ-CHAT-01** MUST advertise `https://tabard.dev/jmap/chat` as a JMAP capability when chat is enabled. Per-account capability when the account has chat access.
- **REQ-CHAT-02** MUST implement `Conversation` JMAP datatype. Properties: `id`, `type` (`dm` | `space`), `name`, `description?`, `members: [Membership]`, `createdAt`, `lastMessageAt?`, `lastMessagePreview?`, `pinned`, `muted`, `unreadCount`. Methods: `Conversation/get`, `/query`, `/changes`, `/set`.
- **REQ-CHAT-03** MUST implement `Message` JMAP datatype. Properties: `id`, `conversationId`, `senderId`, `type` (`text` | `image` | `system`), `body: { html, text }`, `inlineImages: [BlobId]`, `inReplyTo?`, `reactions: { "<emoji>": [PrincipalId, ...] }`, `createdAt`, `editedAt?`, `deleted`. Methods: `Message/get`, `/query`, `/changes`, `/set`.
- **REQ-CHAT-04** MUST implement `Membership` JMAP datatype. Properties: `id`, `conversationId`, `principalId`, `role` (`member` | `admin`), `joinedAt`, `readThrough?`, `notificationsMuted`. Methods: `Membership/get`, `/query`, `/changes`, `/set`.
- **REQ-CHAT-05** State strings advance per the standard JMAP rules. Push events on EventSource carry `Conversation`, `Message`, `Membership` type names alongside mail's.
- **REQ-CHAT-06** Inline images use the existing `Blob/upload` path. No separate chat-blob storage; the chat-message's `inlineImages: [BlobId]` references blobs the same way `Email.attachments` does.

## Conversation lifecycle (REQ-CHAT-LIFECYCLE)

- **REQ-CHAT-10** A DM is created server-side on the first `Message/set { create }` between two principals where no DM yet exists. Subsequent DMs between the same two principals reuse the existing conversation. (No "new DM" UI distinct from "send DM".)
- **REQ-CHAT-11** A Space is created via `Conversation/set { create, type: "space" }`. The creator becomes admin; initial members are added in the same call.
- **REQ-CHAT-12** Member changes (add / remove / role change) emit a system message in the Space.
- **REQ-CHAT-13** When a member leaves a Space (Membership/set destroy), they lose read access to all subsequent and historical messages in that Space. Existing client-side caches are not the substrate's responsibility; the server returns Forbidden on subsequent Message/get requests for that Space from a non-member.
- **REQ-CHAT-14** Spaces require ≥1 admin. Removing the last admin is rejected; an admin must promote a member first.
- **REQ-CHAT-15** Conversation deletion (admin-only for Spaces; both members for DMs) is destructive — messages, memberships, blobs (when refcount reaches zero) are GC'd per the standard retention path.

## Messages (REQ-CHAT-MSG)

- **REQ-CHAT-20** Editing a message is allowed within a per-account window (default 15 minutes, configurable). After the window, `Message/set { update }` for the body fields is rejected.
- **REQ-CHAT-21** Deletion is soft: `Message.deleted = true`, body cleared. The row remains so threading and read-receipt offsets are stable. Deleted messages render as "_message deleted_" client-side.
- **REQ-CHAT-22** Inline images obey the same size cap as mail attachments (REQ-PROTO and the `Blob/upload` per-call cap). Larger uploads return 413.
- **REQ-CHAT-23** Reactions are toggled via `Message/set { update: { reactions: ... } }`. Server validates that the requesting user is in the conversation; otherwise 403.
- **REQ-CHAT-24** A user editing or deleting another user's message is rejected (admin-bypass cut from v1).

## Read receipts (REQ-CHAT-READ)

- **REQ-CHAT-30** `Membership.readThrough` is the message id of the most recent message this member has read. Advanced by client-side `Membership/set { update: { readThrough } }` calls debounced from scroll.
- **REQ-CHAT-31** DMs ignore `Conversation.readReceiptsEnabled`; receipts are always on. (Receipts in DMs are an accepted norm; cross-platform.)
- **REQ-CHAT-32** Spaces have a per-Space `readReceiptsEnabled` boolean, admin-mutable. Default: on. When off, server still tracks `readThrough` (used internally for unread counts) but does NOT expose other members' `readThrough` values via `Membership/get` — only the requesting user's own.

## Ephemeral channel (REQ-CHAT-WS)

A separate WebSocket endpoint, distinct from the JMAP HTTP and EventSource surfaces. Carries signals that don't belong in durable state: typing, presence, WebRTC call setup.

- **REQ-CHAT-40** MUST serve a WebSocket endpoint at `wss://<origin>/chat/ws`. Authenticated by the suite session cookie (REQ-AUTH integration); no separate token negotiation.
- **REQ-CHAT-41** Message format: JSON, one frame per WebSocket message. Every frame uses the JMAP-style envelope `{"type":"<frame-type>","payload":{...}}`. The chat-side frame types are `typing.start`, `typing.stop`, `presence.set`, `subscribe`, `unsubscribe`; the call-side frame type is `call.signal` with an inner `payload.kind` discriminator. Authoritative schema in `architecture/08-chat.md` § Ephemeral channel protocol.
- **REQ-CHAT-42** Heartbeat: server sends `{"type":"ping","payload":{}}` every 30 seconds; client responds `{"type":"pong","payload":{}}`. Server drops the connection on a missed pong (>90 s); client reconnects on a missed ping (>90 s) with exponential backoff.
- **REQ-CHAT-43** Server-side rate limits per user-session: typing emit ≤1/sec, presence emit ≤1/sec, ICE candidate emit ≤30/call. Violations close the connection with code 1008 (policy violation).
- **REQ-CHAT-44** Server-side outbound buffer per session is bounded; flooded buffer triggers a connection close. Client reconnects.
- **REQ-CHAT-45** Multiple concurrent WebSocket connections per user (one per browser tab) are tolerated. Presence is "online if at least one connection is live".
- **REQ-CHAT-46** No replay on reconnect for ephemeral signals. Durable state changes (new message, etc.) are picked up via `Foo/changes` after reconnect — the EventSource handles those.

## Presence (REQ-CHAT-PRESENCE)

- **REQ-CHAT-50** Server tracks a per-user presence state derived from WebSocket connection state and the user's "show as offline" preference: `online` (>=1 connection, document recently active), `away` (>=1 connection, no document activity for >5 min), `offline` (no connections OR show-as-offline mode).
- **REQ-CHAT-51** Activity signal: client emits `{"type":"presence.set","payload":{"state":"online"|"away"}}` periodically (defaults: online while focused; away after 5 min idle). Server uses these signals plus connection state to compute the public presence value.
- **REQ-CHAT-52** Presence updates fan out to interested peers -- anyone in a conversation with the user -- via `{"type":"presence.update","payload":{...}}` on those peers' WebSocket connections.
- **REQ-CHAT-53** "Show as offline" mode (per-user setting) suppresses outbound presence updates; the user appears offline to others but still receives messages, presence updates from others, etc. normally.
- **REQ-CHAT-54** Presence is NOT durable; it is not exposed via `Foo/get` JMAP methods. The only access path is the ephemeral channel.

## Typing indicators (REQ-CHAT-TYPING)

- **REQ-CHAT-60** Client emits `{"type":"typing.start","payload":{"conversationId":"..."}}` while actively typing in a conversation; debounce <=1 emit per 3 s. Emit `{"type":"typing.stop","payload":{"conversationId":"..."}}` 5 s after the last keystroke.
- **REQ-CHAT-61** Server fans out the signal to other participants on the same conversation via their WebSocket connections.
- **REQ-CHAT-62** Receiver auto-clears the indicator after 7 s of no further `typing.start` signal (covers the case where the sender's tab dies mid-typing).
- **REQ-CHAT-63** Typing signals are NOT persisted, NEVER appear in scrollback, NEVER trigger `Message/changes`.

## Mute and block (REQ-CHAT-MUTE)

- **REQ-CHAT-70** Mute a conversation: `Conversation/set { update: { muted: true } }` (or per-`Membership` for Spaces — TBD). Muted conversations don't trigger client-side notifications and don't contribute to the global unread count, but new messages are still delivered.
- **REQ-CHAT-71** Block a user (DM only): exposed as a method on `Conversation` or as a separate `BlockList` datatype — TBD. Blocking is server-enforced: future messages from the blocked user to the blocker are rejected at `Message/set` with 403; the sender does NOT see they have been blocked (their `Message/set` "succeeds" client-side from their cache; server does not deliver). This is a soft-block — deliberate, standard pattern.
- **REQ-CHAT-72** Block is reversible.

## Search (REQ-CHAT-SEARCH)

- **REQ-CHAT-80** `Message/query` with `filter: { text: "..." }` searches FTS over `Message.body.text`. Same Bleve index machinery as mail (REQ-STORE-FTS and friends), separate index name.
- **REQ-CHAT-81** The text projection used for FTS is the plain-text version of the body (`body.text`), not the HTML — strips formatting, keeps content.
- **REQ-CHAT-82** Search results are scoped to conversations the requesting user is a member of. Admin / cross-user search is out (NG1 / single-user posture).

## Storage and retention (REQ-CHAT-STORAGE)

- **REQ-CHAT-90** Chat data stored in herold's metadata store alongside mail. New tables: `conversations`, `messages`, `memberships`. Reactions denormalised onto the message row (JSON column or separate `message_reactions` table — implementation choice; the JMAP shape is `Message.reactions`).
- **REQ-CHAT-91** Inline images stored in the same blob store as mail attachments. Blob refcount includes chat-message references.
- **REQ-CHAT-92** Retention: per-account default (forever) and per-Space override (admin-configurable). When retention is bounded, a sweeper deletes messages older than the retention window; system messages are retained for the conversation history (joins/leaves don't decay).
- **REQ-CHAT-93** Block list, mute state, and presence preferences are per-principal; survive across sessions.

## Auth and authorization (REQ-CHAT-AUTH)

- **REQ-CHAT-100** All chat endpoints (JMAP datatype methods, the WebSocket, the TURN-credential mint) require an authenticated suite session cookie. The same cookie used for mail; no separate auth.
- **REQ-CHAT-101** A user can only `Conversation/get` / `Message/get` / `Membership/get` for conversations they are a member of. The store enforces this at the query layer (every query joins on `Membership` for the requesting principal).
- **REQ-CHAT-102** A user cannot enumerate other users' Spaces or DMs. There is no "list all Spaces" admin path in v1; cross-tenant boundaries don't apply (single-user, NG1) but cross-user privacy does.

## See also

- `architecture/08-chat.md` — chat protocol architecture, including the WebSocket frame schema and the WebRTC signaling shape.
- `requirements/15-video-calls.md` — 1:1 video calls (continues from chat datatypes).
- Tabard side: `/Users/hans/tabard/docs/requirements/08-chat.md`, `/Users/hans/tabard/docs/architecture/07-chat-protocol.md`.
