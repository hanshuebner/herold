# 08 — Chat

Real-time conversation between the suite users. DMs (1:1) and Spaces (group rooms). Lives as a persistent panel in the suite shell, not a separate app — see `../architecture/01-system-overview.md` § Suite shape.

Closed system: no federation, no XMPP/Matrix bridges. The suite users only.

## Conversations

Two types: direct (DM, 1:1) and space (group, 2+ members).

| ID | Requirement |
|----|-------------|
| REQ-CHAT-01 | DMs are 1:1 conversations. Created on first message — the user picks a recipient (autocompleted from the contacts source per `02-mail-basics.md` REQ-MAIL-11) and types. The conversation is created server-side on send. |
| REQ-CHAT-02 | Spaces are group conversations with explicit membership. Created from a "+ Space" affordance: name, optional description, initial members. |
| REQ-CHAT-03 | Space members have a role: `member` or `admin`. The creator is admin. Admins can add/remove members and change the space name; members cannot. |
| REQ-CHAT-04 | A user can leave a Space at any time. Leaving emits a system message ("Hans left the Space"); the user is removed from membership. |
| REQ-CHAT-05 | An admin can remove a member; same system-message emission. |
| REQ-CHAT-06 | Conversations sort by most recent activity. Pinned conversations sort first within their type. |
| REQ-CHAT-07 | Each conversation has a mute state: muted conversations don't trigger panel notifications and don't show in the unread count, but new messages still appear when the conversation is open. |

## Messages

| ID | Requirement |
|----|-------------|
| REQ-CHAT-20 | A message has: sender (PrincipalId), conversation reference, body (rich text via ProseMirror), inline image references (BlobIds), createdAt, optional editedAt, optional in-reply-to (for inline quoted replies). |
| REQ-CHAT-21 | The compose schema for chat is **lighter than mail's**: text marks (bold, italic, underline, strikethrough, inline code), links, bullet / numbered lists, inline images, code blocks. **No** headings, blockquotes, horizontal rules, or tables — chat messages should not look like emails. |
| REQ-CHAT-22 | Inline images are uploaded via `Blob/upload` (the same path mail uses) and referenced by BlobId in the message body. The chat panel renders them with a max width of ~400px and a click-to-expand overlay. Larger payloads → server returns 413; surfaced as inline error in the compose. |
| REQ-CHAT-23 | Pasting an image into the compose uploads and inserts it inline. Drag-and-drop similarly. |
| REQ-CHAT-24 | Emoji entry is via `:shortcode:` autocomplete (e.g. `:tada:` → 🎉) plus a picker accessible from the compose toolbar. Standard Unicode emoji only — no custom emoji in v1. |
| REQ-CHAT-25 | Messages can be edited within a configurable window (default 15 minutes); edited messages display "(edited)" with the editedAt time on hover. |
| REQ-CHAT-26 | Messages can be deleted by the sender. Deleted messages render as "_message deleted_" in italic; the storage row remains so threading and read receipts stay consistent. |
| REQ-CHAT-27 | The compose has its own keyboard shortcuts inside the panel: `Enter` sends; `Shift+Enter` inserts a newline; `Cmd/Ctrl+B/I/U` for marks; `:` triggers emoji autocomplete; `@` triggers member autocomplete in Spaces. |

## Reactions

| ID | Requirement |
|----|-------------|
| REQ-CHAT-30 | Any message can carry zero or more emoji reactions. Each reaction is a (emoji, sender) pair; multiple senders can use the same emoji. |
| REQ-CHAT-31 | Hovering a message reveals a reaction button + a "more actions" menu. The reaction picker is the same emoji picker used in compose. |
| REQ-CHAT-32 | Reactions display below the message as small chips: `🎉 3` `👀 1`. Clicking a chip toggles the user's own reaction with that emoji. |
| REQ-CHAT-33 | Reactions are server-side state. They sync across devices via the chat protocol's state changes. |

## Read receipts

| ID | Requirement |
|----|-------------|
| REQ-CHAT-40 | DMs: the most recent message read by the other participant displays a small "read at HH:MM" indicator. Older messages are implied read. |
| REQ-CHAT-41 | Spaces: read receipts per-member exist server-side but are NOT surfaced inline (would clutter group chat). A "Read by N" affordance on each message expands to show which members have read it. |
| REQ-CHAT-42 | Read receipts are configurable per Space: an admin can disable read receipts for a Space; in DMs they are always on. |
| REQ-CHAT-43 | A message is considered "read" when the conversation is open in the panel AND the message is in the visible scroll area. Marking-as-read fires on the same debounce as scroll (~500ms after settling). |

