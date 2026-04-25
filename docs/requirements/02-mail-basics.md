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
| REQ-MAIL-11 | Recipient fields support autocompletion. Source TBD — see `../notes/open-questions.md`. |
| REQ-MAIL-12 | The From field defaults to the primary `Identity` and can be changed via dropdown. |
| REQ-MAIL-13 | Compose autosaves to Drafts every N seconds while the body is dirty (N: TBD from capture). |
| REQ-MAIL-14 | Send issues `Email/set` (create the final form, removing `$draft`) followed by `EmailSubmission/set` in one batched call with back-references. |
| REQ-MAIL-15 | Send → toast "Message sent" with Undo for 5 seconds; Undo cancels via `EmailSubmission/set destroy` and re-opens the compose. See `11-optimistic-ui.md`. |
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

## Placeholders growing from capture

> **⚠ PLACEHOLDER** — top-actions not already covered above will be added from `gmail-analysis-*.json` (`top_actions` ≥ 5 occurrences). Likely candidates: print, mute thread, report spam, mark important, snippet preview hover. Add as `REQ-MAIL-7n`+ and only re-prefix existing IDs if the area materially reorganises.
