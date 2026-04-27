# 04 — Filters

Filters are user-authored rules that act on incoming mail: apply a label, archive, mark read, delete, forward. Stored server-side as Sieve scripts (RFC 9007); herold supports `urn:ietf:params:jmap:sieve` per resolved Q2.

> **⚠ PLACEHOLDER** — capture data will inform some details (how often the user manages filters, the depth of the test-against-existing-mail flow). The structural rules are concrete.

## Conditions (minimum viable set)

| ID | Requirement |
|----|-------------|
| REQ-FLT-01 | A filter can match on: From address, To address, Subject (contains / equals), Has-attachment (boolean). |
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