## Typing indicators

| ID | Requirement |
|----|-------------|
| REQ-CHAT-50 | When a user is actively typing in a conversation, other participants see "Hans is typing…" beneath the message list. Multiple typers: "Hans and Charlotte are typing…" / "3 people are typing…". |
| REQ-CHAT-51 | Typing signal is sent over the chat ephemeral WebSocket channel (`../architecture/07-chat-protocol.md`). Send debounces: emit "typing" no more than once per 3 seconds while the compose is dirty; emit "stopped" 5 seconds after the last keystroke. |
| REQ-CHAT-52 | Typing indicators auto-clear on the receiver after 7 seconds of no further "typing" signal (covers the case where the sender's tab dies mid-typing). |
| REQ-CHAT-53 | Typing indicators are NOT persisted. They never appear in scrollback. |

## Presence

| ID | Requirement |
|----|-------------|
| REQ-CHAT-60 | Each user has a presence state: `online`, `away`, `offline`. |
| REQ-CHAT-61 | Presence changes are pushed over the chat ephemeral WebSocket channel. Online: WebSocket connected and document recently focused. Away: WebSocket connected but document hidden / no focus for >5 minutes. Offline: WebSocket disconnected. |
| REQ-CHAT-62 | Presence is shown as a small dot on member avatars in conversation lists and member lists. |
| REQ-CHAT-63 | Presence is configurable per user via `20-settings.md` (a "show me as offline" mode that suppresses outbound presence updates). When in this mode, the user appears offline to others but receives messages normally. |

## Mute and block

| ID | Requirement |
|----|-------------|
| REQ-CHAT-70 | Mute a conversation: as REQ-CHAT-07. Reversible. |
| REQ-CHAT-71 | Block a user (DM only): future messages from the blocked user are dropped at the server. Block is reversible. |
| REQ-CHAT-72 | A blocked user does not see they have been blocked; their messages just don't reach the blocker. (Standard "soft block" pattern; deliberate.) |
| REQ-CHAT-73 | Blocking is server-side state. Cross-device. |

## Search

| ID | Requirement |
|----|-------------|
| REQ-CHAT-80 | A search box at the top of the chat panel searches conversation names, member names, and message content. |
| REQ-CHAT-81 | Search-within-conversation: when a conversation is open, `/` focuses an inline search bar that filters messages in that conversation only (highlights matches; `n`/`Shift+n` cycles). |
| REQ-CHAT-82 | Cross-conversation search is full-text against `Message.body` plain-text projection. Results group by conversation. |
| REQ-CHAT-83 | Search results are paged; 50 messages per page. |

## Notifications

| ID | Requirement |
|----|-------------|
| REQ-CHAT-90 | The panel shows an unread count badge per conversation; the panel header shows total unread across all unmuted conversations. |
| REQ-CHAT-91 | A new message in a conversation that is not currently open triggers a one-shot toast in the corner ("New message in <conversation>") unless the conversation is muted. Click → opens the conversation. |
| REQ-CHAT-92 | The browser tab title shows the total unread count when there are unread messages: `(3) The suite — Mail`. |
| REQ-CHAT-93 | Browser-level push notifications (when the tab is closed) are out for v1 — they require a service worker (NG2). Revisit if NG2 changes. |

## Calls

Video calls live in their own doc: `21-video-calls.md`. From the chat panel's perspective:

| ID | Requirement |
|----|-------------|
| REQ-CHAT-100 | Each DM exposes a "Start video call" button. Triggers the call flow (`21-video-calls.md`). |
| REQ-CHAT-101 | A call lifecycle is recorded as system messages in the conversation: "Video call started — 12:34" with sender + duration. Click → details (start/end times, who joined). |
| REQ-CHAT-102 | Calls in Spaces are out of scope for v1 (group calls = SFU = `21-video-calls.md` § Out of scope). The "Start call" button is hidden in Spaces. |

## Storage and privacy

| ID | Requirement |
|----|-------------|
| REQ-CHAT-110 | All chat data lives in herold (resolved Q-chat-6). The suite never persists chat content client-side beyond the in-memory cache. |
| REQ-CHAT-111 | Chat content is not encrypted end-to-end. Same trust posture as mail: the operator running herold has access. The threat model is "external attacker on the wire", which TLS handles, not "compromised server". |
| REQ-CHAT-112 | Conversation, message, membership, and presence retention is herold's policy. The suite surfaces "this Space's retention is N days" if herold reports it. |
