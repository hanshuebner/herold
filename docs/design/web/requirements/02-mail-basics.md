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
| REQ-MAIL-11 | Recipient fields autocomplete from three sources merged at render time: JMAP Contacts (`urn:ietf:params:jmap:contacts`) as the primary source, the directory of Herold Principals on this server (REQ-MAIL-11n..s) for users on this server the user has not previously corresponded with, and a server-side seen-addresses history (REQ-MAIL-11e..m) for addresses the user has corresponded with but not saved. All three sources are server-side and sync across devices. When the same canonical email appears in more than one source, the higher-priority source wins (Contacts > Principals > SeenAddress) and the lower-priority entries are suppressed from the dropdown. |
| REQ-MAIL-11e | The seen-addresses history is a per-principal sliding window of recently-used email addresses, exposed to clients as the `SeenAddress` JMAP type on the Mail account. The window has a server-enforced cap of 500 entries; when full, the entry with the oldest `lastUsedAt` is evicted on the next write. The seen-history is purely a hint for autocomplete; it carries no semantic relationship to deliveries, threads, or contacts. |
| REQ-MAIL-11f | A `SeenAddress` carries: `id` (opaque), `email` (lower-cased canonical form, the unique key per principal), `displayName?` (the most recently observed `name` from a To / From / Cc / Bcc parsed address), `firstSeenAt`, `lastUsedAt`, `sendCount` (outbound recipient hits), `receivedCount` (inbound From hits). |
| REQ-MAIL-11g | Seed-on-send: every successful `EmailSubmission` emits a `SeenAddress` write for each distinct envelope recipient (To, Cc, and Bcc — the envelope `rcptTo` is the source). `lastUsedAt` is bumped to now; `sendCount` is incremented; `firstSeenAt` is set on first sighting. Excluded: any address matching one of the user's `Identity` emails (REQ-MAIL-11k); any address that is already a JMAP Contact at the moment of seeding. |
| REQ-MAIL-11h | Seed-on-receive: every new inbound mail committed to the user's account emits a `SeenAddress` write for the parsed `From:` address. `lastUsedAt` is bumped, `receivedCount` is incremented. Excluded: any address matching one of the user's `Identity` emails; any address that is already a JMAP Contact; messages with the SMTP envelope null sender (`<>`); messages whose `From:` matches the operator-configured postmaster / mailer-daemon allow-list (`23-nonfunctional.md` § DSN sources, or a sysconfig knob if the spec is missing); messages that carry a `List-Id:` header. |
| REQ-MAIL-11i | Auto-promotion to contacts: when an inbound mail's `From:` matches a `SeenAddress` AND the inbound message's `In-Reply-To:` references the `Message-Id` of an Email the user authored (i.e., is in a Sent-class mailbox of the user's account), the server creates a JMAP Contact for that address using the captured `displayName` and removes the matching `SeenAddress` row in the same transaction. The Contact is added to the principal's primary AddressBook. The promotion is silent — no toast, no confirmation. Messages with a `List-Id:` header never trigger auto-promotion. |
| REQ-MAIL-11j | DSN and list filters apply to both seed paths: any inbound mail with envelope `MAIL FROM: <>` (null sender) or whose `From:` is a postmaster / mailer-daemon address per the operator allow-list is excluded from `SeenAddress` writes and never auto-promotes. Any message with a `List-Id:` header is excluded from seed-on-receive and from auto-promotion. |
| REQ-MAIL-11k | Identity exclusion: addresses matching any of the principal's `Identity` emails MUST NOT appear in `SeenAddress`, on either the send or receive path. The exclusion is canonicalised (lower-case, trimmed). |
| REQ-MAIL-11l | Manual contact creation drops the matching seen-history entry: when the user (or an automated path) creates a JMAP Contact whose canonical email is already in `SeenAddress`, the server removes the `SeenAddress` row in the same transaction (or, if the contact creation came in via a separate `Contact/set`, on the next state advance for the principal). |
| REQ-MAIL-11m | Privacy: a per-principal setting "Remember recently-used addresses" (`20-settings.md` REQ-SET-...; default `true`). When the user sets it to `false`, the server immediately purges every `SeenAddress` row for that principal and stops seeding. Setting it back to `true` resumes seeding from then on; the purged history is not restored. |
| REQ-MAIL-11n | Directory autocomplete source. The compose recipient autocomplete queries the directory of Herold Principals on this server via JMAP `Principal/query` with `filter.textPrefix` (the same method consumed by `08-chat.md` REQ-CHAT-01b). The query is gated on the dedicated capability `https://netzhansa.com/jmap/directory`; when the capability is absent from the JMAP session descriptor, the suite issues no Principal/query call and the dropdown falls back transparently to the two-source merge of Contacts and SeenAddress. |
| REQ-MAIL-11o | Trigger and rate limiting. The directory query fires after the user has typed two or more characters into a recipient field, debounced 150 ms after the last keystroke. At most one Principal/query is in flight per recipient field at any time; superseded queries are cancelled by the suite (the Principal/query response is discarded if a newer prefix has been issued). The query is cancelled when the input becomes empty, when the field loses focus, or when the user commits a chip. |
| REQ-MAIL-11p | Display shape. Principal suggestions render with display name and primary email address — the same shape as Contact suggestions (`REQ-MAIL-11a`) — so the user does not need to learn which source produced which row. The dropdown shows no source label by default. Principal IDs MUST NOT appear in any user-facing surface (`08-chat.md` REQ-CHAT-15 applies). |
| REQ-MAIL-11q | Identity exclusion. Principal suggestions whose email matches one of the requesting user's own `Identity` emails MUST be suppressed (parallel to REQ-MAIL-11k for SeenAddress). The user cannot autocomplete their own address into To, Cc, or Bcc from any source. |
| REQ-MAIL-11r | Privacy and operator policy. The directory autocomplete is single-tenant — every Principal on this herold is a candidate, consistent with NG3 (`02-identity-and-auth.md`). Operators who do not want this surface in their deployment disable the capability at the server; when disabled, REQ-MAIL-11n's fallback applies. There is no per-Principal "hide from directory" toggle in v1; group-restricted directory views are deferred. |
| REQ-MAIL-11s | Result cap and ranking. The Principal/query response is capped at 10 candidates per query (the suite passes `limit: 10` and the server enforces an upper bound). Ranking inside the directory result is the server's responsibility (prefix match on display name and email local-part, with display-name matches preferred); the suite renders the order it receives. The merged dropdown across the three sources caps at 10 rows total — when sources overflow, the per-source caps are: Contacts up to 10, Principals up to 5, SeenAddress up to 5, with the priority ordering of REQ-MAIL-11 applied to deduplicate before truncation. |
| REQ-MAIL-11a | Recipients in the To, Cc, and Bcc fields render as chips. Each chip shows the display name (when available) and the email address; chips have a remove control. The free-text input area sits inline with the chips and accepts more recipients. A recipient becomes a chip ("recognized") when either (a) the user types or pastes a syntactically complete email address and commits it via `,`, `;`, Enter, Tab, or whitespace, or (b) the user picks an entry from the autocomplete dropdown (REQ-MAIL-11). |
| REQ-MAIL-11b | Manual separators that commit the pending input as a chip: comma, semicolon, Enter, Tab, and a trailing space after a complete address. Whitespace inside a structured address like `Hans Hübner <hans@huebner.org>` or `hans@huebner.org "Hans Hübner"` is preserved and does NOT split: the parser commits only when the buffer holds a complete `local@domain` token outside any `<…>` or `"…"` group. |
| REQ-MAIL-11c | Pasting one or more addresses parses them in one pass and produces one chip per recognized address. Recognized formats: bare `local@domain`, `Name <local@domain>`, and `local@domain "Name"`. The parser tolerates `,`, `;`, and newlines as separators. A paste that contains both recognized and unrecognized fragments creates chips for the recognized ones and leaves the unrecognized text in the input buffer (highlighted per REQ-MAIL-11d). |
| REQ-MAIL-11d | If the input buffer is non-empty when the user attempts to send (or blurs the field with text remaining), the suite shows an inline warning attached to the field — "Couldn't recognize: <text>" — and does not commit a chip for the unparsed text. Send is blocked until every field is either empty or contains only chips and (optionally) trailing whitespace. The warning clears as soon as the buffer is emptied or becomes parseable. |
| REQ-MAIL-11t | **Blur commits a complete address.** When the recipient input buffer holds text that parses as a structurally complete address (per REQ-MAIL-11b's grammar) and the field loses focus — e.g. the user clicks into Subject, Cc, Bcc, the body, or any other surface — the buffer commits to a chip exactly as if the user had pressed Tab. This is symmetric with the explicit-separator commit paths (REQ-MAIL-11a/b) and prevents the typed address from silently being lost when the user moves on without pressing a separator. If the buffer is non-empty but NOT structurally complete on blur, REQ-MAIL-11d's warning path applies instead. |
| REQ-MAIL-12 | The From field defaults to the primary `Identity` and can be changed via dropdown. |
| REQ-MAIL-13 | Compose autosaves to Drafts every N seconds while the body is dirty (N: TBD from capture). |
| REQ-MAIL-14 | Send issues `Email/set` (create the final form, removing `$draft`) followed by `EmailSubmission/set` in one batched call with back-references. The `EmailSubmission` carries `sendAt = now + <undo-window>` (RFC 8621 §7.5); herold's outbound queue holds the message until that time. |
| REQ-MAIL-15 | Send → toast "Message sent" with Undo for the configured undo window (default 5 s; user-configurable per `20-settings.md` REQ-SET-06). Undo within the window issues `EmailSubmission/set { destroy: [<id>] }` and re-opens the compose. After the window elapses, herold sends. |
| REQ-MAIL-15a | If the user closes the tab during the undo window, the message still sends at `sendAt` because the submission is server-side. The user's "Sent" is the truth — the suite does not silently drop messages on tab close. |
| REQ-MAIL-16 | Compose supports plain-text and HTML bodies. Default mode TBD from capture. |
| REQ-MAIL-17 | File attachment is a `Blob/upload` followed by `Email/set` referencing the blob ID. |
| REQ-MAIL-18 | **Send-without-content warning.** Pressing Send when the subject and/or the body is empty shows a confirm dialog ("Send without a subject?", "Send without a body?", or "Send without a subject and body?") with confirm/cancel actions. The user can dismiss the warning and send anyway; the warning does not block. |
| REQ-MAIL-18a | **Inline images count as body content.** A compose body whose visible contribution is one or more `<img>` elements (with no surrounding text beyond the auto-inserted signature) is NOT empty for the purpose of REQ-MAIL-18. The warning fires only when the body has neither rendered text (after stripping the auto-inserted signature, REQ-MAIL-19) nor any `<img>` element. A message of "just pictures" is intentional content and must not be flagged. |
| REQ-MAIL-19 | The auto-inserted signature is treated as not-user-authored for the purpose of REQ-MAIL-18: a body that contains only the signature counts as empty. (Pulled out of REQ-MAIL-18 so the signature exclusion does not need restating in REQ-MAIL-18a.) |
| REQ-MAIL-20 | **Autosave reuses the same Email id and rewrites the body.** Each compose autosave (and the eventual Send) re-uses the existing draft's Email id by issuing an `Email/set { update: { "<id>": { ... } } }` carrying the full body (`bodyValues` + `textBody` / `htmlBody` / `bodyStructure`) and headers. The suite never destroys the old draft and recreates it. Server contract: when an `Email/set` update payload contains any body field (`bodyValues`, `textBody`, `htmlBody`, `bodyStructure`) or any top-level header (`subject`, `from`, `to`, `cc`, `bcc`, `replyTo`, `inReplyTo`, `references`, `messageId`, `sentAt`), herold rebuilds the underlying RFC 5322 blob from the patch and replaces the message's body in one transaction. Without this rebuild the suite's first autosave snapshot would survive every later edit and Send would deliver the wrong body — most visibly, inline images inserted after the first autosave would arrive missing. |
| REQ-MAIL-21 | Reader hides inline images from the attachment list. An attachment whose Content-Disposition is `inline` and whose `Content-ID` is referenced by a `cid:` URL in the rendered HTML body is shown only inline; it must not appear as a chip in the attachment row. Other attachments (including image attachments not referenced by the body) appear in the attachment row. |
| REQ-MAIL-22 | Reader scales inline images to fit the viewport. Inline `<img>` rendered inside the message body has an effective `max-width: 100%` (capped at the body column width) and preserves aspect ratio; explicit `width=` / `height=` attributes or inline styles in the source HTML override the cap. |
| REQ-MAIL-23 | Reader provides a view/download overlay on every attachment chip. Hovering or focusing an attachment reveals a "View" action (where applicable: image, PDF, plain text) and a "Download" action. Clicking an image attachment opens it in a lightbox; clicking a PDF opens an in-browser preview pane (no native viewer launch). Other types fall through to Download. |
| REQ-MAIL-24 | Composer scales inline image previews to viewport width. An inline `<img>` placed in compose renders at most at the editor column width (`max-width: 100%`), regardless of the source image's pixel dimensions. The full-resolution bytes are sent unchanged in the outbound MIME — preview scaling is purely a CSS-time visual cap. |

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

## External SMTP submission per Identity (v1)

The suite v1 surface for the server-side feature defined in `../../server/requirements/02-identity-and-auth.md` § External SMTP submission per Identity (v1). Outbound for an Identity with submission credentials is routed through the configured external endpoint; inbound continues to flow into the local mailbox via operator-arranged forwarding at the external provider. This is the v1 slice; the broader bidirectional "external mail accounts" feature (next section) is deferred.

| ID | Requirement |
|----|-------------|
| REQ-MAIL-SUBMIT-01 | The Identity edit dialog (settings, alongside REQ-SET-02 / REQ-SET-03 in `20-settings.md`) gains an "External SMTP submission" section. Default is "Use this server (recommended)" — submission goes through herold's outbound queue. The user MAY toggle to "Use an external SMTP server" and supply the credentials. The toggle is per-Identity, so a principal with multiple Identities can mix local and external submission across them. |
| REQ-MAIL-SUBMIT-02 | Provider auto-detection. If the Identity's email domain matches a provider for which the operator has configured OAuth client credentials at the system level (Gmail / Google Workspace, Microsoft 365), the dialog offers a one-click OAuth flow ("Sign in with Google", "Sign in with Microsoft") that, on success, fills the submission config without the user typing host, port, or password. The OAuth flow is server-mediated (REQ-AUTH-EXT-SUBMIT-03); the suite never holds OAuth client credentials. When auto-detection is unavailable, the dialog falls back transparently to manual entry. |
| REQ-MAIL-SUBMIT-03 | Manual entry. The dialog asks for host, port, security mode (implicit-TLS / STARTTLS / plain), auth method (password / app-specific password / OAuth via custom token endpoint), and the credential. The server's `PUT /api/v1/identities/{id}/submission` endpoint validates the credentials by performing a probe submission of an empty test message — failures surface inline before the dialog closes, with a recognisable error category (`auth-failed` / `unreachable` / `permanent-reject`). |
| REQ-MAIL-SUBMIT-04 | State surface. The Settings list of Identities renders each Identity with a small badge: nothing for the default ("Use this server"), "External" for Identities with submission credentials configured. When the per-Identity submission state (REQ-AUTH-EXT-SUBMIT-07) is `auth-failed` or `unreachable`, the badge is replaced by an attention-coloured warning, and clicking it opens the Identity edit dialog scrolled to the submission section. |
| REQ-MAIL-SUBMIT-05 | Compose's From picker shows external-submission Identities alongside local Identities. A small icon next to the Identity in the picker indicates external submission so the user can disambiguate by transport at a glance (cosmetic, not blocking). |
| REQ-MAIL-SUBMIT-06 | Failure surface in compose. When a `EmailSubmission/set` for an external-submission Identity reports failure (REQ-AUTH-EXT-SUBMIT-05), the suite shows a transient toast with the failure category ("Authentication failed", "External server unreachable", "Rejected by external server: <reason>"). For `auth-failed` the toast offers a one-click "Re-authenticate" action that opens the Settings flow scoped to that Identity. The draft is preserved in Drafts so the user can retry after re-auth. |
| REQ-MAIL-SUBMIT-07 | Inbound surface. The suite does NOT ship a UI for inbound external mail in v1. Inbound is operator-configured via the external provider's own forwarding settings; the suite shows the user's local mailbox unchanged. Operator setup is documented in the operator manual (TBD path). The suite makes no assumption about whether an Identity with external submission also has external inbound — they are independent. |
| REQ-MAIL-SUBMIT-08 | Privacy. Submission credentials never appear in any JMAP response, any compose draft, or any client-visible state surface. The Settings dialog shows masked previews ("password: ********", "OAuth: connected as alice@gmail.com") for confirmation and re-authentication; the suite never reads or transmits the underlying credential material. |

## External mail accounts (deferred)

A user MAY want to operate the suite as a unified inbox over more than one mail account: their primary herold-hosted address plus one or more external accounts (a personal Gmail, a work Microsoft 365 mailbox, an IMAP/SMTP account at another provider). v1 supports only the primary local account; the requirements below scope a future "external accounts" feature. They are recorded now to keep the v1 Identity model and the compose UI from painting themselves into a corner that excludes external accounts. **Implementation deferred — not scheduled for v1.** Server-side counterpart: `../../server/requirements/02-identity-and-auth.md` § External transport identities (deferred).

| ID | Requirement |
|----|-------------|
| REQ-MAIL-EXT-01 | A principal MAY add one or more **external mail accounts** to their herold account. Each external account has its own email address, IMAP fetch endpoint, and SMTP submission endpoint, configured by the user. Each external account exposes one or more `Identity` objects (the From-addresses the user can send as via that account; commonly one, but a Gmail "send as" alias becomes a separate Identity bound to the same external account). |
| REQ-MAIL-EXT-02 | An external account is created in Settings (`20-settings.md`). Required fields: account display name, primary email address, IMAP server (host, port, security: implicit-TLS / STARTTLS / plain), SMTP submission server (host, port, security), authentication method (password or OAuth 2.0), credential. Common providers (Gmail, Microsoft 365) are recognised by email-domain heuristics and offer a one-click OAuth flow that fills the rest; manual entry is always available. |
| REQ-MAIL-EXT-03 | External-account credentials are stored encrypted at rest by herold. Passwords are encrypted with a server-managed data key (per the server's secrets-handling rules); OAuth refresh tokens are stored similarly and refreshed automatically. Refresh failures surface as an `authentication-failed` state on the account; fetch and submit are paused until the user re-authenticates from Settings. |
| REQ-MAIL-EXT-04 | The session descriptor's `accounts` map (RFC 8620 §2) MUST enumerate one JMAP account per external account in addition to the local account. Each external account has its own `accountCapabilities`, its own state strings, its own `Mailbox` tree, and its own `Identity` set. The suite uses RFC 8620 multi-account semantics — there is no aggregation in a single account — so cross-account safety properties (mailboxes never mix, state advances are independent, Sieve scripts are per-account) are guaranteed by the protocol shape. |
| REQ-MAIL-EXT-05 | Inbound — herold mirrors external-account mail into the local store on the user's behalf. The mirror is maintained via long-lived IMAP IDLE; messages, mailboxes, and flag state are synchronised in both directions (read state, flag state, and mailbox membership changes the user makes in the suite are pushed back to the external IMAP server). Threads, search, snooze, labels, and Sieve scripts work over mirrored messages exactly as they do for locally-delivered mail. |
| REQ-MAIL-EXT-06 | Outbound — submission for an external Identity goes via the configured external SMTP submission endpoint, not via herold's outbound queue. The external server's authentication and deliverability posture (its SPF/DKIM/DMARC alignment, its rate limits) governs delivery; herold does not re-author or re-sign. The suite's `EmailSubmission` state for an external Identity reflects the external-submission outcome (delivered / temp-fail / perm-fail) as reported back through the external account's transport. |
| REQ-MAIL-EXT-07 | Compose surfaces external-account Identities in the From picker (REQ-MAIL-12, REQ-MODEL-06) alongside local Identities, grouped by account so the user can disambiguate when display names collide. Switching the From identity mid-compose moves the draft from one JMAP account to the other and re-targets autosave (`19-drafts.md`). Drafts written against an external account live under that account's drafts mailbox, on the external server. |
| REQ-MAIL-EXT-08 | Account-state surface. Each external account exposes one of: `connected` (fetch IDLE established, submission reachable), `connecting`, `authentication-failed` (credential or token expired; user must re-authenticate), `degraded` (one of fetch/submit is failing, the other is up), `disabled` (user-paused). State is read by Settings and by an in-app status indicator in the chrome when any account is in a non-`connected` state. |
| REQ-MAIL-EXT-09 | Per-account toggle. The user can disable an external account from Settings without deleting it: fetch and submit are paused, mirrored mail remains accessible read-only, the account is hidden from the From picker. Re-enabling resumes fetch and submit. Disabled accounts do not contribute to the unified inbox count. |
| REQ-MAIL-EXT-10 | Removal. Deleting an external account purges (a) stored credentials and OAuth tokens, (b) the IDLE fetch session, and (c) the JMAP account descriptor. The user is asked at remove time whether to also purge the mirrored mail; default is "keep mirror" (the mail stays accessible in a read-only archived account until the user explicitly purges it). The external server's mailbox is never modified by removal. |
| REQ-MAIL-EXT-11 | Unified views. The suite's primary inbox view is a unified merge across all enabled accounts (local + external) sorted by `receivedAt`. Account-scoped views drill into a single account via the sidebar (one section per account). Search defaults to all enabled accounts; per-account search is reachable from the search bar's account-scope dropdown. Label / mailbox operations inside an account-scoped view target only that account. |
| REQ-MAIL-EXT-12 | Cross-account constraints. A reply or forward to a thread that lives in account A defaults the From identity to an Identity on account A so the conversation continues on the same transport; the user can override the From via the picker, in which case the suite warns inline ("Sending from <other account>; this reply will not appear in <account A>'s sent folder"). A move between mailboxes never crosses accounts in v1 (cross-account move is a copy + delete, deferred). |
| REQ-MAIL-EXT-13 | Settings surface. `20-settings.md` gains a section "External accounts" listing each external account with its display name, primary email, state, last-sync timestamp, and per-account actions (re-authenticate, pause, remove). Adding a new account is a guided flow: address input → provider detection → OAuth or manual transport config → connectivity probe → mirror initialisation. |
| REQ-MAIL-EXT-14 | Capability gating. External-accounts support is gated on `https://netzhansa.com/jmap/external-accounts` advertised at the JMAP session level. When the capability is absent, the Settings entry point is hidden and no external accounts are surfaced; principals migrating between deployments do not see broken UI for accounts that the new server cannot host. |

## Placeholders growing from capture

> **⚠ PLACEHOLDER** — top-actions not already covered above will be added from `gmail-analysis-*.json` (`top_actions` ≥ 5 occurrences). Likely candidates: print, mute thread, report spam, mark important, snippet preview hover. Add as `REQ-MAIL-7n`+ and only re-prefix existing IDs if the area materially reorganises.
