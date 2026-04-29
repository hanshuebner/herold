# 02 — Mail basics

Read, compose, reply, forward, star, archive, delete. The actions every Gmail user does dozens of times per day.

This doc is a placeholder skeleton. Concrete requirements come from gmail-logger capture data once collected; until then, only the structurally obvious ones are written down. See `../notes/capture-integration.md` for how the data feeds in.

## Reading

| ID | Requirement |
|----|-------------|
| REQ-MAIL-01 | Opening a thread loads all messages in it via `Thread/get` + `Email/get`. |
| REQ-MAIL-02 | All messages in a thread are visible in one scrollable view (Gmail-style accordion), collapsed except the most recent unread message — or the most recent message if all are read. |
| REQ-MAIL-03 | Clicking a collapsed message expands it; clicking an expanded message's header collapses it. |
| REQ-MAIL-04 | Quoted reply chains can be folded with a "show quoted text" toggle. |

## Composing

| ID | Requirement |
|----|-------------|
| REQ-MAIL-10 | Compose opens as a modal window in the bottom-right corner. Multiple compose windows stack (cap: 3). |
| REQ-MAIL-11 | Recipient fields autocomplete from two sources merged at render time: JMAP Contacts (`urn:ietf:params:jmap:contacts`) as the primary source, plus a server-side seen-addresses history (REQ-MAIL-11e..m) for addresses the user has corresponded with but not saved. Both sources are server-side and sync across devices; when the same canonical email appears in both, the JMAP Contact wins and the seen-history entry is suppressed from the dropdown. |
| REQ-MAIL-11e | The seen-addresses history is a per-principal sliding window of recently-used email addresses, exposed to clients as the `SeenAddress` JMAP type on the Mail account. The window has a server-enforced cap of 500 entries; when full, the entry with the oldest `lastUsedAt` is evicted on the next write. The seen-history is purely a hint for autocomplete; it carries no semantic relationship to deliveries, threads, or contacts. |
| REQ-MAIL-11f | A `SeenAddress` carries: `id` (opaque), `email` (lower-cased canonical form, the unique key per principal), `displayName?` (the most recently observed `name` from a To / From / Cc / Bcc parsed address), `firstSeenAt`, `lastUsedAt`, `sendCount` (outbound recipient hits), `receivedCount` (inbound From hits). |
| REQ-MAIL-11g | Seed-on-send: every successful `EmailSubmission` emits a `SeenAddress` write for each distinct envelope recipient (To, Cc, and Bcc — the envelope `rcptTo` is the source). `lastUsedAt` is bumped to now; `sendCount` is incremented; `firstSeenAt` is set on first sighting. Excluded: any address matching one of the user's `Identity` emails (REQ-MAIL-11k); any address that is already a JMAP Contact at the moment of seeding. |
| REQ-MAIL-11h | Seed-on-receive: every new inbound mail committed to the user's account emits a `SeenAddress` write for the parsed `From:` address. `lastUsedAt` is bumped, `receivedCount` is incremented. Excluded: any address matching one of the user's `Identity` emails; any address that is already a JMAP Contact; messages with the SMTP envelope null sender (`<>`); messages whose `From:` matches the operator-configured postmaster / mailer-daemon allow-list (`23-nonfunctional.md` § DSN sources, or a sysconfig knob if the spec is missing); messages that carry a `List-Id:` header. |
| REQ-MAIL-11i | Auto-promotion to contacts: when an inbound mail's `From:` matches a `SeenAddress` AND the inbound message's `In-Reply-To:` references the `Message-Id` of an Email the user authored (i.e., is in a Sent-class mailbox of the user's account), the server creates a JMAP Contact for that address using the captured `displayName` and removes the matching `SeenAddress` row in the same transaction. The Contact is added to the principal's primary AddressBook. The promotion is silent — no toast, no confirmation. Messages with a `List-Id:` header never trigger auto-promotion. |
| REQ-MAIL-11j | DSN and list filters apply to both seed paths: any inbound mail with envelope `MAIL FROM: <>` (null sender) or whose `From:` is a postmaster / mailer-daemon address per the operator allow-list is excluded from `SeenAddress` writes and never auto-promotes. Any message with a `List-Id:` header is excluded from seed-on-receive and from auto-promotion. |
| REQ-MAIL-11k | Identity exclusion: addresses matching any of the principal's `Identity` emails MUST NOT appear in `SeenAddress`, on either the send or receive path. The exclusion is canonicalised (lower-case, trimmed). |
| REQ-MAIL-11l | Manual contact creation drops the matching seen-history entry: when the user (or an automated path) creates a JMAP Contact whose canonical email is already in `SeenAddress`, the server removes the `SeenAddress` row in the same transaction (or, if the contact creation came in via a separate `Contact/set`, on the next state advance for the principal). |
| REQ-MAIL-11m | Privacy: a per-principal setting "Remember recently-used addresses" (`20-settings.md` REQ-SET-...; default `true`). When the user sets it to `false`, the server immediately purges every `SeenAddress` row for that principal and stops seeding. Setting it back to `true` resumes seeding from then on; the purged history is not restored. |
| REQ-MAIL-11a | Recipients in the To, Cc, and Bcc fields render as chips. Each chip shows the display name (when available) and the email address; chips have a remove control. The free-text input area sits inline with the chips and accepts more recipients. A recipient becomes a chip ("recognized") when either (a) the user types or pastes a syntactically complete email address and commits it via `,`, `;`, Enter, Tab, or whitespace, or (b) the user picks an entry from the autocomplete dropdown (REQ-MAIL-11). |
| REQ-MAIL-11b | Manual separators that commit the pending input as a chip: comma, semicolon, Enter, Tab, and a trailing space after a complete address. Whitespace inside a structured address like `Hans Hübner <hans@huebner.org>` or `hans@huebner.org "Hans Hübner"` is preserved and does NOT split: the parser commits only when the buffer holds a complete `local@domain` token outside any `<…>` or `"…"` group. |
| REQ-MAIL-11c | Pasting one or more addresses parses them in one pass and produces one chip per recognized address. Recognized formats: bare `local@domain`, `Name <local@domain>`, and `local@domain "Name"`. The parser tolerates `,`, `;`, and newlines as separators. A paste that contains both recognized and unrecognized fragments creates chips for the recognized ones and leaves the unrecognized text in the input buffer (highlighted per REQ-MAIL-11d). |
| REQ-MAIL-11d | If the input buffer is non-empty when the user attempts to send (or blurs the field with text remaining), the suite shows an inline warning attached to the field — "Couldn't recognize: <text>" — and does not commit a chip for the unparsed text. Send is blocked until every field is either empty or contains only chips and (optionally) trailing whitespace. The warning clears as soon as the buffer is emptied or becomes parseable. |
| REQ-MAIL-12 | The From field defaults to the primary `Identity` and can be changed via dropdown. |
| REQ-MAIL-13 | Compose autosaves to Drafts every N seconds while the body is dirty (N: TBD from capture). |
| REQ-MAIL-14 | Send issues `Email/set` (create the final form, removing `$draft`) followed by `EmailSubmission/set` in one batched call with back-references. The `EmailSubmission` carries `sendAt = now + <undo-window>` (RFC 8621 §7.5); herold's outbound queue holds the message until that time. |
| REQ-MAIL-15 | Send → toast "Message sent" with Undo for the configured undo window (default 5 s; user-configurable per `20-settings.md` REQ-SET-06). Undo within the window issues `EmailSubmission/set { destroy: [<id>] }` and re-opens the compose. After the window elapses, herold sends. |
| REQ-MAIL-15a | If the user closes the tab during the undo window, the message still sends at `sendAt` because the submission is server-side. The user's "Sent" is the truth — the suite does not silently drop messages on tab close. |
| REQ-MAIL-16 | Compose supports plain-text and HTML bodies. Default mode TBD from capture. |
| REQ-MAIL-17 | File attachment is a `Blob/upload` followed by `Email/set` referencing the blob ID. |

## Reply / reply-all / forward

| ID | Requirement |
|----|-------------|
| REQ-MAIL-30 | Reply opens compose pre-populated with the original sender as `to`, original Subject prefixed with `Re: ` if not already, and original body quoted (`>` markers for plain text, `<blockquote>` for HTML). |
| REQ-MAIL-31 | Reply-all populates `to` with the original sender and `cc` with the original `to`+`cc` minus the user's own `Identity` addresses. |
| REQ-MAIL-32 | Forward populates Subject with `Fwd: `, leaves `to` empty, and quotes the original including its From / Date / Subject / To headers. |
| REQ-MAIL-33 | A successful reply / reply-all / forward sets `keywords/$answered` or `$forwarded` on the parent email per RFC 8621. |

## Per-message actions

| ID | Requirement |
|----|-------------|
| REQ-MAIL-50 | Star toggles `keywords/$flagged` on the email. Optimistic. |
| REQ-MAIL-51 | Archive removes the inbox mailbox from `mailboxIds`. Thread-level by default; per-message archive is not exposed. |
| REQ-MAIL-52 | Delete moves the thread to Trash by setting `mailboxIds` to `{<trash-id>: true}` for all emails in the thread. |
| REQ-MAIL-53 | Mark-read / mark-unread toggles `keywords/$seen`. Available on thread and on individual messages. |
| REQ-MAIL-54 | Move-to relocates a thread (or a multi-selection of threads) into a single chosen mailbox by replacing each affected email's `mailboxIds` with `{<target-id>: true}`. Surfaced as the move-to toolbar button (REQ-UI-19b, REQ-UI-51) and as drag-and-drop from the thread list onto a sidebar mailbox (REQ-UI-17). Optimistic with Undo. |

## Compose details

The toolbar, paste handling, signatures, Cc/Bcc, and send variants — the parts of compose that turn a message into something the user actually wants to send.

### Toolbar

The compose body is a ProseMirror editor (`../implementation/01-tech-stack.md`). The toolbar exposes the schema's marks and node types:

| Action | Shortcut |
|--------|----------|
| Bold | `Cmd/Ctrl+B` |
| Italic | `Cmd/Ctrl+I` |
| Underline | `Cmd/Ctrl+U` |
| Strikethrough | `Cmd/Ctrl+Shift+X` |
| Inline code | `Cmd/Ctrl+E` |
| Bullet list | toolbar only |
| Numbered list | toolbar only |
| Blockquote | `Cmd/Ctrl+Shift+>` |
| Link | `Cmd/Ctrl+K` (opens URL prompt; selection becomes link text) |
| Heading 2 / 3 | toolbar only — h1 is conventionally not used in email bodies |
| Horizontal rule | toolbar only |
| Remove formatting | `Cmd/Ctrl+\` |

| ID | Requirement |
|----|-------------|
| REQ-MAIL-80 | The toolbar appears at the bottom of the compose, beneath the body editor. (Top-of-body toolbars steal vertical real estate during typing; bottom keeps the body's start visible.) |
| REQ-MAIL-81 | Toolbar buttons reflect the current selection's mark/node state — bold lights up when the cursor is in bold text. Implementation: ProseMirror state subscription bridged to Svelte `$state` (`../architecture/06-design-system.md` § Compose window). |
| REQ-MAIL-82 | The compose schema rejects styles outside the supported set — pasting `<style>`, `<script>`, raw HTML, or arbitrary inline `style=` attributes drops the disallowed parts. The schema is defined once and shared between compose-output and inbound-sanitisation as the contract for "what the suite considers valid email HTML". |

### Paste

| ID | Requirement |
|----|-------------|
| REQ-MAIL-90 | Paste of plain text inserts at the cursor without formatting. |
| REQ-MAIL-91 | Paste of formatted (HTML) content is mapped through the compose schema: supported marks/nodes are preserved; unsupported are dropped silently (no warning toast — the user is unlikely to know what was filtered). |
| REQ-MAIL-92 | Paste of an image (clipboard image, screenshot) is uploaded via `Blob/upload` and inserted as inline content with a Content-ID reference. See `17-attachments.md` REQ-ATT-07. |
| REQ-MAIL-93 | Paste of a URL onto a text selection wraps the selection as a link to the URL. Plain paste of a URL with no selection inserts it as a link with the URL as both href and visible text. |

### Signatures

| ID | Requirement |
|----|-------------|
| REQ-MAIL-100 | Each `Identity` has an optional signature, plain text in v1. HTML signatures cut to phase 2 (`../implementation/04-simplifications-and-cuts.md`). |
| REQ-MAIL-101 | When a compose opens (new, reply, forward), the From identity's signature is appended to the body separated by the standard delimiter `\n-- \n`. The cursor is positioned above the delimiter. |
| REQ-MAIL-102 | The user can edit the signature in the compose without affecting the saved signature on the `Identity`. Editing the saved signature lives in `20-settings.md`. |
| REQ-MAIL-103 | If the From identity is changed mid-compose, the signature is replaced with the new identity's. If the user has edited the signature, a confirmation appears: "Replace your edited signature with <new identity>'s?". |

### Cc / Bcc

| ID | Requirement |
|----|-------------|
| REQ-MAIL-110 | Cc and Bcc rows are hidden by default. "Cc" / "Bcc" buttons next to the To field reveal each row. |
| REQ-MAIL-111 | Once a Cc or Bcc row has any content, it stays visible for this compose (no re-collapse). |
| REQ-MAIL-112 | "Remove yourself from this conversation" — a small affordance in the reading pane when the user is in a thread's Cc list. Adds the user's address to a per-thread "don't auto-include" set; future reply-all on this thread excludes the user from Cc. The set is `localStorage` per account, keyed by thread id. |

### Send variants

| ID | Requirement |
|----|-------------|
| REQ-MAIL-120 | Send: `Cmd/Ctrl+Enter`. The default action. |
| REQ-MAIL-121 | Send + archive (the parent thread for replies, or the most recently focused thread otherwise): `Cmd/Ctrl+Shift+Enter`. Visible in the Send button's split menu. |
| REQ-MAIL-122 | Cut for v1: send-later (scheduled send), send-and-snooze, send-with-confirmation-required. Server-side feature dependencies that we'd rather not pull in for v1. |

## Per-message context menu

The three-dot menu surfaced from each message header in the reading-pane accordion (`09-ui-layout.md` REQ-UI-21b). Items grouped by purpose; separators in the table indicate grouping.

| ID | Action | Behaviour |
|----|--------|-----------|
| REQ-MAIL-130 | Reply | Opens compose pre-populated as `30-32` describe; per-message scope (just this message, not the whole thread). |
| REQ-MAIL-131 | Forward | Opens compose populated with the message body and headers per REQ-MAIL-32. |
| REQ-MAIL-132 | Delete | Moves the **message** to Trash via `Email/set` (`mailboxIds: { <trash>: true }`). Does NOT delete the rest of the thread. Optimistic with Undo. |
| REQ-MAIL-133 | Mark as unread | Sets `keywords/$seen: null` on this message only. The thread shows unread again if any of its messages is unread. |
| REQ-MAIL-134 | Block sender | Adds the message's `From` address to the user's block list. Future messages from that address are filtered (Sieve rule auto-generated; see `04-filters.md`). Confirmation dialog before applying. |
| REQ-MAIL-135 | Report spam | Marks the message `$junk` and moves to the Spam mailbox. Sends a feedback signal to herold's spam classifier (REQ-FILT-220-style mechanism). The message's sender is NOT auto-blocked (the user can do that explicitly via REQ-MAIL-134). |
| REQ-MAIL-136 | Report phishing | Same as report-spam, plus an additional flag indicating phishing rather than generic spam. Herold may forward the report to upstream (operator policy). |
| REQ-MAIL-137 | Report illegal content | Surfaces a confirmation modal explaining the operator-side handling, then sends a report payload to herold's admin queue. Operator policy governs further escalation. Hidden if the operator hasn't enabled illegal-content reporting in herold's policy. |
| REQ-MAIL-138 | Filter messages like this | Opens the filter editor (`04-filters.md`) pre-populated with conditions derived from the message: From address (or domain), subject prefix, list-id (if present). User picks the action. |
| REQ-MAIL-139 | Translate | Cut for v1. The action is hidden until the suite ships translation. (Hidden, not greyed-out — invisible.) |
| REQ-MAIL-140 | Print | Opens the browser print dialog scoped to this message's rendered body (with sender / date / subject header). |
| REQ-MAIL-141 | Download message | Downloads the raw RFC 5322 source as `<subject>.eml` via `Blob/download` of the email's blob. |
| REQ-MAIL-142 | Show original | Opens a modal showing the raw RFC 5322 source (headers + body). Read-only, monospace, copy-to-clipboard affordance. Useful for debugging delivery and authentication. |

## Below-message actions

Pill-shaped action buttons rendered below each expanded message (`09-ui-layout.md` REQ-UI-25a), mirroring the most-used items from the per-message header.

| ID | Action | Behaviour |
|----|--------|-----------|
| REQ-MAIL-150 | Reply | Same as REQ-MAIL-130. |
| REQ-MAIL-151 | Forward | Same as REQ-MAIL-131. |
| REQ-MAIL-152 | React | Opens an emoji picker; selecting an emoji applies it as a reaction to the message. Stored server-side as an `Email.reactions` extension property — see § Reactions below and `notes/server-contract.md` § Email reactions. |

The pill buttons are the second time these actions surface for the message — once in the per-message header (REQ-UI-21b), once below the body. Both invoke the same handlers. Redundancy is intentional: the user reaches the bottom of a long message and wants to reply without scrolling back up.

## Reactions

The suite supports emoji reactions on email messages. Reactions mirror the chat reaction shape (`08-chat.md` REQ-CHAT-30..33): per-emoji lists of reactor PrincipalIds.

For users on the same herold (sender and reactor share a server), reactions are native — stored on `Email.reactions`, synced via JMAP state. For cross-server recipients, the reactor's herold sends an additional email carrying structured reaction headers; if the receiving server is herold (or another reaction-aware server), it consumes the headers and applies a native reaction; otherwise the email lands as a normal short message ("Alice reacted with 🎉 to your message") that threads with the original.

### Storage

| ID | Requirement |
|----|-------------|
| REQ-MAIL-170 | `Email.reactions` is an extension property (not standard RFC 8621). Shape: `{ "<emoji>": ["<principal-id>", ...] }`. Sparse — emojis with no current reactors are absent rather than empty arrays. |
| REQ-MAIL-171 | Adding a reaction: the suite issues `Email/set { update: { "<id>": { "reactions/<emoji>/<my-principal-id>": true } } }`. The JSON-patch path appends the user's id to the emoji's reactor list (or creates the emoji entry if absent). Removing: `... null` on the same path. |
| REQ-MAIL-172 | Reactions are **per-Email, not per-thread.** Reacting to message 3 in a thread does not propagate to message 1. The reading-pane UI shows reactions inline beneath each message. |
| REQ-MAIL-173 | Authorisation: a user can only add/remove their own principalId in any reactor list. Attempting to mutate someone else's reaction returns `forbidden` from herold. |

### Cross-server propagation

| ID | Requirement |
|----|-------------|
| REQ-MAIL-180 | When a user adds a reaction to a message whose recipients include addresses on other servers (i.e., recipients of the original email outside the reactor's herold), the reactor's herold sends an outbound reaction email to those external recipients. The reactor sees the same UX whether the original was local-only or cross-server — the propagation is an internal herold concern. |
| REQ-MAIL-181 | Reaction emails carry both structured headers (so a herold-aware receiver can apply as a native reaction) and a human-readable body fallback (so a non-herold receiver sees "Alice reacted with 🎉 to your message"). Wire format defined in `notes/server-contract.md` § Email reactions. |
| REQ-MAIL-182 | Reaction emails follow standard threading (`In-Reply-To`, `References` matching the original message). On non-herold receivers they appear as short messages in the same thread; on herold receivers the reaction is consumed and the email is suppressed from delivery. |
| REQ-MAIL-183 | Removing a reaction does NOT propagate to non-herold receivers. The remove is local to the reactor's server (and any herold receivers within the original recipient set). Rationale: a "Alice un-reacted" email to someone on a non-herold server is awkward; reactions are ephemeral signals, the asymmetry is acceptable. |
| REQ-MAIL-184 | The suite does not surface the local-vs-cross-server distinction in the UI. The user sees one "react" button; herold decides what happens internally. |

### What's reacted to

| ID | Requirement |
|----|-------------|
| REQ-MAIL-190 | The suite exposes the React affordance on every message in the reading pane. Reactions are equally available on inbound and outbound messages — the user can react to their own sent mail. |
| REQ-MAIL-191 | Mailing-list messages (`16-mailing-lists.md`) are reactable, but the cross-server propagation can fan out to many recipients. The suite surfaces a one-time confirmation when reacting to a message with `List-ID` header AND > 5 recipients: "This will send a reaction email to N people. Continue?". |

## Mute thread

| ID | Action | Behaviour |
|----|--------|-----------|
| REQ-MAIL-160 | Mute thread | Adds the thread's id to a per-account mute set. Future replies on the thread skip the inbox (auto-archive on arrival; the thread keeps growing in All Mail / labels). The mute is reversible via the same menu — "Unmute thread" — when the thread is open. |

Mute set storage: server-side via Sieve rule (`04-filters.md`) auto-generated from the mute action, OR client-local `localStorage` if Sieve is unavailable. Default: server-side via Sieve. The user-facing affordance is one click; the rule generation is internal.

## Placeholders growing from capture

> **⚠ PLACEHOLDER** — top-actions not already covered above will be added from `gmail-analysis-*.json` (`top_actions` ≥ 5 occurrences). Likely candidates: print, mute thread, report spam, mark important, snippet preview hover. Add as `REQ-MAIL-7n`+ and only re-prefix existing IDs if the area materially reorganises.
