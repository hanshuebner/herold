# 01 — Data model

Mapping between Gmail's user-facing concepts and JMAP objects (RFC 8621 §1, §4). Where the two diverge, this doc records which side wins.

## Core entity mapping

| Gmail concept | JMAP object | Notes |
|---------------|-------------|-------|
| Thread | `Thread` | Conversation grouping; `Thread.emailIds[]` enumerates members. |
| Message | `Email` | One RFC 5322 message. |
| Label | `Mailbox` | Each Gmail label is one JMAP `Mailbox`. RFC 8621 §2 explicitly allows a message to belong to multiple mailboxes; Gmail relies on it. |
| System label (Inbox, Sent, Drafts, Spam, Trash, Archive) | `Mailbox` with `role` | JMAP roles: `inbox`, `sent`, `drafts`, `junk`, `trash`, `archive`. |
| Star | `Email.keywords["$flagged"]` | Standard JMAP keyword. |
| Unread | `Email.keywords["$seen"]` (inverted) | Standard JMAP keyword. |
| Important | `Email.keywords["$important"]` | Non-standard; the suite treats as advisory only. |
| Snooze | Server-side: `$snoozed` keyword + `snoozedUntil` field | The suite requires herold to expose this; see `../notes/server-contract.md`. |
| Filter / Rule | Sieve script via RFC 9007 | Stored on the server; the suite authors them through a UI. |
| From-address / alias | `Identity` | The suite surfaces all `Identity` objects in the From picker. |

## Requirements

| ID | Requirement |
|----|-------------|
| REQ-MODEL-01 | The thread is the primary unit of display in all list views. |
| REQ-MODEL-02 | An action applied to a thread (archive, label, snooze) applies to all messages in the thread by default. Per-message actions are only available with the thread open. |
| REQ-MODEL-03 | The client uses `Thread/get` to expand a thread's message list, not iterating `Email/query` per thread. |
| REQ-MODEL-04 | The client treats Gmail labels and JMAP mailboxes as the same thing; there is no separate "folder" UI. |
| REQ-MODEL-05 | The client supports nested labels via `Mailbox.parentId` and renders them as an indented tree at least 3 levels deep. |
| REQ-MODEL-06 | The client surfaces all `Identity` objects belonging to the account as selectable From addresses in compose. |
| REQ-MODEL-07 | Standard JMAP keywords (`$seen`, `$flagged`, `$answered`, `$forwarded`, `$draft`) are the source of truth for read/star/answered/forwarded/draft state. |
| REQ-MODEL-08 | Non-standard keywords (`$important`, `$snoozed`) are advisory: the suite reads and displays them. `$snoozed` is also acted upon (see `06-snooze.md`); `$important` is not a sort key in v1. |

## JMAP method mapping

The actual call shapes. Every Gmail action in scope reduces to one of these.

| Operation | JMAP method(s) |
|-----------|----------------|
| Load inbox | `Email/query` (filter: `inMailbox=<inbox-id>`, sort: `receivedAt desc`) → `Email/get` |
| Load thread | `Thread/get` → `Email/get` (for `emailIds`) |
| Archive thread | `Email/set` (remove inbox mailbox from `mailboxIds`) |
| Apply label | `Email/set` (add label mailbox to `mailboxIds`) |
| Remove label | `Email/set` (remove label mailbox from `mailboxIds`) |
| Delete | `Email/set` (set `mailboxIds` to `{<trash-id>: true}`) |
| Mark read | `Email/set` (`keywords/$seen: true`) |
| Mark unread | `Email/set` (`keywords/$seen: null`) |
| Star | `Email/set` (`keywords/$flagged: true`) |
| Search | `Email/query` (filter: `text=<query>` + structured operators) → `Email/get` |
| Send | `Email/set` (create draft Email) → `EmailSubmission/set` |
| Save draft | `Email/set` (create or update Email in `drafts` mailbox with `$draft` keyword) |
| Sync updates | `Email/changes` (since `state`) → `Email/get` for changed IDs |
| Get labels | `Mailbox/get` |
| Create label | `Mailbox/set` (create) |
| Delete label | `Mailbox/set` (destroy) |
| Manage filters | `Sieve/set`, `Sieve/get` (RFC 9007) |

Most user actions become a single batched JMAP call with back-references; see `../architecture/02-jmap-client.md`.

## Threading

The suite does NOT decide threading. Herold does, when it ingests messages — its `internal/store/` code groups by `Message-ID` / `In-Reply-To` / `References` per RFC 5256, exposes the result via JMAP `Thread`. The suite renders what `Thread/get` returns.

That said, the thread shape has subtleties the suite must handle:

| ID | Requirement |
|----|-------------|
| REQ-MODEL-20 | Subject normalisation. The thread persists across reply prefixes (`Re:`, `Aw:`, `RE:`, `RES:`), forward prefixes (`Fwd:`, `Fw:`, `WG:`), and bracketed list tags (`[List-Name]`). The suite's rendered thread subject strips these prefixes; the raw subject is in raw-headers view. See also `16-mailing-lists.md` REQ-LIST-30. |
| REQ-MODEL-21 | Messages in a thread render in chronological order by `receivedAt`, regardless of reply chain shape. Reply-chain rendering (indented tree) is cut for v1 in favour of the linear accordion (`02-mail-basics.md` REQ-MAIL-02). |
| REQ-MODEL-22 | Broken-chain messages — those whose `In-Reply-To` points at a missing parent, or whose `References` chain doesn't fully resolve in the user's mailboxes — render normally in the thread. A subtle indicator ("may not be a direct reply") appears only when the missing parent was within the user's mailboxes (i.e. it was deleted), not for first-message-from-this-person cases. |
| REQ-MODEL-23 | Subject-change-within-thread (RFC 5256 says: new thread). Herold's policy applies; the suite does not second-guess. If herold groups messages with different subjects into one Thread (Gmail-style), the suite renders that Thread; if herold splits, the suite renders the split. |

## Email properties the suite reads

The properties requested in `Email/get` calls. Listed for clarity since they're spread across several requirement docs.

- **Identification**: `id`, `blobId`, `threadId`, `mailboxIds`, `keywords`.
- **Envelope-ish**: `from`, `to`, `cc`, `bcc`, `replyTo`, `subject`, `sentAt`, `receivedAt`, `inReplyTo`, `references`.
- **Content**: `preview` (snippet fallback), `bodyValues` (plain-text body and HTML body content), `bodyStructure` (MIME tree), `textBody`, `htmlBody`, `attachments`, `hasAttachment`.
- **Headers the suite parses by name**: `header:List-ID:asText`, `header:List-Unsubscribe:asAddresses`, `header:List-Unsubscribe-Post:asText`, `header:List-Post:asText`, `header:List-Archive:asText`, `header:Authentication-Results:asText`, `header:Reply-To:asAddresses`.
- **Custom**: `keywords/$snoozed`, the snoozedUntil extension property (`06-snooze.md` REQ-SNZ-13), the `signature` extension on `Identity` (`20-settings.md` REQ-SET-03), the `reactions` extension on `Email` (`02-mail-basics.md` § Reactions).

A typical view-load batch fetches a stable subset; per-message extras (full bodyValues, full attachments) come on demand when the user opens the message.
