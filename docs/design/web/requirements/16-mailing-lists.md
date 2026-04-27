# 16 — Mailing lists

When a message carries the RFC 2369 `List-*` headers, the suite surfaces list metadata and per-list affordances. Distinct from the unsubscribe handling in `14-unsubscribe.md` (which deals only with `List-Unsubscribe` and is a member of this header family).

## Detection

| ID | Requirement |
|----|-------------|
| REQ-LIST-01 | The suite parses the RFC 2369 list-headers when present: `List-ID`, `List-Help`, `List-Subscribe`, `List-Post`, `List-Owner`, `List-Archive`. The presence of `List-ID` is the single signal that "this is a mailing list message". |
| REQ-LIST-02 | The list's display label is taken from `List-ID`'s description part (`"Project X discuss" <projectx-discuss.example.com>` → "Project X discuss"); fallback to the local part of the angle-bracketed identifier. |

## Surface

| ID | Requirement |
|----|-------------|
| REQ-LIST-10 | A small chip with the list's display label is shown in the thread header area, beside the sender. The chip uses `--support-info` background. |
| REQ-LIST-11 | Hovering the chip reveals a popover with the available `List-*` actions (see below). |
| REQ-LIST-12 | The chip appears only on threads where every message carries the same `List-ID`. A thread that's been forwarded out of the list and replied to (so some messages have the list header and some don't) gets the chip on the relevant messages only — not on the thread overall. |

## Actions

| ID | Requirement |
|----|-------------|
| REQ-LIST-20 | "View archive" — opens the `List-Archive` URL (HTTPS only — see `14-unsubscribe.md` REQ-UNS-04 logic) in a new tab. Hidden if `List-Archive` is absent. |
| REQ-LIST-21 | "Get help" — opens the `List-Help` URL (HTTPS or `mailto:`) in a new tab or compose window respectively. Hidden if absent. |
| REQ-LIST-22 | "Reply to list" — when composing a reply, this option uses the `List-Post` address as `to:` instead of the original sender. Hidden if `List-Post` is absent or if `List-Post: NO` (which RFC 2369 §3.4 reserves for "no posting"). |
| REQ-LIST-23 | "Mute this list" — adds the `List-ID` to a client-local mute set. New messages from the list go to the Muted view (a synthetic sidebar entry) instead of the Inbox. The behaviour is purely a suite-local view filter; it does not invoke a server-side filter. (Server-side filtering on `List-ID` is handled via `04-filters.md`.) |
| REQ-LIST-24 | "Unmute" — reverse of REQ-LIST-23, available from the Muted view. |
| REQ-LIST-25 | "Make a filter for this list" — opens the filter editor (`04-filters.md`) pre-populated with `header:list-id contains <list-id>` as the condition. The user picks the action (label, archive, etc.). |

## Threading and subject prefixes

Mailing list software typically prepends a tag to the subject (`[List-Name] Original Subject`). This breaks naive threading because reply subjects without the prefix don't match.

| ID | Requirement |
|----|-------------|
| REQ-LIST-30 | When computing thread groupings, the suite strips a leading `[xxx]` tag from the subject before normalising for thread comparison. Stripping applies regardless of `List-*` header presence (other systems use bracket prefixes too). |
| REQ-LIST-31 | The list chip serves as the visual anchor that the message is from a list, replacing the need to retain the bracket prefix in the rendered subject. The original subject (with prefix) remains available in raw-headers view. |
| REQ-LIST-32 | The suite does NOT auto-rewrite outgoing reply subjects. If the original had a bracket prefix, the reply subject inherits it as-is — the list software typically wants the prefix preserved for its own threading. |

## Mute set storage

| ID | Requirement |
|----|-------------|
| REQ-LIST-40 | The mute set is `localStorage` per account, keyed by `List-ID`. No v1 server-side sync. |
| REQ-LIST-41 | Bulk-mute is not exposed in v1; "mute this list" is one list at a time. |

## Out of scope

- Detecting mailing-list-style messages from senders that don't set `List-*` headers. (Some commercial senders use `List-Unsubscribe` only — those are handled by `14-unsubscribe.md`.)
- Auto-folder-by-list. The user creates a filter explicitly via REQ-LIST-25.
- List archive search. The archive is a third-party URL; in-archive search is its problem.
