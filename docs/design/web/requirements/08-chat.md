# 08 — Chat

Real-time conversation between the suite users. DMs (1:1) and Spaces (group rooms). Lives as a persistent panel in the suite shell, not a separate app — see `../architecture/01-system-overview.md` § Suite shape.

Closed system: no federation, no XMPP/Matrix bridges. The suite users only.

## Conversations

Two types: direct (DM, 1:1) and space (group, 2+ members).

| ID | Requirement |
|----|-------------|
| REQ-CHAT-01 | DMs are 1:1 conversations. Created on first message via the new-chat picker (REQ-CHAT-01a..d). The conversation is created server-side on first send. |
| REQ-CHAT-01a | The single new-chat entry point is the "+" button in the sidebar Chats section header (`09-ui-layout.md` REQ-UI-13h). It opens a modal picker that defaults to DM mode and offers a "Create Space" toggle to switch to multi-recipient Space creation. There is no separate "+ Space" affordance and no "Start chat with X" affordance in search (`08-chat.md` REQ-CHAT-80 is find-existing only). |
| REQ-CHAT-01b | The picker's typeahead source is the directory of Herold Principals on this server only — not the user's mail-contacts list. Each suggestion renders the principal's display name and email address. Principal IDs are never shown (REQ-CHAT-15). |
| REQ-CHAT-01c | The picker's input also accepts a free-text email address. On commit, the suite resolves the address against the directory: if it matches a Herold Principal on this server the picker accepts the recipient; if it does not, the picker shows a hard error inline ("<address> is not a Herold user on this server") and refuses to proceed. There is no "send mail instead" fallback and no out-of-server invite flow in v1. |
| REQ-CHAT-01d | If the picked DM recipient already has an existing DM with the user, the picker routes to the existing conversation rather than creating a duplicate. The existing conversation opens as the floating overlay (`09-ui-layout.md` REQ-UI-13i). |
| REQ-CHAT-02 | Spaces are group conversations with explicit membership. Created via the same picker (REQ-CHAT-01a) in "Create Space" mode: name (required), description (optional), and initial members. |
| REQ-CHAT-02a | Space creation requires at least one initial member besides the creator. The picker disables the create button until the member list is non-empty. Members are picked using the same typeahead and validation rules as DM recipients (REQ-CHAT-01b..c); the input is multi-select and renders confirmed members as chips. Promoting an existing DM to a Space (i.e. adding a third member to a DM) is out of scope for v1; the user creates a new Space instead. |
| REQ-CHAT-03 | Space members have a role: `member` or `admin`. The creator is admin. Admins can add/remove members and change the space name; members cannot. |
| REQ-CHAT-04 | A user can leave a Space at any time. Leaving emits a system message ("Hans left the Space"); the user is removed from membership. |
| REQ-CHAT-05 | An admin can remove a member; same system-message emission. |
| REQ-CHAT-06 | Conversations sort by most recent activity. Pinned conversations sort first within their type. |
| REQ-CHAT-07 | Each conversation has a mute state: muted conversations don't trigger panel notifications and don't show in the unread count, but new messages still appear when the conversation is open. |
| REQ-CHAT-15 | Principal IDs are an internal identifier and MUST NOT appear in any user-facing surface. This includes the new-chat picker, member lists, recipient chips, error messages, settings panels, profile cards, and tooltips. The canonical user-facing identifiers for a Principal are the display name and the email address; the suite renders one or both depending on context but never the principal ID. |

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
| REQ-CHAT-80 | A search box at the top of the chat panel searches conversation names, member names, and message content. Search is **find-existing only**: it surfaces conversations and messages the user already has access to. It does not offer "Start a chat with this person" — new conversations are started exclusively through the picker (REQ-CHAT-01a). |
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

## User presence and idle state

The same chat surface is consulted by the divider, mark-read, sound,
and (in future) typing-broadcast rules. Pin the user-state vocabulary
once so every downstream rule says the same thing.

