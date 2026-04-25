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
| REQ-MAIL-11 | Recipient fields autocomplete from JMAP for Contacts (`urn:ietf:params:jmap:contacts`) as the primary source, supplemented by a client-local seen-addresses history for addresses the user has corresponded with but not saved. The JMAP contacts data syncs across devices; the local supplement is per-browser. Resolved Q9. |
| REQ-MAIL-12 | The From field defaults to the primary `Identity` and can be changed via dropdown. |
| REQ-MAIL-13 | Compose autosaves to Drafts every N seconds while the body is dirty (N: TBD from capture). |
| REQ-MAIL-14 | Send issues `Email/set` (create the final form, removing `$draft`) followed by `EmailSubmission/set` in one batched call with back-references. The `EmailSubmission` carries `sendAt = now + <undo-window>` (RFC 8621 §7.5); herold's outbound queue holds the message until that time. |
| REQ-MAIL-15 | Send → toast "Message sent" with Undo for the configured undo window (default 5 s; user-configurable per `20-settings.md` REQ-SET-06). Undo within the window issues `EmailSubmission/set { destroy: [<id>] }` and re-opens the compose. After the window elapses, herold sends. |
| REQ-MAIL-15a | If the user closes the tab during the undo window, the message still sends at `sendAt` because the submission is server-side. The user's "Sent" is the truth — tabard does not silently drop messages on tab close. |
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
| REQ-MAIL-82 | The compose schema rejects styles outside the supported set — pasting `<style>`, `<script>`, raw HTML, or arbitrary inline `style=` attributes drops the disallowed parts. The schema is defined once and shared between compose-output and inbound-sanitisation as the contract for "what tabard considers valid email HTML". |

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

## Placeholders growing from capture

> **⚠ PLACEHOLDER** — top-actions not already covered above will be added from `gmail-analysis-*.json` (`top_actions` ≥ 5 occurrences). Likely candidates: print, mute thread, report spam, mark important, snippet preview hover. Add as `REQ-MAIL-7n`+ and only re-prefix existing IDs if the area materially reorganises.
