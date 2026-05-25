# 04 — Filters

Filters are user-authored rules that act on incoming mail: apply a label, archive, mark read, delete, forward. Stored server-side as Sieve scripts (RFC 9007); herold supports `urn:ietf:params:jmap:sieve` per resolved Q2.

> **⚠ PLACEHOLDER** — capture data will inform some details (how often the user manages filters, the depth of the test-against-existing-mail flow). The structural rules are concrete.

## Conditions (minimum viable set)

| ID | Requirement |
|----|-------------|
| REQ-FLT-01 | A filter can match on: From address, From domain, To address, Subject (contains / equals), List-Id (equals; matches the `List-Id` header per RFC 2919), Has-attachment (boolean). The condition-field enum carried in `FiltersForm.svelte` and in the Sieve compiler is the closed set `from` \| `from-domain` \| `to` \| `subject` \| `list-id` \| `has-attachment` \| `thread-id`. |
| REQ-FLT-02 | Multiple conditions combine with AND logic. (OR support is post-v1.) |
| REQ-FLT-03 | Address-field conditions support a wildcard (`*`). |

## Actions

| ID | Requirement |
|----|-------------|
| REQ-FLT-10 | A filter can apply a label. |
| REQ-FLT-11 | A filter can skip the inbox (archive on arrival). |
| REQ-FLT-12 | A filter can mark as read. |
| REQ-FLT-13 | A filter can delete (move to Trash). |
| REQ-FLT-14 | A filter can forward to an address. |
| REQ-FLT-15 | Multiple actions combine on a single match. |

## Management

| ID | Requirement |
|----|-------------|
| REQ-FLT-20 | User can create, edit, reorder, enable/disable, and delete filters. |
| REQ-FLT-21 | User can test a filter against existing mail ("apply to matching conversations"). |
| REQ-FLT-22 | Filters are stored as Sieve scripts via `Sieve/set` (RFC 9007). Required server capability: `urn:ietf:params:jmap:sieve` — committed by herold. The suite does not implement a client-side filtering fallback. |

## UI

| ID | Requirement |
|----|-------------|
| REQ-FLT-30 | The filter editor expresses conditions and actions in a structured form, not raw Sieve. The Sieve compilation is internal. |
| REQ-FLT-31 | The filter list shows each filter's conditions and actions in human-readable form, plus enabled/disabled state. |
| REQ-FLT-32 | **Seed conditions.** `FiltersForm.svelte` accepts an optional seed-conditions list when its create flow is invoked from an external call site (notably the per-message kebab's "Filter messages like this", `02-mail-basics.md` REQ-MAIL-138). When the seed list is present, the editor opens with those conditions pre-populated and the default empty single-condition row (`from contains ''`) is replaced; the user may edit, add, or remove conditions before saving. When the seed list is absent (the existing entry point from Settings → Filters → New), the editor opens with the existing empty default. The seed conditions use the same shape as filter-rule conditions (`{field, op, value}`), constrained to the closed enum from REQ-FLT-01. The call site is responsible for deriving the seeds — e.g. for "Filter messages like this", From address, From domain, Subject (with `Re:` / `Fwd:` prefix stripped), and List-Id (when present) are candidates; the seed list typically contains one or two entries and the user picks among them or adds others. The entry-point contract for navigation is implementation-defined (URL search parameter on `/settings/filters/new` is the expected mechanism); the suite never serialises filter contents into a URL beyond the seed conditions themselves. |
