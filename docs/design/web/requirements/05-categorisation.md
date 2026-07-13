# 05 — Categorisation

*(Rewritten 2026-07-13 by ADR-0004. A category and a label are one primitive; a
category's disposition decides how loudly its mail enters the inbox. Superseded:
REQ-CAT-01, -10, -13, -40. The v1 framing — "tabs are a visual filter, not a
separate location" — is reversed: a tab *is* a label, viewed loudly.)*

A **category is a label** (`03-labels.md`). It carries a name, colour and parent
like any label. A category that also carries a natural-language **definition**
fills itself; one without a definition is a plain label the user applies by hand.
Every category carries a **disposition**, and that single setting decides
everything about how its mail reaches the user.

Server contract: `../server/requirements/06-filtering.md` Part C.

## The disposition, and why there is only one knob

| Disposition | Inbox | Presentation | Badge | Push |
|---|---|---|---|---|
| `pinned` | yes | own tab (max 5) | yes | yes |
| `bundled` | yes | one collapsed row in the stream | inline count only | no |
| `daily` | yes | bundled, surfaced once a day | inline count only | no |
| `weekly` | yes | bundled, surfaced once a week | inline count only | no |
| `filed` | no | reachable via the category only | no | no |
| `none` | yes | no lane; a search facet | no | no |

`filed` is "label and archive". `pinned` is "category tab". They are the same
mechanism at two settings — which is the whole point.

| ID | Requirement |
|----|-------------|
| REQ-CAT-01 | A message may carry several categories. The inbox shows it **once**, in the lane of its highest-priority category (REQ-CAT-05). Search sees all of them. |
| REQ-CAT-02 | Categorisation is server-side. The suite never classifies locally. |
| REQ-CAT-03 | A message with no category is shown under the category holding the `primary` role. |
| REQ-CAT-04 | Disposition is a single per-category setting and drives inbox membership, stream presentation, unread badging, and push notification together. The suite exposes no independent mute, badge, or notify toggle for a category. |
| REQ-CAT-05 | Categories occupy one user-ordered priority list. It is the only place a message matching two categories (a hobby vendor's sale is both Hobby and Promotions) is resolved. The user reorders by drag. |

## Inbox view

The default Inbox is a **complete stream**: every message, in date order, with
categories compressed rather than removed. Nothing is ever reachable only through
a lane the user has stopped checking.

```
  [Allgemein] [Hobby]              <- pinned tabs, max 5, opt-in
  ------------------------------------------------------------
    Torsten Bulck      Re: CC 2027            10. Juli
  > Werbung (12)                              10. Juli
    Anna Meier         Rechnung Juni           9. Juli
  > Soziale Netze (5)                          8. Juli
    Sparkasse          Kontoauszug             8. Juli
```

| ID | Requirement |
|----|-------------|
| REQ-CAT-10 | The Inbox renders as one stream in date order. A `bundled` category collapses to a single row, positioned by its newest member, showing the category name, its unread count, and a preview of its senders. Expanding a bundle reveals its messages inline. |
| REQ-CAT-11 | A `pinned` category renders as a tab above the stream. **At most 5** may be pinned. Pinning is an explicit user act; attempting a sixth prompts the user to unpin one. Selecting a tab filters the Inbox to that category. |
| REQ-CAT-12 | An empty bundle does not appear in the stream (hide-when-empty). A category in the sidebar may be set to appear only when it has unread mail (show-if-unread), per REQ-LBL-07a. These are two different predicates on two different surfaces, and both apply to one category. |
| REQ-CAT-13 | A category **is** a label. It appears in the sidebar under Labels, is a drag-and-drop target, and opens a thread list like any label (REQ-LBL-20). Its inbox behaviour is a consequence of its disposition, not of it being a different kind of object. |
| REQ-CAT-14 | Searching from a category-filtered Inbox preserves the filter unless the user explicitly changes it. |
| REQ-CAT-15 | A bundle offers a one-gesture sweep: mark the whole bundle read, or archive it, without expanding it. |
| REQ-CAT-16 | A `daily` or `weekly` bundle surfaces at a fixed hour the user sets (default 07:00 local, REQ-FILT-224) and displays when it will next surface. Its mail is reachable at any time through the category itself; deferral governs the inbox stream, never access. |

## Assigning and correcting

| ID | Requirement |
|----|-------------|
| REQ-CAT-20 | The user recategorises a thread from the thread-list toolbar, the open-thread toolbar, the context menu, or the `m` shortcut. This fires `Email/set` on the `$category-*` keywords. |
| REQ-CAT-21 | A user assignment is recorded with `user` provenance and is **never** overwritten by the classifier or by a re-categorisation run (REQ-FILT-206, REQ-FILT-220). |
| REQ-CAT-22 | On recategorising, the suite offers to **make it stick**: add this sender, domain, or list-id to the target category's named list. Accepting writes a deterministic rule — no LLM call — so the next message from that sender lands correctly. Declining files this one message only. |
| REQ-CAT-23 | Recategorisation applies at thread granularity by default; a per-message override is available from the message's own action menu. |

## Configuring a category

| ID | Requirement |
|----|-------------|
| REQ-CAT-40 | **Reinstated** (withdrawn in v1, restored by ADR-0004). The category set is user-owned, stored data. The user creates, renames, recolours, reorders, deletes, and sets the disposition of any category, including the five shipped defaults. |
| REQ-CAT-41 | A category may carry a free-text **definition** describing what mail belongs to it ("mail about my model railway hobby: club newsletters, these vendors, forum digests"). A category with no definition is a plain hand-applied label. |
| REQ-CAT-42 | The suite shows the **compiled rules** for a category, each carrying the definition line that produced it, and lets the user disable an individual rule without editing the definition. The user can see the filter, not just the prose that generated it. |
| REQ-CAT-43 | Saving a definition does not silently change the user's mail. The suite runs the compile flow (REQ-FILT-217) and renders a **preview diff**: "12 messages would change category", listed. Nothing is applied until the user accepts. The previous ruleset is retained; rollback is one click. |
| REQ-CAT-44 | **Per-message transparency (G14).** The message-inspect view shows, for any categorised message, the rule trace: which rules fired, what each contributed, and where the total landed. This is a decision, not a narrative. Operator guardrails are excluded (REQ-FILT-67). |
| REQ-CAT-45 | The settings panel states plainly what leaves the machine. With a compiled ruleset, **no message content is sent anywhere** — the definition text and the rule schema are what reach the compiler. Where the escalation band sends a message to an LLM, the panel says so. |
| REQ-CAT-46 | Named lists (the senders, domains, and list-ids a category matches) are editable as chips. This is where routine tuning happens and it needs no LLM call. |
| REQ-CAT-47 | A deployment with **no LLM endpoint** presents a fully working category set from the shipped defaults. Definition editing is disabled with an explanation; everything else — dispositions, priority, lists, pinning, corrections — works. |
| REQ-CAT-48 | "Re-categorise" re-runs the evaluator over recent mail with a progress indicator. It never touches `user`-provenance assignments. |

## Storage and contract

| ID | Requirement |
|----|-------------|
| REQ-CAT-50 | Categories, their definitions, compiled rulesets, dispositions, priority order, and named lists are per-account server state and sync across devices. A fresh suite tab reads them on bootstrap. |
| REQ-CAT-51 | The server contract is `https://netzhansa.com/jmap/categorise` (see `../notes/server-contract.md`): the category collection with CRUD, the compile-and-preview flow, per-message provenance and trace, and re-categorisation. |
| REQ-CAT-52 | Each category is exposed as a **virtual mailbox** so IMAP clients (Thunderbird, Apple Mail) can browse it as a folder. Membership remains a keyword — the store holds no second copy of the message and re-categorisation does not churn UIDs (ADR-0004 §3). |
| REQ-CAT-53 | Writes from IMAP into a category folder mirror Gmail (REQ-FILT-223): copy assigns the category, move assigns it and files the message out of the inbox, expunge removes it. A category assigned from IMAP carries `user` provenance, so the Suite must render it as a user assignment and never let the classifier overwrite it. |

## Cross-references

- Labels — `03-labels.md`. A category is a label; that document defines the shared object.
- Filters — `04-filters.md`. Rules can test and act on `$category-*` like any other keyword.
- The rule language, evaluator, backtest, and trace — ADR-0002. Categorisation introduces no second rule format.
- Plugin state and settings UI, which the compiled ruleset depends on — ADR-0003.
