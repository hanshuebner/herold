# 03 — Labels

*(Revised 2026-07-13 by ADR-0004: a label and a category are one primitive. A
label that carries a definition fills itself; a label that carries a disposition
decides how loudly its mail enters the inbox. REQ-LBL-07 is overridden by
disposition.)*

Labels are JMAP `Mailbox` objects (per `01-data-model.md`). A message belongs to
one or more labels; system labels (Inbox, Sent, Drafts, Spam, Trash, Archive) are
mailboxes with the corresponding standard `role`.

**A label and a category are the same object.** What a category adds is optional:

| Field | Meaning | A plain label |
|---|---|---|
| `definition` | Natural-language prose: what mail belongs here | absent — the user applies it by hand |
| `rules` | The ruleset compiled from `definition` | absent |
| `disposition` | How loudly its mail enters the inbox (`05-categorisation.md`) | `none` — no inbox effect |
| `priority` | Position in the single ordered category list | unranked |

So "label and archive" is a label with `filed` disposition, and a Gmail-style tab
is a label with `pinned` disposition. There is no second mechanism. The category
surface is specified in `05-categorisation.md`; this document defines the shared
object and its CRUD.

Membership is stored as a keyword and each label is exposed as a virtual mailbox
for IMAP clients (ADR-0004 §3), so a message is never stored twice and
recategorisation does not churn UIDs.

## Label management (CRUD)

| ID | Requirement |
|----|-------------|
| REQ-LBL-01 | User can create a label. Required field: name. Optional: parent label, colour, definition, disposition, priority. |
| REQ-LBL-02 | User can rename a label. |
| REQ-LBL-03 | User can delete a label. The client warns if the label contains threads. Deleting a label does NOT delete its messages — it removes the label from each affected message and discards the label's definition, compiled ruleset, and named lists. |
| REQ-LBL-04 | User can assign a colour to a label from a fixed palette of ≥ 12 colours. |
| REQ-LBL-05 | User can nest a label under a parent label, at least 3 levels deep. |
| REQ-LBL-06 | The label list in the sidebar is sorted: system labels first (fixed order), then user labels alphabetically within each parent. The **priority** order (REQ-CAT-05) is a separate, explicitly-ordered list used to resolve a message matching several labels; it does not reorder the sidebar. |
| REQ-LBL-06a | System mailbox order in the sidebar (`09-ui-layout.md` REQ-UI-13b): Inbox → Snoozed → Important → Sent → Drafts → All Mail. Spam, Trash, and any further system folders sit under a "More" expander, default collapsed. |
| REQ-LBL-07 | **Superseded by REQ-FILT-204.** A label's unread badge is governed by its disposition, not applied unconditionally. A `pinned` label badges; a `bundled`, `daily`, `weekly`, or `filed` one does not — suppressing attention is the entire purpose of bundling. A plain label (disposition `none`) badges when it has unread threads, which is the v1 behaviour. |
| REQ-LBL-07a | A label may be set to appear in the sidebar only when it has unread mail (show-if-unread). This is distinct from a bundle's hide-when-empty rule in the inbox stream (REQ-CAT-12): navigation surfaces key on unread, stream surfaces key on presence, and both may apply to one label. |

## Applying / removing labels

| ID | Requirement |
|----|-------------|
| REQ-LBL-10 | User can apply one or more labels to a selected thread from a label picker. |
| REQ-LBL-11 | User can remove a label from a thread, including the label whose view is currently open. |
| REQ-LBL-12 | Label apply/remove is optimistic: the UI updates immediately and the JMAP `Email/set` is fired in the background. See `11-optimistic-ui.md`. |
| REQ-LBL-13 | Label apply is available from: (a) the thread-list toolbar, (b) the open-thread toolbar, (c) the keyboard shortcut `l`. |
| REQ-LBL-14 | A label applied by the user is recorded with `user` provenance and is never overwritten by the classifier or a re-categorisation run (REQ-FILT-206). |

## Label views

| ID | Requirement |
|----|-------------|
| REQ-LBL-20 | Clicking a label in the sidebar opens a thread list filtered to that label. |
| REQ-LBL-21 | The URL encodes the current label view so it is bookmarkable and shareable. |

## Colour storage

`Mailbox.role` is standard JMAP; `Mailbox.color` is not. The suite requires herold to support a `color` property on `Mailbox` per the server contract (`../notes/server-contract.md`). If absent, the suite falls back to a deterministic colour derived from the mailbox ID and surfaces a one-time notice that colours are not persisted.
