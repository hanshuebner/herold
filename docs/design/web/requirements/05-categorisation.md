# 05 — Automatic categorisation

Inbound messages are classified into categories by an LLM running on herold. The suite renders the categories as inbox tabs (Gmail-style) and lets the user re-classify, override, and configure both the category set and the classifier prompt.

This replaces the original v1 framing of "user-defined groupings of labels". Resolved Q11.

## Defaults: Gmail-style categories

Out of the box the category set matches Gmail's:

- **Primary** — direct correspondence, transactional, anything that doesn't fit another category.
- **Social** — notifications from social networks, messaging apps.
- **Promotions** — marketing, offers, retail.
- **Updates** — receipts, statements, automated notifications, package tracking.
- **Forums** — mailing-list discussion, online groups.

A message that doesn't classify into the user's set falls under Primary.

## Mechanism

Herold runs a per-message LLM classification on each delivered Email. The result is applied as a `$category-<name>` keyword (e.g. `$category-promotions`) on the Email. The suite reads the keyword to render the category tab.

| ID | Requirement |
|----|-------------|
| REQ-CAT-01 | Each Email has at most one `$category-<name>` keyword set by herold's classifier on delivery. |
| REQ-CAT-02 | The classifier is server-side. The suite never runs classification locally. |
| REQ-CAT-03 | When `$category-<name>` is absent (legacy mail, classifier failure), the Email is treated as Primary. The suite does not retroactively classify; the user can use "Re-categorise inbox" (REQ-CAT-30). |

## Inbox view

| ID | Requirement |
|----|-------------|
| REQ-CAT-10 | The Inbox renders with category tabs at the top: Primary | Social | Promotions | Updates | Forums (in the user's configured order, defaulting to that ordering). |
| REQ-CAT-11 | The active tab filters the Inbox to messages with the corresponding `$category-<name>` keyword (or no category keyword, for the Primary tab). |
| REQ-CAT-12 | Each tab shows an unread count badge. |
| REQ-CAT-13 | Tabs are not nested labels. They do not appear under "Labels" in the sidebar. They are a visual filter on the Inbox view, not a separate location. |
| REQ-CAT-14 | Searching from a category-filtered Inbox preserves the filter unless the user explicitly changes it. |

## Re-classification

| ID | Requirement |
|----|-------------|
| REQ-CAT-20 | Right-click on a thread (or `m` shortcut, or "Move to category" in the toolbar) opens a category picker. Selecting a category fires `Email/set` to update the `$category-<name>` keyword. |
| REQ-CAT-21 | Re-classification is treated as user-correction by herold's classifier (a feedback signal for prompt tuning). The mechanism is herold's responsibility; the suite's contract is "we set the keyword, herold does what it does with that". |
| REQ-CAT-22 | Re-classification is applied at thread granularity by default — every Email in the thread gets the same `$category-<name>`. (A per-message override is possible via opening the message and using its own action menu.) |

## Bulk re-categorisation

| ID | Requirement |
|----|-------------|
| REQ-CAT-30 | The settings panel exposes "Re-categorise inbox": triggers herold to re-run the classifier on the user's recent inbox (e.g. last 1000 messages). Slow operation; runs in background with a progress indicator in the chrome. |
| REQ-CAT-31 | Re-categorisation is also triggered automatically when the user changes the category prompt or category set (REQ-CAT-40, REQ-CAT-41). |

## User configuration

The user can edit both the category set and the prompt that classifies into them.

| ID | Requirement |
|----|-------------|
| REQ-CAT-40 | The category set is editable in settings: rename, reorder, add, remove. The Primary category cannot be removed (it's the fallback); other defaults can. |
| REQ-CAT-41 | The classifier prompt is editable in settings (advanced section). The default prompt approximates Gmail's behaviour. The suite provides the editor; herold validates and applies the prompt. |
| REQ-CAT-42 | A reset-to-default control reverts the prompt and category set to the shipped defaults. |
| REQ-CAT-43 | When the user saves prompt or category-set changes, herold runs the re-categorisation flow (REQ-CAT-30) automatically; the suite's UI shows the progress. |

## Storage and contract

| ID | Requirement |
|----|-------------|
| REQ-CAT-50 | The category set and the classifier prompt are stored server-side, per account. They sync across devices automatically (a fresh the suite tab reads them on bootstrap). |
| REQ-CAT-51 | The server contract for categorisation is `https://tabard.dev/jmap/categorise` (proposed URN — see `../notes/server-contract.md`). It declares: per-account category set, per-account prompt, per-Email `$category-*` keyword application by the classifier on delivery, and the re-classification API. |

## Cross-references

- The classifier itself (LLM model, hosting, prompt engineering) is herold's responsibility. The suite renders results; herold does the work. See `../notes/herold-coverage.md` for status.
- Spam filtering is a separate concern (herold's existing LLM-based spam plugin produces `$junk` and the spam mailbox); categorisation runs independently of spam classification.
- Filters (`04-filters.md`) can read and act on `$category-*` keywords, just like any other keyword. A user could write a filter "if `$category-promotions` archive after 7 days" — the mechanism is generic.