| ID | Requirement |
|----|-------------|
| REQ-CHAT-180 | **Local user presence states.** At any instant the chat client classifies its local user into exactly one of three states with respect to a given conversation: `present-in-chat`, `present-elsewhere`, or `absent`. The state is computed entirely client-side from browser signals; the server does not know it (and is not asked to). The state is recomputed on every input/focus/visibility event. |
| REQ-CHAT-181 | **`present-in-chat`** — the OS-level browser window has keyboard focus (`document.hasFocus() === true`) AND the chat compose surface for **this conversation** has DOM focus (`document.activeElement` is the compose editor for this conversation). Any other state with focus on a different conversation's compose, on email, on calendar, etc. is `present-elsewhere`. |
| REQ-CHAT-182 | **`present-elsewhere`** — the OS-level browser window has keyboard focus AND the user has been active (mouse / keyboard / touch input observed) within the last `idleThresholdSeconds` window, AND `present-in-chat` does not hold. |
| REQ-CHAT-183 | **`absent`** — neither `present-in-chat` nor `present-elsewhere` holds. That is: the window does not have focus, OR no input event has been observed for `idleThresholdSeconds`. Default `idleThresholdSeconds` is 120 (2 minutes); operator config may override. |
| REQ-CHAT-184 | **Per-conversation projection.** The state vocabulary is per-conversation. For a single user there is at most one conversation in `present-in-chat` at any moment (that requires DOM focus). All other open conversations the user has are simultaneously `present-elsewhere` or `absent` per the same window/idle gate. The states are mutually exclusive **per conversation**. |

## Unread tracking and the "New" divider

The unread surface has two visible artifacts: per-conversation badges
(sidebar row + overlay-window title) and the inline "New" divider
inside an open conversation. Both are derived from one server-
authoritative datum so they cannot drift, and both are dismissed by
the same well-defined user actions.

| ID | Requirement |
|----|-------------|
| REQ-CHAT-200 | **Source of truth.** The per-conversation unread count is the server-authoritative `Conversation.unreadCount` JMAP field. It is the count of non-deleted messages in the conversation whose id is greater than `myMembership.lastReadMessageId` AND whose sender is not the current principal. Messages the current user sent themselves never count as unread. The client never derives this count locally; it only mirrors the server value. |
| REQ-CHAT-201 | **Badge consistency.** The unread count badge in the sidebar conversation row and the unread count shown in an overlay-window title bar for the same conversation MUST always render the same number. Both surfaces read `conversation.unreadCount` from the same in-memory entry; an implementation MUST NOT cache, increment, or decrement either copy independently. Muted conversations show no badge in either surface (REQ-CHAT-07). |
| REQ-CHAT-202 | **Divider visibility — primary rule.** Inside an open conversation the "New" divider is shown if and only if `unreadCount > 0` AND the user is not currently `present-in-chat` for this conversation (REQ-CHAT-181). When `present-in-chat` holds, every arriving message is auto-marked-read on receipt (REQ-CHAT-210), so `unreadCount` stays at zero and the divider does not appear. The divider is anchored at the position immediately after the message identified by `myMembership.lastReadMessageId` at the moment unread-count last transitioned from zero to non-zero (typically conversation open with pre-existing unread, or first message arrival while the user is `present-elsewhere` or `absent`). |
| REQ-CHAT-203 | **Divider does not move for messages that arrive mid-read.** While the conversation is open and the divider is shown, new messages arriving from other principals append below the divider and increment `unreadCount`; the divider's position MUST NOT move. New messages from the current principal do not affect `unreadCount` and do not move the divider. |
| REQ-CHAT-204 | **Divider re-appears when unread returns from zero.** If the divider has been cleared (count went to zero) and a later message from another principal arrives, the new message increments `unreadCount` from 0 to 1, and the divider is re-shown anchored at the post-clearance read pointer (i.e. immediately above that new message). The same per-conversation rules then govern its dismissal. |
| REQ-CHAT-205 | **Sender's own log shows no divider.** The divider is never shown when every message after `myMembership.lastReadMessageId` was sent by the current principal. This is a direct consequence of REQ-CHAT-200's "non-self" definition: such conversations have `unreadCount === 0`. |
| REQ-CHAT-206 | **Clear by focus when fully visible.** If the divider AND every unread message are simultaneously fully visible inside the message-list scroll viewport, then placing focus in the chat compose for the open conversation marks the conversation read (advances the read pointer to the latest message, drops `unreadCount` to 0, removes the divider). "Fully visible" means the divider's element AND the most recent unread message's element each have an `IntersectionObserver` `intersectionRatio === 1`. If only some unread messages are visible, focus alone does NOT clear the divider — the user has not yet seen all the new content. **Special case:** when REQ-CHAT-202 suppresses the divider because the user is `present-in-chat`, the divider precondition is satisfied trivially (there is no divider element to observe); the rule then fires when only the most recent unread message is at `intersectionRatio === 1`. |
| REQ-CHAT-207 | **Clear by sustained scroll-into-view.** If the divider is not in the viewport (typically because the conversation auto-scrolled to the latest message on open and the divider is above the visible region), and the user later scrolls so that the divider element enters the viewport AND remains continuously visible (`intersectionRatio > 0`) for at least 3000 ms, the conversation is marked read and the divider is removed. The 3000 ms timer resets whenever the divider leaves the viewport; only continuous visibility counts. This rule fires regardless of focus state. |
| REQ-CHAT-208 | **No other automatic mark-read paths.** Opening a conversation in the main pane or as an overlay window MUST NOT automatically mark it read. Hovering over messages, scrolling without satisfying REQ-CHAT-207, or clicking on a non-compose surface MUST NOT mark it read. Rules REQ-CHAT-206 and REQ-CHAT-207 are the only automatic mark-read triggers in the chat reader. **Sending a message in the conversation IS implicit engagement** and advances the read pointer to the just-sent message id (subsuming any prior unread). The user can also mark-read via an explicit "Mark as read" gesture if one exists. |
| REQ-CHAT-209 | **Mark-read state propagates immediately.** When either REQ-CHAT-206 or REQ-CHAT-207 fires the client first updates the local `Conversation.unreadCount` to 0 and removes the divider synchronously, then issues `Membership/set { lastReadMessageId }` to the server. Both badge surfaces (REQ-CHAT-201) reflect the change in the same render pass. The server response is reconciliation only; the local view does not regress if the response is slow. |
| REQ-CHAT-210 | **Auto-mark-read while `present-in-chat`.** While the user's state for a conversation is `present-in-chat` (REQ-CHAT-181), every newly-arriving message in that conversation triggers an immediate `Membership/set { lastReadMessageId: <new message id> }`. The client also updates `unreadCount` locally to 0 in the same render pass. Consequence: a user actively engaged with the conversation never accumulates a backlog and never sees the "New" divider for content that arrived while they were typing. The auto-mark-read is bypassed when the new message's sender is the current principal (their own send is implicitly read). |
| REQ-CHAT-211 | **State transitions do not retroactively rewrite history.** Transitioning into `present-in-chat` does NOT mark prior unread messages read; it only governs messages arriving from this point forward. Existing unread is dismissed by REQ-CHAT-206 or REQ-CHAT-207, never by mere presence. Conversely, transitioning out of `present-in-chat` (e.g. window blurs while compose still has DOM focus) does not eagerly write a read pointer; it just stops the auto-mark-read of REQ-CHAT-210 for subsequent arrivals. |

