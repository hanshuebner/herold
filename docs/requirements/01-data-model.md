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
| Important | `Email.keywords["$important"]` | Non-standard; tabard treats as advisory only. |
| Snooze | Server-side: `$snoozed` keyword + `snoozedUntil` field | Tabard requires herold to expose this; see `../notes/server-contract.md`. |
| Filter / Rule | Sieve script via RFC 9007 | Stored on the server; tabard authors them through a UI. |
| From-address / alias | `Identity` | Tabard surfaces all `Identity` objects in the From picker. |

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
| REQ-MODEL-08 | Non-standard keywords (`$important`, `$snoozed`) are advisory: tabard reads and displays them. `$snoozed` is also acted upon (see `06-snooze.md`); `$important` is not a sort key in v1. |

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
