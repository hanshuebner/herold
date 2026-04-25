# 05 — Categorisation

User-defined groupings of labels, rendered as a sidebar section. Distinct from Gmail's automatic Primary/Social/Promotions/Updates/Forums tabs (which are out of scope — see `../00-scope.md` NG7).

> **⚠ PLACEHOLDER** — capture data will show whether the user actually navigates to category-style views, and if so, how many. If the count is low, this section can be cut. See `../notes/capture-integration.md`.

## Preliminary requirements

| ID | Requirement |
|----|-------------|
| REQ-CAT-01 | The sidebar supports a "Categories" section containing user-defined category groups. |
| REQ-CAT-02 | A category is a named group of labels rendered together. |
| REQ-CAT-03 | User can assign a label to a category and remove it again. |
| REQ-CAT-04 | The thread list can be filtered to a category — shows all threads matching any label in the category (`Email/query` with `inMailboxOtherThan` semantics inverted: `OR` over the category's mailbox IDs). |

## Storage

Categories are tabard-local metadata, not a JMAP concept. They live in `localStorage` keyed by account ID. There is no v1 sync of category definitions across devices.

## Open questions

- Does the user need this at all, or does the existing nested-label tree suffice?
- If kept, should the category-to-labels mapping be server-stored (custom property on a synthetic mailbox? a Sieve comment block?) for cross-device sync?

Both questions resolve once capture data lands.