## Notification sounds

| ID | Requirement |
|----|-------------|
| REQ-CHAT-220 | **Sound triggers.** A notification sound is played when a new chat message arrives in a non-muted conversation AND the local user state for that conversation is `absent` (REQ-CHAT-183). Sound is suppressed for `present-in-chat` (the user is reading) and `present-elsewhere` (the user is at the machine and will see the unread badge / browser-tab title). The trigger is the message-arrival event, not the per-conversation `unreadCount` transition; this matters for back-to-back arrivals where the count was already non-zero. |
| REQ-CHAT-221 | **Sound de-bounce.** Across all conversations and all senders, the chat client plays at most one notification sound per 30 seconds. The de-bounce window is global per user-session (not per conversation) so a burst of messages from many conversations or senders does not produce a cascade. The 30 s window is wall-clock; it is not reset by user activity. The window is local to the tab — a new tab starts its own window. |
| REQ-CHAT-222 | **Sound exceptions.** Incoming-call ringtones (`21-video-calls.md`) are NOT subject to REQ-CHAT-220 / REQ-CHAT-221 — calls are inherently synchronous and have their own audio rules. Reactions, edits, deletions, typing indicators, and presence/typing pulses NEVER play sound. |
| REQ-CHAT-223 | **User-controlled sound mute.** The user can globally disable chat notification sound via the existing notifications setting panel (REQ-CHAT-90 family). When disabled, REQ-CHAT-220 short-circuits; the de-bounce window in REQ-CHAT-221 is unaffected (it tracks would-be triggers regardless, so re-enabling sound mid-session does not produce a delayed catch-up burst). |

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
