# 12 — Workflows

A workflow is a named, repeatable sequence of actions the user performs to achieve a goal. Workflows tie individual feature requirements together and are the unit of acceptance testing (`../implementation/03-testing-strategy.md`).

> **⚠ PLACEHOLDER (partial)** — the WFs below come from the stated feature set. Additional workflows will be added from `top_workflows` in the gmail-logger analysis report (action bigrams with count ≥ 5). Each new WF gets a `REQ-WF-nn` ID and an acceptance scenario in the testing doc.

## REQ-WF-01: Inbox-zero pass

**Trigger:** user opens Inbox.
**Goal:** every thread in the Inbox is dealt with.
**Success state:** Inbox thread count = 0.

| Step | Action | System response |
|------|--------|-----------------|
| 1 | Open Inbox | Thread list loads, sorted by date desc |
| 2 | `j` / `k` to focus a thread | Row highlighted |
| 3a | `e` | Thread archived (optimistic), removed from list |
| 3b | `l`, type label, Enter | Label applied; thread leaves Inbox if its inbox-membership was removed |
| 3c | `b`, pick snooze time | Thread snoozed; disappears from list |
| 3d | `r` | Compose reply opens; on send, thread archived (per setting) or stays |
| 4 | Repeat | |

## REQ-WF-02: Apply label to thread

**Trigger:** user wants to categorise a thread.
**Keyboard:** `l` → type label name → Enter. **Mouse:** toolbar Label dropdown → click.

| Step | Action | System response |
|------|--------|-----------------|
| 1 | Select thread(s) | Selection state updated |
| 2 | Open label picker | Picker shows label list with search |
| 3 | Type to filter labels | List narrows |
| 4 | Pick label(s) | Checkmarks update |
| 5 | Confirm / dismiss picker | Labels applied optimistically; `Email/set` issued |

## REQ-WF-03: Snooze a thread

**Trigger:** user wants to defer dealing with a thread.
**Keyboard:** `b`.

| Step | Action | System response |
|------|--------|-----------------|
| 1 | Focus / open thread | Thread selected |
| 2 | `b` (or click Snooze) | Snooze picker appears |
| 3 | Pick preset or custom time | Thread disappears from Inbox; toast "Snoozed until …" with Undo |
| 4 | (Optional) click Undo | Snooze cancelled; thread restored |

## REQ-WF-04: Search and act

**Trigger:** user needs to find a thread and act on it.

| Step | Action | System response |
|------|--------|-----------------|
| 1 | `/` | Search bar focused |
| 2 | Type query | (Optional) live suggestions |
| 3 | Enter | Result list shown |
| 4 | Click / Enter on a result | Thread opens |
| 5 | Perform an action (reply, label, archive) | Action applied; back returns to results scrolled to the same row |

## REQ-WF-05: Compose and send

**Trigger:** user wants to write a new message.
**Keyboard:** `c`.

| Step | Action | System response |
|------|--------|-----------------|
| 1 | `c` | Compose opens |
| 2 | Type recipient(s) | Autocomplete suggestions |
| 3 | Tab to Subject, type | |
| 4 | Tab to body, write | Autosave fires periodically |
| 5 | `Ctrl+Enter` (or click Send) | Compose closes; toast "Message sent — Undo" for 5 s |
| 5a | Click Undo | Compose re-opens with content intact; submission cancelled |

## REQ-WF-06: Filter creation

> **⚠ PLACEHOLDER** — included only if filter management appears in capture data. If absent (filters set up once and forgotten), this WF is cut.

## REQ-WF-07+: Captured workflows

> **⚠ PLACEHOLDER** — populate from `top_workflows` (bigrams ≥ 5 occurrences). Template:
>
> **REQ-WF-XX: [name from bigram]**
> Trigger: [first action]
> Frequency: [count]
> Steps: …
