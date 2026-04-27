# 03 — Labels

Labels are JMAP `Mailbox` objects (per `01-data-model.md`). A message belongs to one or more labels; system labels (Inbox, Sent, Drafts, Spam, Trash, Archive) are mailboxes with the corresponding standard `role`.

## Label management (CRUD)

| ID | Requirement |
|----|-------------|
| REQ-LBL-01 | User can create a label. Required field: name. Optional: parent label, colour. |
| REQ-LBL-02 | User can rename a label. |
| REQ-LBL-03 | User can delete a label. The client warns if the label contains threads. Deleting a label does NOT delete its messages — it removes the mailbox from each affected `Email.mailboxIds`. |
| REQ-LBL-04 | User can assign a colour to a label from a fixed palette of ≥ 12 colours. |
| REQ-LBL-05 | User can nest a label under a parent label, at least 3 levels deep. |
| REQ-LBL-06 | The label list in the sidebar is sorted: system labels first (fixed order), then user labels alphabetically within each parent. |
| REQ-LBL-06a | System mailbox order in the sidebar (`09-ui-layout.md` REQ-UI-13b): Inbox → Snoozed → Important → Sent → Drafts → All Mail. Spam, Trash, and any further system folders sit under a "More" expander, default collapsed. |
| REQ-LBL-07 | Each label shows an unread count badge when unread threads exist in it. |

## Applying / removing labels

| ID | Requirement |
|----|-------------|
| REQ-LBL-10 | User can apply one or more labels to a selected thread from a label picker. |
| REQ-LBL-11 | User can remove a label from a thread, including the label whose view is currently open. |
| REQ-LBL-12 | Label apply/remove is optimistic: the UI updates immediately and the JMAP `Email/set` is fired in the background. See `11-optimistic-ui.md`. |
| REQ-LBL-13 | Label apply is available from: (a) the thread-list toolbar, (b) the open-thread toolbar, (c) the keyboard shortcut `l`. |

## Label views

| ID | Requirement |
|----|-------------|
| REQ-LBL-20 | Clicking a label in the sidebar opens a thread list filtered to that label (`Email/query` with `inMailbox=<label-id>`). |
| REQ-LBL-21 | The URL encodes the current label view so it is bookmarkable and shareable. |

## Colour storage

`Mailbox.role` is standard JMAP; `Mailbox.color` is not. Tabard requires herold to support a `color` property on `Mailbox` per the server contract (`../notes/server-contract.md`). If absent, tabard falls back to a deterministic colour derived from the mailbox ID and surfaces a one-time notice that colours are not persisted.
